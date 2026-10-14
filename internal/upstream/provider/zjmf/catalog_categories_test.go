package zjmf

import (
	"strings"
	"testing"
)

func TestCatalogCategoryNavigation(t *testing.T) {
	cases := []string{
		`<section class="product-menu-panel" id="product-menu-panel-8" aria-label="海外"><a href="/cart?fid=8&amp;gid=17"><strong>香港 &amp; CN2</strong><span>说明不是分类名</span></a></section>`,
		`<div class="sky-cart-menu-item"><div class="sky-cart-menu-title">海外</div><a class="sky-cart-submenu-item" href="/cart?fid=8&amp;gid=17">香港 &amp; CN2</a></div>`,
	}
	for _, body := range cases {
		groups, e := parseCatalogCategories([]byte(body))
		if e != nil || len(groups) != 2 {
			t.Fatal(groups, e)
		}
		if groups[0].ID != "first:8" || groups[0].ParentID != "0" || groups[1].ID != "17" || groups[1].ParentID != "first:8" || groups[1].Name != "香港 & CN2" {
			t.Fatal(groups)
		}
	}
	if _, e := parseCatalogCategories([]byte(`<a href="/cart?fid=8&gid=17">无层级的广告链接</a>`)); e == nil {
		t.Fatal("invented navigation from advertisement")
	}
	if _, e := parseCatalogCategories([]byte(strings.Repeat("x", (8<<20)+1))); e == nil {
		t.Fatal("oversize accepted")
	}
}
