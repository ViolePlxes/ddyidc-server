package portal

import (
	"strings"
	"testing"
)

func TestSeedContracts(t *testing.T) {
	d := Seed()
	if e := Validate(d); e != nil {
		t.Fatal(e)
	}
	for _, kind := range []string{"banner_carousel", "news_announcements", "partner_logos"} {
		found := false
		for _, m := range d.Pages[0].Modules {
			if m.Type == kind && m.Enabled {
				found = true
			}
		}
		if !found {
			t.Fatalf("required default module %s disabled", kind)
		}
	}
}
func TestRejectExecutableLinksAndPrivateRoutes(t *testing.T) {
	for _, url := range []string{"javascript:alert(1)", "data:text/html,test", "//evil.invalid", "/../private", "/safe\\path"} {
		d := Seed()
		d.Pages[0].Modules[0].Href = url
		if Validate(d) == nil {
			t.Fatalf("accepted unsafe URL %q", url)
		}
	}
	d := Seed()
	d.Pages[1].Path = "/admin/private"
	if Validate(d) == nil {
		t.Fatal("accepted private route")
	}
}
func TestPlainTextDoesNotExposeExecutableContent(t *testing.T) {
	got := plain(`<h2>服务说明</h2><script>secret()</script><iframe>private</iframe><p>公开内容</p>`)
	if got != "服务说明 公开内容" {
		t.Fatal(got)
	}
}
func TestCrawlerGroupsRetainPrivateRestrictions(t *testing.T) {
	raw := robots(Seed().Site)
	for _, group := range strings.Split(raw, "\n\n") {
		if strings.HasPrefix(group, "User-agent:") && (!strings.Contains(group, "Allow: /\n") || !strings.Contains(group, "Disallow: /admin\n")) {
			t.Fatalf("unsafe group %s", group)
		}
	}
	if !strings.Contains(raw, "OAI-SearchBot") || !strings.Contains(raw, "Claude-SearchBot") {
		t.Fatal("search crawler missing")
	}
}
func TestSitemapExcludesNoindexAndEscapesURL(t *testing.T) {
	d := Seed()
	snap := Snapshot{Document: d}
	pages := []Page{{Path: "/public", Title: "Public"}, {Path: "/private", SEO: SEO{NoIndex: true}}}
	xml := string(sitemap(snap, pages))
	if !strings.Contains(xml, "/public</loc>") || strings.Contains(xml, "/private") {
		t.Fatal(xml)
	}
}

// The footer licence block is operator-edited plain text, so it must stay
// bounded and still accept the normal ICP/许可证 lines.
func TestLicenseTextIsBounded(t *testing.T) {
	d := Seed()
	d.Site.Licenses = strings.Repeat("冀ICP备2023039940号-6\n", 200)
	if Validate(d) == nil {
		t.Fatal("accepted oversized licence text")
	}
	d.Site.Licenses = "冀ICP备2023039940号-6\n冀公网安备13080302000132号\n信息服务业务 ICP许可证: 冀B2-20240444"
	if err := Validate(d); err != nil {
		t.Fatalf("rejected a normal licence block: %v", err)
	}
}
