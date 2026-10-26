package portal

import (
 "context"
 "testing"
)

func TestBootstrapNeverPublishesUnconfirmedContent(t *testing.T) {
 // No database/asset access is needed for startup or public-read bootstrap.
 // Any attempt to recreate the old automatic Save path would fail this test.
 service := New(nil,t.TempDir())
 if err:=service.Bootstrap(context.Background());err!=nil {t.Fatal(err)}
}

func TestContentCategoriesKeepLegacyDocumentsCompatible(t *testing.T) {
	d := Seed()
	if err := Validate(d); err != nil {
		t.Fatal(err)
	}
	d.Categories = []Category{{ID: "news", Name: "公告", Kind: "news"}, {ID: "help", Name: "帮助", Kind: "help"}}
	d.Pages = append(d.Pages, Page{ID: "help-article", Path: "/help/start", Title: "帮助", Kind: "article", ContentType: "help", CategoryID: "help", Enabled: true})
	if err := Validate(d); err != nil {
		t.Fatal(err)
	}
	d.Pages[len(d.Pages)-1].CategoryID = "news"
	if Validate(d) == nil {
		t.Fatal("accepted category with wrong content type")
	}
	d.Pages[len(d.Pages)-1].CategoryID = "help"
	d.Categories = d.Categories[:1]
	if Validate(d) == nil {
		t.Fatal("accepted removal of category still referenced by article")
	}
	d.Pages[len(d.Pages)-1].Enabled = false
	if Validate(d) == nil {
		t.Fatal("disabled article reference was ignored")
	}
}

func TestContentCategoryValidation(t *testing.T) {
	for _, categories := range [][]Category{
		{{ID: "x", Name: "", Kind: "news"}},
		{{ID: "x", Name: "公告", Kind: "invalid"}},
		{{ID: "x", Name: "公告", Kind: "news"}, {ID: "x", Name: "帮助", Kind: "help"}},
		{{ID: "x", Name: " News ", Kind: "news"}, {ID: "y", Name: "news", Kind: "news"}},
	} {
		d := Seed()
		d.Categories = categories
		if Validate(d) == nil {
			t.Fatalf("accepted invalid categories: %+v", categories)
		}
	}
}
