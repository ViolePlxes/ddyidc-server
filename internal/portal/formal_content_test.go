package portal

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestFormalDraftPreservesCustomContentAndHomepage(t *testing.T) {
	d := Seed()
	for i := range d.Pages {
		if d.Pages[i].Path == "/company" {
			d.Pages[i].Title = "已核实企业介绍"
			d.Pages[i].Modules = []Module{{ID: "custom-company", Type: "feature_sections", Enabled: true, Text: "管理员自定义内容"}}
		}
	}
	home := d.Pages[0]
	d.Pages = append(d.Pages, Page{ID: "lab-news-local-1", Path: "/news/local-1", Title: "本地演示公告", Kind: "article", Enabled: true, Modules: []Module{{ID: "lab-body", Type: "feature_sections", Text: "本地演示"}}})
	before, _ := json.Marshal(d)
	got, err := FormalDraft(d)
	if err != nil {
		t.Fatal(err)
	}
	after, _ := json.Marshal(d)
	if string(before) != string(after) {
		t.Fatal("caller draft mutated")
	}
	if !reflect.DeepEqual(home, got.Pages[0]) {
		t.Fatal("approved homepage changed")
	}
	for _, p := range got.Pages {
		if p.Path == "/news/local-1" {
			t.Fatal("synthetic announcement retained")
		}
		if p.Path == "/company" && (p.Title != "已核实企业介绍" || p.Modules[0].Text != "管理员自定义内容") {
			t.Fatal("custom company content overwritten")
		}
	}
	again, err := FormalDraft(got)
	if err != nil || !reflect.DeepEqual(got, again) {
		t.Fatal("formal content initialization is not idempotent", err)
	}
	if got.Revision != d.Revision {
		t.Fatal("preparation published or incremented revision")
	}
}

func TestFormalSeedHasNoInventedAnnouncementsOrCredentials(t *testing.T) {
	d := Seed()
	for _, p := range d.Pages {
		if strings.HasPrefix(p.Path, "/news/") {
			t.Fatal("release ships invented announcements")
		}
		for _, path := range []string{"/honors", "/licenses", "/company-environment", "/recruit", "/sponsors", "/aup"} {
			if p.Path == path && p.Enabled {
				t.Fatal("unverified materials published", path)
			}
		}
		text, _ := json.Marshal(p)
		for _, marker := range []string{"本地演示", "隔离闭环验证", "仅供本地布局", "本段仅用于验证"} {
			if strings.Contains(string(text), marker) {
				t.Fatal("demo marker in release content")
			}
		}
	}
	if err := Validate(d); err != nil {
		t.Fatal(err)
	}
}

func TestStoredDocumentNeverInheritsOmittedFieldsFromSeed(t *testing.T) {
	d := Seed()
	d.Pages = []Page{{ID: "terms", Path: "/terms/service", Title: "服务条款", Kind: "page", Enabled: true}, {ID: "guide", Path: "/help/guide", Title: "帮助", Kind: "article", ContentType: "help"}}
	raw, _ := json.Marshal(d)
	got, err := decodeStoredDocument(string(raw))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, d) || got.Pages[0].ContentType != "" {
		t.Fatal("reordered stored page inherited fields from seed")
	}
	// Deliberately omit optional fields at every seed position, not only the first.
	for i := range d.Pages {
		d.Pages[i].ContentType = ""
	}
	raw, _ = json.Marshal(d)
	got, err = decodeStoredDocument(string(raw))
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range got.Pages {
		if p.ContentType != "" {
			t.Fatal("omitted contentType retained")
		}
	}
}
