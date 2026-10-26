package portal

import (
	"bytes"
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"html/template"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	productrepo "ddyidc/internal/product/repository"
	"ddyidc/internal/shared/richtext"
	"github.com/google/uuid"
	"golang.org/x/net/html"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

//go:embed seed.json
var seedJSON []byte

type siteRow struct {
	ID          int64
	Revision    int64
	Draft       string
	Published   string
	Fingerprint string
	Generation  string
	UpdatedAt   time.Time
}

func (siteRow) TableName() string { return "system.portal_sites" }

type revisionRow struct {
	ID         int64     `json:"id,string"`
	Revision   int64     `json:"revision"`
	Document   string    `json:"-"`
	ActorID    int64     `json:"actorId,string"`
	OperatorIP string    `json:"operatorIp"`
	Action     string    `json:"action"`
	CreatedAt  time.Time `json:"createdAt"`
}

func (revisionRow) TableName() string { return "system.portal_revisions" }

type mediaRow struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Path      string    `json:"path"`
	Mime      string    `json:"mime"`
	ActorID   int64     `json:"-"`
	CreatedAt time.Time `json:"createdAt"`
}

func (mediaRow) TableName() string { return "system.portal_media" }

type Assets struct {
	Theme   string   `json:"theme"`
	Version int      `json:"version"`
	Styles  []string `json:"styles"`
	Script  string   `json:"script"`
}
type ThemeDefinition struct {
	ID            string `json:"id"`
	Label         string `json:"label"`
	Version       int    `json:"version"`
	SchemaVersion int    `json:"schemaVersion"`
}

func (s *Service) Themes() ([]ThemeDefinition, error) {
	var rows []ThemeDefinition
	b, e := os.ReadFile(filepath.Join(s.dist, "portal/themes.json"))
	if e != nil {
		return nil, e
	}
	if e = json.Unmarshal(b, &rows); e != nil {
		return nil, e
	}
	for _, r := range rows {
		if !identifier.MatchString(r.ID) || r.SchemaVersion != 1 {
			return nil, errors.New("主题目录契约无效")
		}
	}
	return rows, nil
}

type View struct {
	Site        Site
	Page        Page
	Revision    int64
	SEO         SEO
	Canonical   string
	Products    []Product
	Articles    []Page
	Assets      Assets
	GeneratedAt string
	Structured  template.JS
}
type Service struct {
	db         *gorm.DB
	root, dist string
	mu         sync.Mutex
}

func New(db *gorm.DB, root string) *Service {
	dist := os.Getenv("DDYIDC_PORTAL_ASSETS")
	if dist == "" {
		dist = filepath.Join(root, "update/workspaces/frontend/dist")
	}
	return &Service{db: db, root: filepath.Join(root, "storage/portal"), dist: dist}
}
func (s *Service) Prepare() error {
	for _, p := range []string{s.root, filepath.Join(s.root, "generations"), filepath.Join(s.root, "media")} {
		if e := os.MkdirAll(p, 0750); e != nil {
			return e
		}
	}
	return nil
}
func (s *Service) row(ctx context.Context) (siteRow, error) {
	var r siteRow
	e := s.db.WithContext(ctx).First(&r, 1).Error
	return r, e
}
func (s *Service) Draft(ctx context.Context) (Document, error) {
	r, e := s.row(ctx)
	if e != nil {
		return Document{}, e
	}
	d := Seed()
	if r.Draft != "" {
		e = json.Unmarshal([]byte(r.Draft), &d)
	}
	d.Revision = r.Revision
	return d, e
}
func plain(raw string) string {
	n, e := html.Parse(strings.NewReader(raw))
	if e != nil {
		return ""
	}
	var b strings.Builder
	var visit func(*html.Node)
	visit = func(n *html.Node) {
		if n.Type == html.ElementNode {
			switch n.Data {
			case "script", "style", "iframe", "object", "template", "noscript":
				return
			}
		}
		if n.Type == html.TextNode {
			b.WriteString(n.Data)
			b.WriteByte(' ')
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			visit(c)
		}
	}
	visit(n)
	return strings.Join(strings.Fields(b.String()), " ")
}
func (s *Service) products(ctx context.Context) ([]Product, error) {
	repo := productrepo.NewRepository(s.db)
	out := []Product{}
	for page := 1; ; page++ {
		rows, total, e := repo.ListPublic(ctx, page, 100)
		if e != nil {
			return nil, e
		}
		for _, r := range rows {
			cycle := map[string]string{"monthly": "月", "quarterly": "季度", "semiannually": "半年", "annually": "年", "biennially": "两年", "triennially": "三年", "onetime": "次", "hourly": "小时", "daily": "天"}[r.Cycle]
			if cycle == "" {
				cycle = "计费周期"
			}
			out = append(out, Product{r.ID, r.Name, plain(r.Summary), plain(r.Description), r.GroupName, fmt.Sprintf("%.2f", r.Price), cycle})
		}
		if len(out) >= int(total) {
			break
		}
		if len(rows) == 0 {
			return nil, errors.New("产品目录发生变更，请重试")
		}
	}
	return out, nil
}
func (s *Service) theme(themeID string) (Assets, []byte, error) {
	var a Assets
	if !identifier.MatchString(themeID) {
		return a, nil, errors.New("主题标识无效")
	}
	b, e := os.ReadFile(filepath.Join(s.dist, "portal", themeID+".json"))
	if e != nil {
		return a, nil, e
	}
	if e = json.Unmarshal(b, &a); e != nil {
		return a, nil, e
	}
	if a.Theme != themeID || a.Version != 1 || len(a.Styles) == 0 {
		return a, nil, errors.New("门户主题资源契约无效")
	}
	for _, p := range append(append([]string{}, a.Styles...), a.Script) {
		if !strings.HasPrefix(p, "/assets/") || strings.Contains(p, "..") || strings.ContainsAny(p, "\\?#") {
			return a, nil, errors.New("主题资源路径无效")
		}
		if _, e = os.Stat(filepath.Join(s.dist, filepath.FromSlash(p[1:]))); e != nil {
			return a, nil, e
		}
	}
	body, e := os.ReadFile(filepath.Join(s.dist, "portal", themeID+".html"))
	return a, body, e
}
func inherit(page, site SEO) SEO {
	if page.Title == "" {
		page.Title = site.Title
	}
	if page.Keywords == "" {
		page.Keywords = site.Keywords
	}
	if page.Description == "" {
		page.Description = site.Description
	}
	if page.Image == "" {
		page.Image = site.Image
	}
	page.NoIndex = page.NoIndex || site.NoIndex
	return page
}
func pagesFor(snap Snapshot) []Page {
	out := []Page{}
	for _, p := range snap.Document.Pages {
		if p.Enabled && !strings.HasPrefix(p.Path, "/products/") {
			out = append(out, p)
		}
	}
	for _, r := range snap.Products {
		path := "/products/" + r.ID
		p := Page{ID: "product-" + r.ID, Path: path, Title: r.Name, Kind: "product", Enabled: true, SEO: SEO{Title: r.Name + " | " + snap.Document.Site.Brand, Description: r.Summary}}
		allowed := true
		for _, override := range snap.Document.Pages {
			if override.Path == path {
				allowed = override.Enabled
				p.SEO = inherit(override.SEO, p.SEO)
				p.Modules = override.Modules
			}
		}
		if !allowed {
			continue
		}
		found := false
		for _, existing := range out {
			if existing.Path == path {
				found = true
			}
		}
		if !found {
			out = append(out, p)
		}
	}
	return out
}

func templateFunctions() template.FuncMap {
	return template.FuncMap{
		"richBody": renderRichBody,
		"absolute": func(base, path string) string {
			if strings.HasPrefix(path, "/") {
				return base + path
			}
			return path
		},
		"moduleView": func(root View, m Module) any {
			return struct {
				Root   View
				Module Module
			}{root, m}
		},
		"hasHero": func(p Page) bool {
			for _, m := range p.Modules {
				if m.Enabled && m.Type == "hero" {
					return true
				}
			}
			return false
		},
		"primaryHero": func(root View, m Module) bool {
			for _, candidate := range root.Page.Modules {
				if candidate.Enabled && candidate.Type == "hero" {
					return candidate.ID == m.ID
				}
			}
			return false
		},
		"moduleProducts": func(root View, m Module) View {
			selected := []Product{}
			if len(m.ProductIDs) > 0 {
				for _, id := range m.ProductIDs {
					for _, p := range root.Products {
						if p.ID == id {
							selected = append(selected, p)
						}
					}
				}
			} else {
				selected = root.Products
			}
			limit := m.Limit
			if limit == 0 {
				limit = 6
			}
			if len(selected) > limit {
				selected = selected[:limit]
			}
			root.Products = selected
			return root
		},
	}
}
func (s *Service) generate(ctx context.Context, d Document) (Snapshot, string, error) {
	if e := Validate(d); e != nil {
		return Snapshot{}, "", e
	}
	products, e := s.products(ctx)
	if e != nil {
		return Snapshot{}, "", e
	}
	assets, body, e := s.theme(d.Site.Theme)
	if e != nil {
		return Snapshot{}, "", e
	}
	snap := Snapshot{Document: d, Products: products, GeneratedAt: time.Now().UTC().Format(time.RFC3339)}
	fingerprintBody, _ := json.Marshal(struct {
		D Document
		P []Product
		A Assets
	}{d, products, assets})
	sum := sha256.Sum256(append(fingerprintBody, body...))
	snap.Fingerprint = hex.EncodeToString(sum[:])
	t, e := template.New("portal").Funcs(templateFunctions()).Parse(string(body))
	if e != nil {
		return snap, "", e
	}
	generation := uuid.NewString()
	dir := filepath.Join(s.root, "generations", generation)
	if e = os.MkdirAll(dir, 0750); e != nil {
		return snap, "", e
	}
	complete := false
	defer func() {
		if !complete {
			_ = os.RemoveAll(dir)
		}
	}()
	pages := pagesFor(snap)
	articles := []Page{}
	for _, p := range pages {
		if p.Kind == "article" {
			articles = append(articles, p)
		}
	}
	for _, p := range pages {
		sort.SliceStable(p.Modules, func(i, j int) bool { return p.Modules[i].Sort < p.Modules[j].Sort })
		selected := products
		if p.Kind == "product" {
			selected = nil
			for _, r := range products {
				if p.Path == "/products/"+r.ID {
					selected = append(selected, r)
				}
			}
		}
		structured, _ := json.Marshal(map[string]any{"@context": "https://schema.org", "@type": "WebPage", "name": p.Title, "url": d.Site.Origin + p.Path, "isPartOf": map[string]any{"@type": "WebSite", "name": d.Site.Brand, "url": d.Site.Origin}, "publisher": map[string]any{"@type": "Organization", "name": d.Site.Brand, "url": d.Site.Origin}})
		if p.Kind == "article" {
			structured, _ = json.Marshal(map[string]any{"@context": "https://schema.org", "@type": "Article", "headline": p.Title, "url": d.Site.Origin + p.Path, "dateModified": p.UpdatedAt, "publisher": map[string]any{"@type": "Organization", "name": d.Site.Brand}})
		}
		view := View{d.Site, p, d.Revision, inherit(p.SEO, d.Site.SEO), d.Site.Origin + p.Path, selected, articles, assets, snap.GeneratedAt, template.JS(structured)}
		var b bytes.Buffer
		if e = t.Execute(&b, view); e != nil {
			return snap, "", e
		}
		if e = writePage(dir, p.Path, b.Bytes()); e != nil {
			return snap, "", e
		}
	}
	if e = writePage(dir, "/sitemap.xml", sitemap(snap, pages)); e != nil {
		return snap, "", e
	}
	if e = writePage(dir, "/robots.txt", []byte(robots(d.Site))); e != nil {
		return snap, "", e
	}
	if d.Site.LLMS {
		var b strings.Builder
		fmt.Fprintf(&b, "# %s\n\n%s\n\n", d.Site.Brand, d.Site.SEO.Description)
		for _, p := range pages {
			if !inherit(p.SEO, d.Site.SEO).NoIndex {
				fmt.Fprintf(&b, "- [%s](%s%s)\n", p.Title, d.Site.Origin, p.Path)
			}
		}
		if e = writePage(dir, "/llms.txt", []byte(b.String())); e != nil {
			return snap, "", e
		}
	}
	raw, _ := json.Marshal(snap)
	if e = os.WriteFile(filepath.Join(dir, "snapshot.json"), raw, 0640); e != nil {
		return snap, "", e
	}
	complete = true
	return snap, generation, nil
}
func fileFor(dir, path string) string {
	if path == "/" {
		return filepath.Join(dir, "index.html")
	}
	if strings.HasSuffix(path, ".xml") || strings.HasSuffix(path, ".txt") {
		return filepath.Join(dir, path[1:])
	}
	return filepath.Join(dir, filepath.FromSlash(path[1:]), "index.html")
}
func writePage(dir, path string, b []byte) error {
	f := fileFor(dir, path)
	if e := os.MkdirAll(filepath.Dir(f), 0750); e != nil {
		return e
	}
	return os.WriteFile(f, b, 0640)
}
func robots(site Site) string {
	var b strings.Builder
	for _, agent := range []string{"*", "Googlebot", "bingbot", "Baiduspider", "GPTBot", "OAI-SearchBot", "ChatGPT-User", "ClaudeBot", "Claude-SearchBot", "Claude-User", "Google-Extended"} {
		fmt.Fprintf(&b, "User-agent: %s\nAllow: /\n", agent)
		for _, p := range []string{"/api/", "/admin", "/console", "/sales", "/login", "/register", "/auth/", "/cart", "/order", "/preview"} {
			fmt.Fprintf(&b, "Disallow: %s\n", p)
		}
		b.WriteByte('\n')
	}
	fmt.Fprintf(&b, "Sitemap: %s/sitemap.xml\n", site.Origin)
	return b.String()
}
func sitemap(snap Snapshot, pages []Page) []byte {
	var b strings.Builder
	b.WriteString(xml.Header + `<urlset xmlns="http://www.sitemaps.org/schemas/sitemap/0.9">`)
	for _, p := range pages {
		if inherit(p.SEO, snap.Document.Site.SEO).NoIndex {
			continue
		}
		b.WriteString("<url><loc>")
		_ = xml.EscapeText(&b, []byte(snap.Document.Site.Origin+p.Path))
		b.WriteString("</loc>")
		if p.UpdatedAt != "" {
			b.WriteString("<lastmod>")
			_ = xml.EscapeText(&b, []byte(p.UpdatedAt))
			b.WriteString("</lastmod>")
		}
		b.WriteString("</url>")
	}
	b.WriteString("</urlset>")
	return []byte(b.String())
}

// Database publication is the atomic pointer. A complete generation exists before
// the transaction commits; a crash cannot expose a partly written directory.
func (s *Service) Save(ctx context.Context, d Document, publish bool, actor int64, ip, action string) (Document, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	row, e := s.row(ctx)
	if e != nil {
		return d, e
	}
	if row.Revision != d.Revision {
		return d, errors.New("内容已更新，请刷新后重试")
	}
	if e = Validate(d); e != nil {
		return d, e
	}
	for pi := range d.Pages {
		for mi := range d.Pages[pi].Modules {
			m := &d.Pages[pi].Modules[mi]
			if len(m.RichContent) > 0 {
				canonical, text, err := richtext.Validate(m.RichContent, nil)
				if err != nil {
					return d, err
				}
				m.RichContent, m.Text = canonical, text
			}
		}
	}
	now := time.Now().UTC().Format(time.RFC3339)
	old := Seed()
	if row.Draft != "" {
		_ = json.Unmarshal([]byte(row.Draft), &old)
	}
	for i := range d.Pages {
		changed := true
		for _, p := range old.Pages {
			if p.ID == d.Pages[i].ID {
				a, b := p, d.Pages[i]
				a.UpdatedAt = ""
				b.UpdatedAt = ""
				ab, _ := json.Marshal(a)
				bb, _ := json.Marshal(b)
				if bytes.Equal(ab, bb) {
					changed = false
					d.Pages[i].UpdatedAt = p.UpdatedAt
				}
			}
		}
		if changed || d.Pages[i].UpdatedAt == "" {
			d.Pages[i].UpdatedAt = now
		}
	}
	d.Revision++
	raw, _ := json.Marshal(d)
	updates := map[string]any{"revision": d.Revision, "draft": string(raw), "updated_at": time.Now()}
	generation := ""
	if publish {
		snap, g, e := s.generate(ctx, d)
		if e != nil {
			return d, e
		}
		generation = g
		pub, _ := json.Marshal(snap)
		updates["published"] = string(pub)
		updates["fingerprint"] = snap.Fingerprint
		updates["generation"] = g
	}
	e = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var locked siteRow
		if e := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&locked, 1).Error; e != nil {
			return e
		}
		if locked.Revision != row.Revision {
			return errors.New("内容已更新，请刷新后重试")
		}
		if e := tx.Model(&siteRow{}).Where("id = 1").Updates(updates).Error; e != nil {
			return e
		}
		return tx.Create(&revisionRow{Revision: d.Revision, Document: string(raw), ActorID: actor, OperatorIP: ip, Action: action, CreatedAt: time.Now()}).Error
	})
	if e != nil && generation != "" {
		_ = os.RemoveAll(filepath.Join(s.root, "generations", generation))
	}
	return d, e
}
func (s *Service) Current(ctx context.Context) (Snapshot, string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	row, e := s.row(ctx)
	if e != nil {
		return Snapshot{}, "", e
	}
	if row.Published == "" {
		return Snapshot{}, "", errors.New("门户尚未发布，请在后台保存并生效")
	}
	var snap Snapshot
	if e = json.Unmarshal([]byte(row.Published), &snap); e != nil {
		return snap, "", e
	}
	products, e := s.products(ctx)
	if e != nil {
		return snap, "", e
	}
	assets, body, e := s.theme(snap.Document.Site.Theme)
	if e != nil {
		return snap, "", e
	}
	b, _ := json.Marshal(struct {
		D Document
		P []Product
		A Assets
	}{snap.Document, products, assets})
	sum := sha256.Sum256(append(b, body...))
	fingerprint := hex.EncodeToString(sum[:])
	_, stat := os.Stat(filepath.Join(s.root, "generations", row.Generation, "snapshot.json"))
	if fingerprint != row.Fingerprint || stat != nil {
		next, g, e := s.generate(ctx, snap.Document)
		if e != nil {
			return snap, "", e
		}
		raw, _ := json.Marshal(next)
		result := s.db.WithContext(ctx).Model(&siteRow{}).Where("id=1 AND generation=?", row.Generation).Updates(map[string]any{"published": string(raw), "fingerprint": next.Fingerprint, "generation": g})
		if result.Error != nil {
			return snap, "", result.Error
		}
		if result.RowsAffected != 1 {
			return snap, "", errors.New("门户发布状态已变化，请重试")
		}
		snap = next
		row.Generation = g
	}
	return snap, filepath.Join(s.root, "generations", row.Generation), nil
}
func (s *Service) Bootstrap(ctx context.Context) error {
	// Kept for existing startup/public-read callers. Installing assets, reading a
	// public page or starting a worker must never publish an unconfirmed draft.
	// Only an authenticated administrator's explicit publish/rollback does so.
	return nil
}
func (s *Service) Revisions(ctx context.Context) ([]revisionRow, error) {
	rows := []revisionRow{}
	e := s.db.WithContext(ctx).Order("revision DESC").Limit(100).Find(&rows).Error
	return rows, e
}
func (s *Service) Rollback(ctx context.Context, id string, expectedRevision int64, actor int64, ip string) (Document, error) {
	var r revisionRow
	if e := s.db.WithContext(ctx).Where("id=?", id).First(&r).Error; e != nil {
		return Document{}, e
	}
	var d Document
	if e := json.Unmarshal([]byte(r.Document), &d); e != nil {
		return d, e
	}
	current, e := s.Draft(ctx)
	if e != nil {
		return d, e
	}
	if expectedRevision != current.Revision {
		return d, errors.New("内容已更新，请刷新后重试")
	}
	d.Revision = current.Revision
	return s.Save(ctx, d, true, actor, ip, "rollback")
}
