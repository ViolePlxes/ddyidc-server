package update

import (
 "os"
 "path/filepath"
 "testing"
)

func portalReleaseFixture(t *testing.T) string {
 t.Helper();root:=t.TempDir()
 for name,body:=range map[string]string{
 "portal/themes.json":`[{"id":"cloudflare-reference","label":"门户","version":1,"schemaVersion":1}]`,
 "portal/cloudflare-reference.json":`{"theme":"cloudflare-reference","version":1,"styles":["/assets/site.css"],"script":"/assets/portal-content-0123456789abcdef.js"}`,
 "portal/cloudflare-reference.html":"<h1>Public content</h1>","assets/site.css":"body{}","assets/portal-content-0123456789abcdef.js":"// public runtime",
 } {path:=filepath.Join(root,name);if err:=os.MkdirAll(filepath.Dir(path),0755);err!=nil{t.Fatal(err)};if err:=os.WriteFile(path,[]byte(body),0644);err!=nil{t.Fatal(err)}}
 return root
}
func TestPortalReleaseRegistryIsNarrowAndValidatesReferences(t *testing.T) {
 if err:=validateReleaseTree(portalReleaseFixture(t),true);err!=nil{t.Fatal(err)}
 for _,test:=range []struct{name,body string}{
 {"portal/private.json",`{"customer":"not-public"}`},
 {"portal/themes.json",`[{"id":"cloudflare-reference","label":"门户","version":1,"schemaVersion":1,"secret":"blocked"}]`},
 {"portal/cloudflare-reference.json",`{"theme":"cloudflare-reference","version":1,"styles":["/../private.css"],"script":"/assets/portal-content-0123456789abcdef.js"}`},
 {"portal/cloudflare-reference.json",`{"theme":"cloudflare-reference","version":1,"styles":["/assets/missing.css"],"script":"/assets/portal-content-0123456789abcdef.js"}`},
 } {t.Run(test.name+test.body,func(t *testing.T){root:=portalReleaseFixture(t);if err:=os.WriteFile(filepath.Join(root,test.name),[]byte(test.body),0644);err!=nil{t.Fatal(err)};if validateReleaseTree(root,true)==nil{t.Fatal("unsafe portal artifact accepted")}})}
}
