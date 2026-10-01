package portal

import (
	"bytes"
	"html/template"
	"os"
	"testing"
)

func TestInstalledThemeStaticDocument(t *testing.T) {
	dist := os.Getenv("DDYIDC_PORTAL_ASSETS")
	if dist == "" {
		t.Skip("set DDYIDC_PORTAL_ASSETS to the VM-built frontend dist")
	}
	s := Service{dist: dist}
	assets, body, e := s.theme("cloudflare-reference")
	if e != nil {
		t.Fatal(e)
	}
	tmpl, e := template.New("portal").Funcs(templateFunctions()).Parse(string(body))
	if e != nil {
		t.Fatal(e)
	}
	d := Seed()
	view := View{Site: d.Site, Page: d.Pages[0], SEO: d.Site.SEO, Assets: assets, Canonical: d.Site.Origin + "/", Revision: 1, Products: []Product{{ID: "123", Name: "真实测试目录", Group: "测试分组", Price: "10.00"}}}
	var b bytes.Buffer
	if e = tmpl.Execute(&b, view); e != nil {
		t.Fatal(e)
	}
	if bytes.Contains(b.Bytes(), []byte("PORTAL_DIRECTIVE")) || !bytes.Contains(b.Bytes(), []byte("<h1")) || !bytes.Contains(b.Bytes(), []byte("真实测试目录")) {
		t.Fatal("static body or template compilation missing")
	}
}
