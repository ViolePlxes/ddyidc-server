package backup

import (
	"archive/tar"
	"compress/gzip"
	"os"
	"path/filepath"
	"testing"
)

func TestBusinessArchiveAllowlistAndRoundtrip(t *testing.T) {
	root := t.TempDir()
	payload := filepath.Join(root, "payload")
	if e := os.MkdirAll(filepath.Join(payload, "storage/ticket-attachments"), 0700); e != nil {
		t.Fatal(e)
	}
	for name, content := range map[string]string{"mysql.sql": "-- real dump fixture\nSELECT 1;", "storage/ticket-attachments/id": "attachment bytes", "config.yaml": "MUST NOT UPLOAD", "secrets.json": "MUST NOT UPLOAD"} {
		if e := os.WriteFile(filepath.Join(payload, name), []byte(content), 0600); e != nil {
			t.Fatal(e)
		}
	}
	archive := filepath.Join(root, "backup.tar.gz")
	key := []byte("01234567890123456789012345678901")
	if e := pack(payload, archive, key); e != nil {
		t.Fatal(e)
	}
	destination := filepath.Join(root, "restored")
	if e := os.MkdirAll(destination, 0700); e != nil {
		t.Fatal(e)
	}
	if e := unpack(archive, destination, key); e != nil {
		t.Fatal(e)
	}
	if b, e := os.ReadFile(filepath.Join(destination, "storage/ticket-attachments/id")); e != nil || string(b) != "attachment bytes" {
		t.Fatal("attachment changed")
	}
	for _, name := range []string{"config.yaml", "secrets.json"} {
		if _, e := os.Stat(filepath.Join(destination, name)); !os.IsNotExist(e) {
			t.Fatal("configuration included")
		}
	}
	other := filepath.Join(root, "other")
	os.MkdirAll(other, 0700)
	if e := unpack(archive, other, []byte("different key")); e == nil {
		t.Fatal("wrong AES key accepted")
	}
}
func TestArchiveRejectsTraversalAndSymlink(t *testing.T) {
	for _, header := range []*tar.Header{{Name: "storage/../../escape", Typeflag: tar.TypeReg}, {Name: "storage/link", Typeflag: tar.TypeSymlink, Linkname: "/etc/ddyidc/secrets"}, {Name: "config.yaml", Typeflag: tar.TypeReg}} {
		root := t.TempDir()
		archive := filepath.Join(root, "bad.tar.gz")
		f, _ := os.Create(archive)
		gz := gzip.NewWriter(f)
		tw := tar.NewWriter(gz)
		tw.WriteHeader(header)
		tw.Close()
		gz.Close()
		f.Close()
		dest := filepath.Join(root, "incoming")
		os.MkdirAll(dest, 0700)
		if e := unpack(archive, dest, nil); e == nil {
			t.Fatal("unsafe archive accepted", header.Name)
		}
	}
}
func TestPackRejectsStorageSymlink(t *testing.T) {
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, "storage"), 0700)
	os.WriteFile(filepath.Join(root, "mysql.sql"), []byte("sql"), 0600)
	if e := os.Symlink("/etc/passwd", filepath.Join(root, "storage/link")); e != nil {
		t.Skip(e)
	}
	if e := pack(root, filepath.Join(t.TempDir(), "bad.tar.gz"), nil); e == nil {
		t.Fatal("symlink accepted")
	}
}
func TestEndpointValidationAndSecretMask(t *testing.T) {
	for _, endpoint := range []string{"http://oss-cn-beijing.aliyuncs.com", "https://127.0.0.1", "https://oss-cn-beijing.aliyuncs.com.evil.test", "https://user:secret@oss-cn-beijing.aliyuncs.com", "https://oss-cn-beijing.aliyuncs.com/path"} {
		c := defaults()
		c.Endpoint = endpoint
		if validate(&c) == nil {
			t.Fatal("unsafe endpoint", endpoint)
		}
	}
	c := defaults()
	c.AccessKeyID = "LTAI123456789012"
	c.AccessKeySecret = "not-returned"
	p := c.public()
	if p.AccessKeyMasked == c.AccessKeyID || !p.SecretConfigured {
		t.Fatal("mask failed")
	}
	c.Prefix = "../other/"
	if validate(&c) == nil {
		t.Fatal("unsafe prefix")
	}
}
