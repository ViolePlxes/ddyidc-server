package zjmf

import (
	"ddyidc/internal/upstream/provider/contracts"
	"ddyidc/internal/upstream/testpolicy"
	"fmt"
	"golang.org/x/net/html"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

func catalogueClass(n *html.Node, c string) bool {
	for _, v := range strings.Fields(securityAttribute(n, "class")) {
		if v == c {
			return true
		}
	}
	return false
}
func categoryText(n *html.Node) string { return strings.Join(strings.Fields(securityText(n)), " ") }

// Parse only the supplier's actual public category navigation. Unknown product
// groups remain unknown; product names and local groups never invent hierarchy.
func parseCatalogCategories(body []byte) ([]contracts.CatalogCategory, error) {
	if len(body) > 8<<20 {
		return nil, fmt.Errorf("category response too large")
	}
	root, e := html.Parse(strings.NewReader(string(body)))
	if e != nil {
		return nil, e
	}
	out := []contracts.CatalogCategory{}
	seen := map[string]bool{}
	add := func(id, parent, name string) {
		if id == "" || name == "" || seen[id] {
			return
		}
		seen[id] = true
		out = append(out, contracts.CatalogCategory{ID: id, ParentID: parent, Name: name})
	}
	securityWalk(root, func(section *html.Node) {
		if catalogueClass(section, "product-menu-panel") {
			fid := strings.TrimPrefix(securityAttribute(section, "id"), "product-menu-panel-")
			if _, e := positiveID(fid); e != nil {
				return
			}
			parent := "first:" + fid
			add(parent, "0", securityAttribute(section, "aria-label"))
			securityWalk(section, func(a *html.Node) {
				if a.Data != "a" {
					return
				}
				u, e := url.Parse(securityAttribute(a, "href"))
				if e != nil {
					return
				}
				gid := u.Query().Get("gid")
				if _, e := positiveID(gid); e != nil {
					return
				}
				name := ""
				securityWalk(a, func(c *html.Node) {
					if c.Data == "strong" {
						name = categoryText(c)
					}
				})
				if name == "" {
					name = categoryText(a)
				}
				add(gid, parent, name)
			})
		}
		if catalogueClass(section, "sky-cart-menu-item") {
			name := ""
			securityWalk(section, func(n *html.Node) {
				if catalogueClass(n, "sky-cart-menu-title") {
					name = categoryText(n)
				}
			})
			securityWalk(section, func(a *html.Node) {
				if a.Data != "a" || !catalogueClass(a, "sky-cart-submenu-item") {
					return
				}
				u, e := url.Parse(securityAttribute(a, "href"))
				if e != nil {
					return
				}
				fid, gid := u.Query().Get("fid"), u.Query().Get("gid")
				if _, e := positiveID(fid); e != nil {
					return
				}
				if _, e := positiveID(gid); e != nil {
					return
				}
				parent := "first:" + fid
				add(parent, "0", name)
				add(gid, parent, categoryText(a))
			})
		}
	})
	if len(out) == 0 {
		return nil, fmt.Errorf("供应商未返回可识别的公开分类导航")
	}
	return out, nil
}
func (p *Provider) GetProductCategories() ([]contracts.CatalogCategory, error) {
	if e := testpolicy.CheckCatalogue(p.Supplier.URL); e != nil {
		return nil, e
	}
	base, e := p.baseURL()
	if e != nil {
		return nil, e
	}
	body, status, e := p.request(http.MethodGet, base+"/cart", nil, "", "")
	if e != nil {
		return nil, e
	}
	if status != http.StatusOK {
		return nil, fmt.Errorf("category request failed")
	}
	return parseCatalogCategories(body)
}

func positiveID(s string) (int64, error) {
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0, fmt.Errorf("invalid category id")
		}
	}
	v, e := strconv.ParseInt(s, 10, 64)
	if e != nil || v <= 0 {
		return 0, fmt.Errorf("invalid category id")
	}
	return v, nil
}
