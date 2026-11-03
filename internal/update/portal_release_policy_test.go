package update

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPortalRouteAliasesAreOptionalAndConstrained(t *testing.T) {
	tooMany := map[string]string{}
	for i := 0; i < 101; i++ {
		tooMany[fmt.Sprintf("/page%d.html", i)] = "/company"
	}
	many, _ := json.Marshal(tooMany)
	for _, test := range []struct {
		name, body string
		valid      bool
	}{
		{"valid", `{"/about.html":"/company","/help.html":"/help/getting-started"}`, true},
		{"empty", `{}`, true},
		{"external", `{"/about.html":"https://evil.example"}`, false},
		{"protocol-relative", `{"/about.html":"//evil.example"}`, false},
		{"traversal", `{"/about.html":"/../private"}`, false},
		{"source-traversal", `{"/../about.html":"/company"}`, false},
		{"relative-source", `{"about.html":"/company"}`, false},
		{"query", `{"/about.html":"/company?token=private"}`, false},
		{"null", `null`, false},
		{"array", `[]`, false},
		{"number", `{"/about.html":123}`, false},
		{"trailing", `{} {}`, false},
		{"invalid-json", `{`, false},
		{"too-many", string(many), false},
		{"oversized", `{"/about.html":"/` + strings.Repeat("a", 65536) + `"}`, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := portalReleaseFixture(t)
			if err := os.WriteFile(filepath.Join(root, "portal/routes.json"), []byte(test.body), 0644); err != nil {
				t.Fatal(err)
			}
			err := validateReleaseTree(root, true)
			if (err == nil) != test.valid {
				t.Fatalf("valid=%v err=%v", test.valid, err)
			}
		})
	}
	root := portalReleaseFixture(t)
	if err := os.Symlink("themes.json", filepath.Join(root, "portal/routes.json")); err != nil {
		t.Fatal(err)
	}
	if validateReleaseTree(root, true) == nil {
		t.Fatal("route symlink accepted")
	}
}

func portalReleaseFixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	for name, body := range map[string]string{
		"portal/themes.json":               `[{"id":"cloudflare-reference","label":"门户","version":1,"schemaVersion":1}]`,
		"portal/cloudflare-reference.json": `{"theme":"cloudflare-reference","version":1,"styles":["/assets/site.css"],"script":"/assets/portal-content-0123456789abcdef.js"}`,
		"portal/cloudflare-reference.html": "<h1>Public content</h1>", "assets/site.css": "body{}", "assets/portal-content-0123456789abcdef.js": "// public runtime",
	} {
		path := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}
func TestPortalReleaseRegistryIsNarrowAndValidatesReferences(t *testing.T) {
	if err := validateReleaseTree(portalReleaseFixture(t), true); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct{ name, body string }{
		{"portal/private.json", `{"customer":"not-public"}`},
		{"portal/themes.json", `[{"id":"cloudflare-reference","label":"门户","version":1,"schemaVersion":1,"secret":"blocked"}]`},
		{"portal/cloudflare-reference.json", `{"theme":"cloudflare-reference","version":1,"styles":["/../private.css"],"script":"/assets/portal-content-0123456789abcdef.js"}`},
		{"portal/cloudflare-reference.json", `{"theme":"cloudflare-reference","version":1,"styles":["/assets/missing.css"],"script":"/assets/portal-content-0123456789abcdef.js"}`},
	} {
		t.Run(test.name+test.body, func(t *testing.T) {
			root := portalReleaseFixture(t)
			if err := os.WriteFile(filepath.Join(root, test.name), []byte(test.body), 0644); err != nil {
				t.Fatal(err)
			}
			if validateReleaseTree(root, true) == nil {
				t.Fatal("unsafe portal artifact accepted")
			}
		})
	}
}
