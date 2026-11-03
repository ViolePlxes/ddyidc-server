// Package portal owns public content and versioned static publication.
// It has no dependency on customer sessions or purchase handlers.
package portal

import (
	"ddyidc/internal/shared/richtext"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"
)

type SEO struct {
	Title       string `json:"title"`
	Keywords    string `json:"keywords"`
	Description string `json:"description"`
	Image       string `json:"image"`
	NoIndex     bool   `json:"noIndex"`
}
type Link struct {
	Enabled     *bool  `json:"enabled,omitempty"`
	Description string `json:"description,omitempty"`
	Label       string `json:"label"`
	Href        string `json:"href"`
	Children    []Link `json:"children"`
}
type Item struct {
	ID       string `json:"id"`
	Title    string `json:"title"`
	Text     string `json:"text"`
	Image    string `json:"image"`
	Alt      string `json:"alt"`
	Href     string `json:"href"`
	Label    string `json:"label"`
	Category string `json:"category"`
}
type Module struct {
	RichContent json.RawMessage `json:"richContent,omitempty"`
	Limit       int             `json:"limit"`
	ProductIDs  []string        `json:"productIds"`
	Interval    int             `json:"interval"`
	AutoPlay    bool            `json:"autoPlay"`
	ID          string          `json:"id"`
	Type        string          `json:"type"`
	Enabled     bool            `json:"enabled"`
	Sort        int             `json:"sort"`
	Eyebrow     string          `json:"eyebrow"`
	Title       string          `json:"title"`
	Text        string          `json:"text"`
	Image       string          `json:"image"`
	Alt         string          `json:"alt"`
	Href        string          `json:"href"`
	Label       string          `json:"label"`
	Items       []Item          `json:"items"`
}
type Page struct {
	Layout      string   `json:"layout,omitempty"`
	ContentType string   `json:"contentType,omitempty"`
	CategoryID  string   `json:"categoryId,omitempty"`
	ID          string   `json:"id"`
	Path        string   `json:"path"`
	Title       string   `json:"title"`
	Kind        string   `json:"kind"`
	Enabled     bool     `json:"enabled"`
	SEO         SEO      `json:"seo"`
	Modules     []Module `json:"modules"`
	UpdatedAt   string   `json:"updatedAt"`
}
type Site struct {
	Brand         string `json:"brand"`
	Origin        string `json:"origin"`
	ConsoleOrigin string `json:"consoleOrigin"`
	Logo          string `json:"logo"`
	Copyright     string `json:"copyright"`
	Filing        string `json:"filing"`
	FilingURL     string `json:"filingUrl"`
	Theme         string `json:"theme"`
	Enabled       bool   `json:"enabled"`
	HeaderEnabled bool   `json:"headerEnabled"`
	FooterEnabled bool   `json:"footerEnabled"`
	LLMS          bool   `json:"llms"`
	SEO           SEO    `json:"seo"`
	Navigation    []Link `json:"navigation"`
	Footer        []Link `json:"footer"`
}
type Document struct {
	Categories    []Category `json:"categories,omitempty"`
	SchemaVersion int        `json:"schemaVersion"`
	Revision      int64      `json:"revision"`
	Site          Site       `json:"site"`
	Pages         []Page     `json:"pages"`
}
type Category struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Kind string `json:"kind"`
}
type Product struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Summary     string `json:"summary"`
	Description string `json:"description"`
	Group       string `json:"group"`
	Price       string `json:"price"`
	Cycle       string `json:"cycle"`
}
type Snapshot struct {
	Document    Document  `json:"document"`
	Products    []Product `json:"products"`
	GeneratedAt string    `json:"generatedAt"`
	Fingerprint string    `json:"fingerprint"`
}
type Definition struct {
	Type  string `json:"type"`
	Label string `json:"label"`
}

var Definitions = []Definition{{"faq", "常见问题"}, {"steps", "办理流程"}, {"statistics", "信息指标"}, {"image_gallery", "图片与证书"}, {"contact_cards", "联系卡片"}, {"job_list", "招聘岗位"}, {"announcement_bar", "活动提示条"}, {"hero", "首屏主视觉"}, {"network_capabilities", "网络与能力"}, {"customer_logos", "客户标识"}, {"testimonial", "客户评价"}, {"advantage_comparison", "特色优势"}, {"product_solutions", "产品与方案"}, {"feature_sections", "图文能力"}, {"final_cta", "行动区"}, {"banner_carousel", "轮播 Banner"}, {"news_announcements", "新闻公告"}, {"partner_logos", "合作伙伴"}}
var identifier = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,64}$`)
var productPagePath = regexp.MustCompile(`^/products/[0-9]{1,20}$`)
var pagePath = regexp.MustCompile(`^/(?:[a-zA-Z0-9_-]+/)*[a-zA-Z0-9_-]*$`)

func safeURL(raw string, image bool) bool {
	if raw == "" {
		return true
	}
	if strings.ContainsAny(raw, "\\\r\n\t") || strings.HasPrefix(raw, "//") {
		return false
	}
	if strings.HasPrefix(raw, "/") {
		return !strings.Contains(raw, "..")
	}
	u, e := url.Parse(raw)
	if e != nil || u.User != nil {
		return false
	}
	return u.Scheme == "https" && u.Host != "" && (!image || u.Fragment == "")
}
func origin(raw string) bool {
	u, e := url.Parse(raw)
	return e == nil && u.Scheme == "https" && u.Host != "" && u.User == nil && u.Path == "" && u.RawQuery == "" && u.Fragment == ""
}
func Validate(d Document) error {
	if len(d.Categories) > 100 {
		return errors.New("内容分类不能超过 100 个")
	}
	categories := map[string]Category{}
	names := map[string]bool{}
	for _, c := range d.Categories {
		name := strings.TrimSpace(c.Name)
		key := c.Kind + ":" + strings.ToLower(name)
		if !identifier.MatchString(c.ID) || name == "" || len(name) > 200 || (c.Kind != "news" && c.Kind != "help") || names[key] {
			return errors.New("分类名称、类型或 ID 无效或重复")
		}
		if _, exists := categories[c.ID]; exists {
			return errors.New("分类 ID 重复")
		}
		categories[c.ID] = c
		names[key] = true
	}
	if d.SchemaVersion != 1 || !identifier.MatchString(d.Site.Theme) || !origin(d.Site.Origin) || !origin(d.Site.ConsoleOrigin) {
		return errors.New("主题、版本或 HTTPS 站点地址无效")
	}
	if strings.TrimSpace(d.Site.Brand) == "" || len(d.Pages) > 500 {
		return errors.New("请填写品牌名称；页面数量不能超过 500")
	}
	if !safeURL(d.Site.Logo, true) || !safeURL(d.Site.FilingURL, false) {
		return errors.New("站点图片或备案链接无效")
	}
	var checkSEO = func(s SEO) bool {
		return len(s.Title) <= 512 && len(s.Description) <= 2048 && len(s.Keywords) <= 1024 && safeURL(s.Image, true)
	}
	if !checkSEO(d.Site.SEO) {
		return errors.New("SEO 长度或分享图片地址无效")
	}
	var links func([]Link, int) bool
	links = func(list []Link, depth int) bool {
		if len(list) == 0 {
			return true
		}
		if depth > 2 || len(list) > 100 {
			return false
		}
		for _, l := range list {
			if l.Label == "" || len(l.Description) > 600 || !safeURL(l.Href, false) || !links(l.Children, depth+1) {
				return false
			}
		}
		return true
	}
	if !links(d.Site.Navigation, 0) || !links(d.Site.Footer, 0) {
		return errors.New("菜单名称或链接无效，最多支持三级")
	}
	ids, paths := map[string]bool{}, map[string]bool{}
	home := false
	for _, p := range d.Pages {
		if !identifier.MatchString(p.ID) || ids[p.ID] || paths[p.Path] || !pagePath.MatchString(p.Path) || strings.HasSuffix(p.Path, "/") && p.Path != "/" {
			return errors.New("页面 ID 或路径无效或重复")
		}
		if strings.HasPrefix(p.Path, "/dd-admin-") {
			return errors.New("公开页面不能使用保留路径")
		}
		ids[p.ID] = true
		paths[p.Path] = true
		for _, prefix := range []string{"/api", "/admin", "/console", "/sales", "/wallet", "/user", "/login", "/register", "/reset-password", "/bind-mobile", "/auth", "/cart", "/order", "/assets", "/groupbuy", "/marketplace"} {
			if p.Path == prefix || strings.HasPrefix(p.Path, prefix+"/") {
				return errors.New("公开页面不能使用保留路径")
			}
		}
		if p.Path == "/" {
			home = true
			if !p.Enabled {
				return errors.New("首页不可停用，请使用站点总开关")
			}
		}
		if p.Layout != "" {
			allowed := map[string]bool{"overview": true, "contact": true, "certificates": true, "service": true, "partners": true, "careers": true, "catalog": true, "solutions": true, "campaign": true, "document": true}
			if !allowed[p.Layout] {
				return errors.New("页面布局无效")
			}
		}
		if p.Path == "/products" && p.Kind != "products" {
			return errors.New("产品目录路径需要使用受控产品目录类型")
		}
		if strings.HasPrefix(p.Path, "/products/") && (p.Kind != "product" || !productPagePath.MatchString(p.Path)) {
			return errors.New("商品介绍路径或类型无效")
		}
		if p.Kind != "page" && p.Kind != "article" && p.Kind != "products" && p.Kind != "product" {
			return errors.New("页面类型无效")
		}
		if p.ContentType != "" && (p.Kind != "article" || (p.ContentType != "news" && p.ContentType != "help")) {
			return errors.New("文章内容类型无效")
		}
		if p.CategoryID != "" {
			kind := p.ContentType
			if kind == "" {
				kind = "news"
			}
			c, exists := categories[p.CategoryID]
			if p.Kind != "article" || !exists || c.Kind != kind {
				return errors.New("文章分类不存在或类型不匹配；请先调整关联文章再删除分类")
			}
		}
		if (p.Kind == "product") != strings.HasPrefix(p.Path, "/products/") {
			return errors.New("产品 SEO 页面必须使用产品详情路径")
		}
		if len(p.Modules) > 100 || !checkSEO(p.SEO) {
			return errors.New("页面模块数量或 SEO 无效")
		}
		mids := map[string]bool{}
		for _, m := range p.Modules {
			if _, _, err := richtext.Validate(m.RichContent, nil); err != nil {
				return errors.New("正文富文本无效；请使用受支持的格式，图片请使用封面字段")
			}
			if m.Limit < 0 || m.Limit > 100 || m.Interval < 0 || m.Interval > 60 || len(m.ProductIDs) > 100 {
				return errors.New("产品数量或轮播间隔无效")
			}
			found := false
			for _, def := range Definitions {
				if def.Type == m.Type {
					found = true
				}
			}
			if !found || !identifier.MatchString(m.ID) || mids[m.ID] {
				return fmt.Errorf("模块类型或 ID 无效：%s", m.ID)
			}
			mids[m.ID] = true
			if !safeURL(m.Image, true) || !safeURL(m.Href, false) || len(m.Items) > 200 {
				return errors.New("模块链接、媒体或条目数量无效")
			}
			if m.Image != "" && m.Alt == "" {
				return errors.New("模块图片必须填写替代文字")
			}
			for _, i := range m.Items {
				if !safeURL(i.Image, true) || !safeURL(i.Href, false) || i.Image != "" && i.Alt == "" {
					return errors.New("条目图片必须填写替代文字，链接必须安全")
				}
			}
		}
	}
	if !home {
		return errors.New("必须保留首页")
	}
	return nil
}
func Seed() Document {
	var d Document
	if e := json.Unmarshal(seedJSON, &d); e != nil {
		panic(e)
	}
	formal, err := FormalDraft(d)
	if err != nil {
		panic(err)
	}
	return formal
}
