package certification

import (
	"bytes"
	"crypto/rand"
	crypt "ddyidc/pkg/crypto"
	"encoding/hex"
	"encoding/json"
	"errors"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"time"
)

type upload struct {
	Owner     int64
	Type      string
	Data      []byte
	CreatedAt time.Time
}

var fileID = regexp.MustCompile(`^[a-f0-9]{48}$`)

func (s *Service) readUpload(id string, owner int64, admin bool) (*upload, error) {
	if !fileID.MatchString(id) {
		return nil, errors.New("invalid file")
	}
	raw, err := os.ReadFile(filepath.Join(s.root, id))
	if err != nil {
		return nil, err
	}
	plain, err := crypt.Decrypt(string(raw), s.key)
	if err != nil {
		return nil, err
	}
	var file upload
	if err = json.Unmarshal([]byte(plain), &file); err != nil {
		return nil, err
	}
	if !admin && file.Owner != owner {
		return nil, errors.New("file access denied")
	}
	return &file, nil
}
func (s *Service) saveUpload(r *http.Request, owner int64) (string, error) {
	file, header, err := r.FormFile("file")
	if err != nil {
		return "", errors.New("请选择证件图片")
	}
	defer file.Close()
	ext := filepath.Ext(header.Filename)
	if ext != ".jpg" && ext != ".jpeg" && ext != ".png" {
		return "", errors.New("仅支持 jpg/jpeg/png")
	}
	raw, err := io.ReadAll(io.LimitReader(file, 5<<20+1))
	if err != nil || len(raw) == 0 || len(raw) > 5<<20 {
		return "", errors.New("文件大小必须在5MB以内")
	}
	cfg, format, err := image.DecodeConfig(bytes.NewReader(raw))
	if err != nil || cfg.Width <= 0 || cfg.Height <= 0 || int64(cfg.Width)*int64(cfg.Height) > 25_000_000 {
		return "", errors.New("图片内容或尺寸无效")
	}
	mime := http.DetectContentType(raw)
	if format != "jpeg" && format != "png" || mime != "image/jpeg" && mime != "image/png" {
		return "", errors.New("图片类型无效")
	}
	if _, _, err = image.Decode(bytes.NewReader(raw)); err != nil {
		return "", errors.New("图片内容损坏")
	}
	token := make([]byte, 24)
	if _, err = rand.Read(token); err != nil {
		return "", err
	}
	id := hex.EncodeToString(token)
	body, _ := json.Marshal(upload{owner, mime, raw, time.Now()})
	encrypted, err := crypt.Encrypt(string(body), s.key)
	if err != nil {
		return "", err
	}
	f, err := os.OpenFile(filepath.Join(s.root, id), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return "", err
	}
	_, err = f.WriteString(encrypted)
	closeErr := f.Close()
	if err != nil || closeErr != nil {
		os.Remove(filepath.Join(s.root, id))
		return "", errors.New("上传保存失败")
	}
	return id, nil
}
