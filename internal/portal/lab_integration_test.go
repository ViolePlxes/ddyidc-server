package portal

import (
	"context"
	productdomain "ddyidc/internal/product/domain"
	"ddyidc/internal/product/specification"
	"ddyidc/pkg/auth"
	"encoding/json"
	"github.com/zeromicro/go-zero/rest"
	"github.com/zeromicro/go-zero/rest/pathvar"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// Refuse non-isolated databases before executing any fixture DDL.
func cmsLab(t *testing.T) (*Service, *gorm.DB) {
	t.Helper()
	socket := os.Getenv("DDYIDC_CMS_LAB_SOCKET")
	if socket == "" {
		t.Skip("explicit isolated UNIX-socket CMS lab required")
	}
	if !strings.HasPrefix(socket, "/tmp/cms-batch1-mysql-") || filepath.Base(socket) != "mysql.sock" {
		t.Fatal("refuse non-lab socket")
	}
	db, err := gorm.Open(mysql.Open("root@unix("+socket+")/?parseTime=true&charset=utf8mb4"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	var datadir string
	if err = db.Raw("SELECT @@datadir").Scan(&datadir).Error; err != nil || !strings.HasPrefix(datadir, "/tmp/cms-batch1-mysql-") {
		t.Fatal("refuse non-lab datadir")
	}
	for _, query := range []string{
		"CREATE DATABASE IF NOT EXISTS `system`", "CREATE DATABASE IF NOT EXISTS product",
		"CREATE TABLE IF NOT EXISTS `system`.portal_sites (id BIGINT PRIMARY KEY, revision BIGINT, draft LONGTEXT, published LONGTEXT, fingerprint TEXT, generation TEXT, updated_at DATETIME(6))",
		"CREATE TABLE IF NOT EXISTS `system`.portal_revisions (id BIGINT PRIMARY KEY AUTO_INCREMENT, revision BIGINT, document LONGTEXT, actor_id BIGINT, operator_ip VARCHAR(100), action VARCHAR(50), created_at DATETIME(6))",
		"CREATE TABLE IF NOT EXISTS product.products (id BIGINT,group_id BIGINT,status INT,hidden TINYINT,name TEXT,module_type TEXT,stock INT,summary TEXT,description TEXT,sort INT)",
		"CREATE TABLE IF NOT EXISTS product.product_groups (id BIGINT,parent_id BIGINT,status INT,name TEXT,sort INT)",
		"CREATE TABLE IF NOT EXISTS product.upstream_products (product_id BIGINT,hidden TINYINT,price DECIMAL(14,2),cycle TEXT,supplier_id BIGINT,upstream_product_id BIGINT)",
	} {
		if err = db.Exec(query).Error; err != nil {
			t.Fatal(err)
		}
	}
	for _, fixture := range []struct {
		schema, table string
		model         any
	}{{"system", "portal_media", &mediaRow{}}, {"product", "product_specs", &productdomain.ProductSpec{}}, {"product", "product_spec_snapshots", &specification.Snapshot{}}} {
		var count int64
		if err = db.Raw("SELECT COUNT(*) FROM information_schema.tables WHERE table_schema=? AND table_name=?", fixture.schema, fixture.table).Scan(&count).Error; err != nil {
			t.Fatal(err)
		}
		if count == 0 {
			if err = db.AutoMigrate(fixture.model); err != nil {
				t.Fatal(err)
			}
		}
	}
	for _, query := range []string{
		"INSERT INTO product.product_groups(id,parent_id,status,name,sort) SELECT 1,0,1,'本地演示资源',0 WHERE NOT EXISTS(SELECT 1 FROM product.product_groups WHERE id=1)",
		"INSERT INTO product.products(id,group_id,status,hidden,name,module_type,stock,summary,description,sort) SELECT 900000000001,1,1,0,'本地演示云服务器','local',10,'仅供界面与只读查询测试，不提供真实开通。','本地隔离商品，不会触发采购或扣款。',1 WHERE NOT EXISTS(SELECT 1 FROM product.products WHERE id=900000000001)",
		"INSERT INTO product.upstream_products(product_id,hidden,price,cycle,supplier_id,upstream_product_id) SELECT 900000000001,0,30.00,'monthly',0,0 WHERE NOT EXISTS(SELECT 1 FROM product.upstream_products WHERE product_id=900000000001)",
	} {
		if err = db.Exec(query).Error; err != nil {
			t.Fatal(err)
		}
	}

	seed := Seed()
	raw, _ := json.Marshal(seed)
	if err = db.Exec("INSERT IGNORE INTO `system`.portal_sites(id,revision,draft,published,fingerprint,generation,updated_at) VALUES (1,0,?,'','','',NOW())", string(raw)).Error; err != nil {
		t.Fatal(err)
	}
	service := New(db, os.Getenv("DDYIDC_CMS_LAB_ROOT"))
	if err = service.Prepare(); err != nil {
		t.Fatal(err)
	}
	return service, db
}
func TestCMSLabPublishDraftHideRollback(t *testing.T) {
	s, _ := cmsLab(t)
	ctx := context.Background()
	d, err := s.Draft(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// Leave ordinary local preview on a clearly identified company fixture.
	d.Site.Brand = "多多云"
	d.Site.Navigation = []Link{{Label: "企业信息", Href: "/company"}, {Label: "关于多多云", Href: "/about"}, {Label: "服务支持", Href: "/help"}}
	for i := range d.Pages {
		if d.Pages[i].Path == "/company" {
			d.Pages = append(d.Pages[:i], d.Pages[i+1:]...)
			break
		}
	}
	d.Pages = append(d.Pages, Page{ID: "lab-company", Path: "/company", Title: "企业信息", Kind: "page", Enabled: true, Modules: []Module{{ID: "lab-profile", Type: "feature_sections", Title: "企业介绍", Text: "本地演示企业介绍，正式内容由管理员维护。", Enabled: true}, {ID: "lab-private", Type: "feature_sections", Text: "not-public-secret", Enabled: false}}})
	d, err = s.Save(ctx, d, true, 1, "127.0.0.1", "publish")
	if err != nil {
		t.Fatal(err)
	}
	revisions, _ := s.Revisions(ctx)
	publishedID := strconv.FormatInt(revisions[0].ID, 10)
	before := d.Revision
	d.Pages[len(d.Pages)-1].Modules[0].Text = "draft-not-public"
	d, err = s.Save(ctx, d, false, 1, "127.0.0.1", "draft")
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest("GET", "/api/v1/portal/content", nil)
	rec := httptest.NewRecorder()
	s.publicContent(rec, request)
	if rec.Code != 200 || strings.Contains(rec.Body.String(), "draft-not-public") || strings.Contains(rec.Body.String(), "not-public-secret") {
		t.Fatal("draft/hidden module leaked")
	}
	var body struct{ Data PublicContent }
	if err = json.Unmarshal(rec.Body.Bytes(), &body); err != nil || body.Data.Revision != before {
		t.Fatal("publication revision moved on draft save")
	}
	stale := d
	stale.Revision--
	if _, err = s.Save(ctx, stale, true, 1, "127.0.0.1", "publish"); err == nil {
		t.Fatal("stale revision accepted")
	}
	d.Pages[len(d.Pages)-1].Enabled = false
	d, err = s.Save(ctx, d, true, 1, "127.0.0.1", "publish")
	if err != nil {
		t.Fatal(err)
	}
	snap, _, err := s.Current(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, page := range publicContent(snap).Pages {
		if page.Path == "/company" {
			t.Fatal("hidden page leaked")
		}
	}
	d, err = s.Rollback(ctx, publishedID, d.Revision, 1, "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	rec = httptest.NewRecorder()
	s.publicContent(rec, request)
	if !strings.Contains(rec.Body.String(), "本地演示企业介绍") || strings.Contains(rec.Body.String(), "draft-not-public") {
		t.Fatal("rollback failed")
	}
	t.Log("real isolated MySQL: save draft / publish / hidden module / hidden page / conflict / rollback passed")
}

// Local-only test server: production Register/auth routes remain untouched.
func TestCMSLabServer(t *testing.T) {
	if os.Getenv("DDYIDC_CMS_LAB_SERVE") != "1" {
		t.Skip("local preview only")
	}
	s, _ := cmsLab(t)
	server := rest.MustNewServer(rest.RestConf{Host: "127.0.0.1", Port: 18092, Timeout: 15000, MaxBytes: 8 << 20})
	defer server.Stop()
	registerPortalRoutes(server, s, func(w http.ResponseWriter, r *http.Request) *auth.Claims {
		if r.Header.Get("X-CMS-Lab") != "local-preview" {
			reply(w, 403, nil, "仅限显式启动的本地隔离演示")
			return nil
		}
		return &auth.Claims{UserID: 1, Role: "admin"}
	})
	server.AddRoute(rest.Route{Method: "GET", Path: "/assets/:asset", Handler: func(w http.ResponseWriter, r *http.Request) {
		name := pathvar.Vars(r)["asset"]
		if filepath.Base(name) != name || strings.Contains(name, "..") {
			http.NotFound(w, r)
			return
		}
		http.ServeFile(w, r, filepath.Join(s.dist, "assets", name))
	}})
	server.AddRoute(rest.Route{Method: "GET", Path: "/version.json", Handler: func(w http.ResponseWriter, r *http.Request) {
		http.ServeFile(w, r, filepath.Join(s.dist, "version.json"))
	}})
	server.AddRoute(rest.Route{Method: "GET", Path: "/api/v1/products", Handler: func(w http.ResponseWriter, r *http.Request) {
		rows, err := s.products(r.Context())
		if err != nil {
			reply(w, 503, nil, err.Error())
			return
		}
		items := []map[string]any{}
		for _, p := range rows {
			price, _ := strconv.ParseFloat(p.Price, 64)
			items = append(items, map[string]any{"id": p.ID, "name": p.Name, "summary": p.Summary, "description": p.Description, "groupName": p.Group, "groupId": "1", "parentGroupId": "0", "moduleType": "local", "stock": 0, "price": price, "cycle": p.Cycle, "sort": 1})
		}
		reply(w, 200, map[string]any{"items": items, "total": len(items)}, "")
	}})
	server.AddRoute(rest.Route{Method: "GET", Path: "/api/v1/products/:id", Handler: func(w http.ResponseWriter, r *http.Request) {
		id := pathvar.Vars(r)["id"]
		rows, err := s.products(r.Context())
		if err != nil {
			reply(w, 503, nil, "读取产品失败")
			return
		}
		for _, p := range rows {
			if p.ID == id {
				price, _ := strconv.ParseFloat(p.Price, 64)
				reply(w, 200, map[string]any{"id": id, "name": p.Name, "summary": p.Summary, "description": p.Description, "groupName": p.Group, "price": price, "cycle": p.Cycle, "stock": 0, "readOnly": true, "config": map[string]string{}, "specs": []any{}}, "")
				return
			}
		}
		reply(w, 404, nil, "商品不存在")
	}})
	for _, path := range []string{"/", "/:one", "/:one/:two", "/:one/:two/:three"} {
		server.AddRoute(rest.Route{Method: "GET", Path: path, Handler: func(w http.ResponseWriter, r *http.Request) {
			query := r.URL.Query()
			query.Set("path", r.URL.Path)
			r.URL.RawQuery = query.Encode()
			s.publicDocument(w, r)
		}})
	}
	t.Log("CMS lab actual route factory on 127.0.0.1:18092; no purchase/payment workers")
	server.Start()
}
