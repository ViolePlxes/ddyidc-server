package certification

import (
	"bytes"

	crypt "ddyidc/pkg/crypto"
	"image"
	"image/png"
	"mime/multipart"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func validInput() Input {
	return Input{Name: "认证验收", Phone: "+8613800000000", DocumentType: 1, DocumentNumber: "110101199001010011", Front: "file", Back: "file"}
}
func TestValidationAndMasking(t *testing.T) {
	in := validInput()
	if err := Validate(&in, 1); err != nil {
		t.Fatal(err)
	}
	for name, change := range map[string]func(*Input){"name": func(i *Input) { i.Name = "<script>" }, "phone": func(i *Input) { i.Phone = "abc" }, "document": func(i *Input) { i.DocumentNumber = "123" }, "permit": func(i *Input) { i.DocumentType = 2; i.DocumentNumber = "!!" }, "image": func(i *Input) { i.Front = "" }} {
		t.Run(name, func(t *testing.T) {
			in := validInput()
			change(&in)
			if Validate(&in, 1) == nil {
				t.Fatal("invalid accepted")
			}
		})
	}
	s := &Service{key: []byte("0123456789abcdef0123456789abcdef"), root: t.TempDir()}
	encrypted, _ := crypt.Encrypt(in.DocumentNumber, s.key)
	row, err := s.view(Record{Name: in.Name, Phone: in.Phone, IDCardNo: encrypted, IDCardFront: "private"}, false)
	if err != nil || row.Name == in.Name || row.IDCardNo == in.DocumentNumber || row.IDCardFront != "" {
		t.Fatal("masking failed")
	}
	if s.hash(in.DocumentNumber) == in.DocumentNumber {
		t.Fatal("hash plaintext")
	}
}
func TestPrivateUploads(t *testing.T) {
	s := &Service{key: []byte("0123456789abcdef0123456789abcdef"), root: t.TempDir()}
	var img bytes.Buffer
	_ = png.Encode(&img, image.NewRGBA(image.Rect(0, 0, 2, 2)))
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, _ := writer.CreateFormFile("file", "identity.png")
	_, _ = part.Write(img.Bytes())
	_ = writer.Close()
	r := httptest.NewRequest("POST", "/uploads", &body)
	r.Header.Set("Content-Type", writer.FormDataContentType())
	if err := r.ParseMultipartForm(5 << 20); err != nil {
		t.Fatal(err)
	}
	defer r.MultipartForm.RemoveAll()
	id, err := s.saveUpload(r, 1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.readUpload(id, 2, false); err == nil {
		t.Fatal("cross-owner access")
	}
	if _, err = s.readUpload("../secret", 1, false); err == nil {
		t.Fatal("traversal")
	}
	raw, _ := os.ReadFile(s.root + "/" + id)
	if bytes.Contains(raw, img.Bytes()) {
		t.Fatal("image stored as plaintext")
	}
	if _, err = s.readUpload(id, 1, false); err != nil {
		t.Fatal(err)
	}
}
func TestOrigin(t *testing.T) {
	r := httptest.NewRequest("POST", "http://example.com/api/v1/certification/person", strings.NewReader("{}"))
	if originOK(r) {
		t.Fatal("missing origin allowed")
	}
	r.Header.Set("Origin", "https://evil.invalid")
	if originOK(r) {
		t.Fatal("cross origin allowed")
	}
	r.Header.Set("Origin", "http://example.com")
	if !originOK(r) {
		t.Fatal("same origin denied")
	}

}
