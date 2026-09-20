package backup

import (
	"archive/tar"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type Manifest struct {
	Version        int               `json:"version"`
	CreatedAt      time.Time         `json:"createdAt"`
	KeyFingerprint string            `json:"keyFingerprint"`
	Files          map[string]string `json:"files"`
}

func fingerprint(key []byte) string {
	h := sha256.Sum256(append([]byte("ddyidc-backup-key-check-v1:"), key...))
	return hex.EncodeToString(h[:])
}
func fileSHA(path string) (string, error) {
	f, e := os.Open(path)
	if e != nil {
		return "", e
	}
	defer f.Close()
	h := sha256.New()
	if _, e = io.Copy(h, f); e != nil {
		return "", e
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
func allowedMember(name string) bool {
	return name == "mysql.sql" || name == "manifest.json" || name == "storage" || (strings.HasPrefix(name, "storage/") && !strings.Contains(name, "\\") && filepath.ToSlash(filepath.Clean(name)) == name && !strings.HasSuffix(name, "/"))
}

// Archive members are an allowlist, never the entire local disaster backup.
func pack(payload, destination string, key []byte) error {
	manifest := Manifest{Version: 1, CreatedAt: time.Now(), KeyFingerprint: fingerprint(key), Files: map[string]string{}}
	paths := []string{"mysql.sql"}
	if e := filepath.WalkDir(filepath.Join(payload, "storage"), func(p string, d fs.DirEntry, e error) error {
		if e != nil {
			return e
		}
		if d.Type()&os.ModeSymlink != 0 {
			return errors.New("附件目录不允许符号链接")
		}
		if d.IsDir() {
			return nil
		}
		if !d.Type().IsRegular() {
			return errors.New("附件包含特殊文件")
		}
		rel, e := filepath.Rel(payload, p)
		if e != nil {
			return e
		}
		paths = append(paths, filepath.ToSlash(rel))
		return nil
	}); e != nil {
		return e
	}
	for _, name := range paths {
		sum, e := fileSHA(filepath.Join(payload, filepath.FromSlash(name)))
		if e != nil {
			return e
		}
		manifest.Files[name] = sum
	}
	raw, _ := json.Marshal(manifest)
	output, e := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if e != nil {
		return e
	}
	success := false
	defer func() {
		output.Close()
		if !success {
			os.Remove(destination)
		}
	}()
	gz := gzip.NewWriter(output)
	tw := tar.NewWriter(gz)
	for _, name := range paths {
		f, e := os.Open(filepath.Join(payload, filepath.FromSlash(name)))
		if e != nil {
			return e
		}
		info, e := f.Stat()
		if e != nil {
			f.Close()
			return e
		}
		e = tw.WriteHeader(&tar.Header{Name: name, Mode: 0600, Size: info.Size(), ModTime: info.ModTime(), Typeflag: tar.TypeReg})
		if e == nil {
			_, e = io.Copy(tw, f)
		}
		f.Close()
		if e != nil {
			return e
		}
	}
	if e = tw.WriteHeader(&tar.Header{Name: "manifest.json", Mode: 0600, Size: int64(len(raw)), Typeflag: tar.TypeReg}); e != nil {
		return e
	}
	if _, e = tw.Write(raw); e != nil {
		return e
	}
	if e = tw.Close(); e != nil {
		return e
	}
	if e = gz.Close(); e != nil {
		return e
	}
	if e = output.Sync(); e != nil {
		return e
	}
	success = true
	return nil
}

// Reject traversal, links, unexpected configuration files, duplicate members and
// decompression bombs before any database or live storage mutation.
func unpack(archive, destination string, key []byte) error {
	input, e := os.Open(archive)
	if e != nil {
		return e
	}
	defer input.Close()
	gz, e := gzip.NewReader(input)
	if e != nil {
		return errors.New("备份压缩格式无效")
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	seen := map[string]string{}
	var manifest Manifest
	hasManifest := false
	var total int64
	for count := 0; ; count++ {
		h, e := tr.Next()
		if e == io.EOF {
			break
		}
		if e != nil {
			return errors.New("备份读取失败")
		}
		if count > 1000000 || h.Size < 0 || h.Size > 100<<30 || total+h.Size > 200<<30 {
			return errors.New("备份超出恢复大小限制")
		}
		total += h.Size
		if !allowedMember(h.Name) || h.Typeflag != tar.TypeReg {
			return errors.New("备份包含不允许的路径或文件类型")
		}
		if _, ok := seen[h.Name]; ok {
			return errors.New("备份包含重复文件")
		}
		seen[h.Name] = ""
		if h.Name == "manifest.json" {
			if h.Size > 32<<20 {
				return errors.New("备份清单过大")
			}
			if e = json.NewDecoder(io.LimitReader(tr, h.Size)).Decode(&manifest); e != nil {
				return errors.New("备份清单无效")
			}
			hasManifest = true
			continue
		}
		target := filepath.Join(destination, filepath.FromSlash(h.Name))
		if e = os.MkdirAll(filepath.Dir(target), 0700); e != nil {
			return e
		}
		f, e := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if e != nil {
			return e
		}
		hash := sha256.New()
		n, e := io.Copy(io.MultiWriter(f, hash), tr)
		closeErr := f.Close()
		if e != nil || closeErr != nil || n != h.Size {
			return errors.New("恢复临时文件写入失败")
		}
		seen[h.Name] = hex.EncodeToString(hash.Sum(nil))
	}
	delete(seen, "manifest.json")
	if !hasManifest || manifest.Version != 1 || manifest.KeyFingerprint != fingerprint(key) {
		return errors.New("备份版本或本机 AES 密钥不匹配，禁止恢复")
	}
	if len(manifest.Files) != len(seen) || manifest.Files["mysql.sql"] == "" {
		return errors.New("备份内容不完整")
	}
	for name, sum := range manifest.Files {
		if seen[name] != sum {
			return errors.New("备份文件哈希校验失败")
		}
	}
	return os.MkdirAll(filepath.Join(destination, "storage"), 0700)
}
