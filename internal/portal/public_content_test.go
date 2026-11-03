package portal

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestPublicContentExcludesDraftAndHiddenModuleBodies(t *testing.T) {
	snap := Snapshot{Document: Document{Revision: 7, Site: Site{Navigation: []Link{{Label: "private", Href: "/private"}, {Label: "company", Href: "/company", Children: []Link{{Label: "private", Href: "/private#x"}}}}}, Pages: []Page{{ID: "visible", Path: "/company", Enabled: true, Modules: []Module{{ID: "hidden", Enabled: false, Text: "confidential-module"}, {ID: "visible", Enabled: true, Text: "public-body"}}}, {ID: "private", Path: "/private", Enabled: false, Modules: []Module{{Enabled: true, Text: "confidential-page"}}}}}}
	result := publicContent(snap)
	encoded, _ := json.Marshal(result)
	if strings.Contains(string(encoded), "confidential") {
		t.Fatal("hidden content escaped projection")
	}
	if result.Revision != 7 || len(result.Pages) != 1 || len(result.Pages[0].Modules) != 1 || len(result.Site.Navigation) != 1 || len(result.Site.Navigation[0].Children) != 0 {
		t.Fatal("public projection mismatch")
	}
	if len(snap.Document.Pages[0].Modules) != 2 || len(snap.Document.Site.Navigation) != 2 {
		t.Fatal("projection mutated source snapshot")
	}
}

func TestPublicCategoriesOnlyDescribePublishedArticles(t *testing.T) {
	snap := Snapshot{Document: Document{Categories: []Category{{ID: "live", Name: "已发布分类", Kind: "news"}, {ID: "private", Name: "私有分类", Kind: "news"}}, Pages: []Page{{ID: "live", Path: "/news/live", Kind: "article", Enabled: true, CategoryID: "live"}, {ID: "hidden", Path: "/news/hidden", Kind: "article", Enabled: false, CategoryID: "private"}}}}
	out := publicContent(snap)
	if len(out.Categories) != 1 || out.Categories[0].ID != "live" {
		t.Fatal("unpublished category escaped projection")
	}
}
func TestPublicLayoutAndModuleValidation(t *testing.T) {
	for _, layout := range []string{"overview", "contact", "certificates", "service", "partners", "careers", "catalog", "solutions", "campaign", "document"} {
		d := Seed()
		d.Pages[0].Layout = layout
		if err := Validate(d); err != nil {
			t.Fatalf("%s: %v", layout, err)
		}
	}
	d := Seed()
	d.Pages[0].Layout = "../../private"
	if Validate(d) == nil {
		t.Fatal("unsafe template accepted")
	}
	for _, kind := range []string{"faq", "steps", "statistics", "image_gallery", "contact_cards", "job_list"} {
		d := Seed()
		d.Pages[0].Modules = []Module{{ID: "test", Type: kind, Enabled: true, Title: "测试", Items: []Item{{ID: "row", Title: "条目", Text: "说明"}}}}
		if err := Validate(d); err != nil {
			t.Fatalf("%s: %v", kind, err)
		}
	}
}

func TestHiddenProductIntroductionDoesNotChangeCatalog(t *testing.T) {
 d:=Seed();d.Pages=append(d.Pages,Page{ID:"intro",Path:"/products/123",Title:"自定义介绍",Kind:"product",Enabled:false,Modules:[]Module{{ID:"intro-body",Type:"feature_sections",Text:"hidden-intro",Enabled:true}}})
 if err:=Validate(d);err!=nil{t.Fatal(err)}
 snap:=Snapshot{Document:d,Products:[]Product{{ID:"123",Name:"真实商品"}}}
 for _,p:=range publicContent(snap).Pages{if p.Path=="/products/123"{t.Fatal("hidden introduction published")}}
 if len(snap.Products)!=1{t.Fatal("CMS changed catalog")}
 d.Pages[len(d.Pages)-1].Enabled=true;snap.Document=d
 var found bool;for _,p:=range publicContent(snap).Pages{if p.Path=="/products/123"{found=true;if p.Title!="自定义介绍"{t.Fatal("title override ignored")}}};if !found{t.Fatal("published introduction missing")}
}

func TestDisabledNavigationLinksStayPrivate(t *testing.T){
 hidden:=false;snap:=Snapshot{Document:Document{Site:Site{Navigation:[]Link{{Label:"私有菜单",Href:"/private-menu",Enabled:&hidden},{Label:"公开菜单",Href:"/company",Children:[]Link{{Label:"私有子菜单",Href:"/private-child",Enabled:&hidden}}}}}}}
 encoded,_:=json.Marshal(publicContent(snap));if strings.Contains(string(encoded),"私有"){t.Fatal("disabled navigation leaked")}
 if len(snap.Document.Site.Navigation)!=2{t.Fatal("projection mutated draft navigation")}
}
