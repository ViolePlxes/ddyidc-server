package navigation

import (
	"context"
	schemamode "ddyidc/internal/shared/schema"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"ddyidc/internal/shared/snowflake"
	"ddyidc/pkg/auth"
	"github.com/zeromicro/go-zero/rest"
	"github.com/zeromicro/go-zero/rest/pathvar"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

//go:embed schema.sql
var schema string

//go:embed seed.json
var seed []byte

type Node struct {
	ID        string    `json:"id" gorm:"primaryKey;autoIncrement:false"`
	Scope     string    `json:"scope"`
	ParentID  string    `json:"parentId"`
	Name      string    `json:"name"`
	Href      string    `json:"href"`
	Target    string    `json:"target"`
	Icon      string    `json:"icon"`
	Gate      string    `json:"gate"`
	Enabled   bool      `json:"enabled"`
	Sort      int       `json:"sort"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

func (Node) TableName() string { return "system.navigation_nodes" }

type scopeRow struct {
	Scope    string `gorm:"primaryKey"`
	Revision int64
	Seeded   bool
}

func (scopeRow) TableName() string { return "system.navigation_scopes" }

type Snapshot struct {
	Revision int64  `json:"revision"`
	Nodes    []Node `json:"nodes"`
}
type Input struct {
	Revision *int64   `json:"revision"`
	ParentID string   `json:"parentId"`
	Name     string   `json:"name"`
	Href     string   `json:"href"`
	Target   string   `json:"target"`
	Icon     string   `json:"icon"`
	Enabled  *bool    `json:"enabled"`
	IDs      []string `json:"ids"`
}
type Service struct{ db *gorm.DB }

func New(db *gorm.DB) *Service { return &Service{db: db} }

var errStale = errors.New("菜单已被修改，请刷新后重试")
var errInvalid = errors.New("参数无效或记录不存在")

func validScope(s string) bool { return s == "console" || s == "admin" || s == "sales" }
func (s *Service) EnsureSchema(ctx context.Context) error {
	if !schemamode.MigrationsEnabled() {
		return nil
	}
	for _, statement := range strings.Split(schema, ";") {
		if strings.TrimSpace(statement) != "" {
			if err := s.db.WithContext(ctx).Exec(statement).Error; err != nil {
				return err
			}
		}
	}
	var nodes []Node
	if err := json.Unmarshal(seed, &nodes); err != nil {
		return err
	}
	for _, scope := range []string{"console", "admin", "sales"} {
		if err := s.db.WithContext(ctx).Clauses(clause.OnConflict{DoNothing: true}).Create(&scopeRow{Scope: scope}).Error; err != nil {
			return err
		}
		if err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			var row scopeRow
			if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("scope = ?", scope).Take(&row).Error; err != nil {
				return err
			}
			if row.Seeded {
				return nil
			}
			for _, n := range nodes {
				if n.Scope == scope {
					if err := tx.Create(&n).Error; err != nil {
						return err
					}
				}
			}
			return tx.Model(&row).Updates(map[string]any{"seeded": true, "revision": 1}).Error
		}); err != nil {
			return err
		}
	}
	return nil
}
func (s *Service) List(ctx context.Context, scope string, enabledOnly bool) (Snapshot, error) {
	out := Snapshot{Nodes: []Node{}}
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var row scopeRow
		if err := tx.Clauses(clause.Locking{Strength: "SHARE"}).Where("scope = ?", scope).Take(&row).Error; err != nil {
			return err
		}
		out.Revision = row.Revision
		q := tx.Where("scope = ?", scope).Order("sort ASC, id DESC")
		if enabledOnly {
			q = q.Where("enabled = ?", true)
		}
		return q.Find(&out.Nodes).Error
	})
	return out, err
}
func validate(in Input) error {
	if in.Enabled == nil || strings.TrimSpace(in.Name) == "" || utf8.RuneCountInString(in.Name) > 80 || len(in.Icon) > 80 || in.Icon == "" {
		return errInvalid
	}
	if in.ParentID == "0" {
		return nil
	}
	if in.Target != "self" && in.Target != "blank" {
		return errInvalid
	}
	if !validHref(in.Href) {
		return errors.New("地址必须为站内绝对路径或 http/https 链接")
	}
	return nil
}
func validHref(href string) bool {
	if href == "" || len(href) > 2048 || strings.Contains(href, "\\") || strings.ContainsFunc(href, func(r rune) bool { return unicode.IsControl(r) || unicode.IsSpace(r) }) {
		return false
	}
	u, err := url.Parse(href)
	if err != nil {
		return false
	}
	if strings.HasPrefix(href, "/") {
		return !strings.HasPrefix(href, "//") && u.Host == "" && u.Scheme == ""
	}
	return (u.Scheme == "https" || u.Scheme == "http") && u.Host != "" && u.User == nil
}
func fullOrder(nodes []Node, ids []string) bool {
	if len(nodes) != len(ids) {
		return false
	}
	wanted := map[string]bool{}
	for _, n := range nodes {
		wanted[n.ID] = true
	}
	for _, id := range ids {
		if !wanted[id] {
			return false
		}
		delete(wanted, id)
	}
	return len(wanted) == 0
}

// Scope row lock serializes every write, including creation/deletion and sorting.
// Revision rejects a stale full list instead of silently overwriting another admin.
func (s *Service) Change(ctx context.Context, scope, id, operation string, in Input) error {
	if !validScope(scope) || in.Revision == nil {
		return errInvalid
	}
	if operation == "save" {
		if err := validate(in); err != nil {
			return err
		}
	}
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var row scopeRow
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("scope = ?", scope).Take(&row).Error; err != nil {
			return err
		}
		if row.Revision != *in.Revision {
			return errStale
		}
		var existing Node
		if id != "" {
			if err := tx.Where("id = ? AND scope = ?", id, scope).Take(&existing).Error; err != nil {
				return errInvalid
			}
		}
		switch operation {
		case "save":
			if id != "" && existing.ParentID != in.ParentID {
				return errors.New("不可改变层级或所属分组")
			}
			if in.ParentID != "0" {
				var parent Node
				if tx.Where("id = ? AND scope = ? AND parent_id = 0", in.ParentID, scope).Take(&parent).Error != nil {
					return errInvalid
				}
			}
			if id == "" {
				var max int
				if err := tx.Model(&Node{}).Select("COALESCE(MAX(sort), -1)").Where("scope = ? AND parent_id = ?", scope, in.ParentID).Scan(&max).Error; err != nil {
					return err
				}
				existing = Node{ID: fmt.Sprint(snowflake.GenID()), Scope: scope, ParentID: in.ParentID, Sort: max + 1}
			}
			existing.Name = strings.TrimSpace(in.Name)
			existing.Href = in.Href
			existing.Target = in.Target
			existing.Icon = in.Icon
			existing.Enabled = *in.Enabled
			if in.ParentID == "0" {
				existing.Href = ""
				existing.Target = "self"
			}
			if err := tx.Save(&existing).Error; err != nil {
				return err
			}
		case "delete":
			if id == "" {
				return errInvalid
			}
			// Removing a group removes only its navigation children, never registered routes.
			if err := tx.Where("scope = ? AND (id = ? OR parent_id = ?)", scope, id, id).Delete(&Node{}).Error; err != nil {
				return err
			}
		case "sort":
			if in.ParentID == "" {
				return errInvalid
			}
			if in.ParentID != "0" {
				var parent Node
				if tx.Where("id = ? AND scope = ? AND parent_id = 0", in.ParentID, scope).Take(&parent).Error != nil {
					return errInvalid
				}
			}
			var nodes []Node
			if err := tx.Where("scope = ? AND parent_id = ?", scope, in.ParentID).Find(&nodes).Error; err != nil {
				return err
			}
			if !fullOrder(nodes, in.IDs) {
				return errors.New("必须提交当前层级全部且不重复的菜单 ID")
			}
			for sort, id := range in.IDs {
				if err := tx.Model(&Node{}).Where("id = ? AND scope = ?", id, scope).Updates(map[string]any{"sort": sort, "updated_at": time.Now()}).Error; err != nil {
					return err
				}
			}
		default:
			return errInvalid
		}
		return tx.Model(&row).Update("revision", row.Revision+1).Error
	})
}

func Register(server *rest.Server, s *Service, manager *auth.Manager) {
	reply := func(w http.ResponseWriter, status int, data any, message string) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(map[string]any{"code": status, "data": data, "message": message})
	}
	authorize := func(w http.ResponseWriter, r *http.Request, kind auth.SessionKind, role string) bool {
		token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		claims, err := manager.ParseAccessTokenFor(r.Context(), kind, token)
		if err != nil || claims.Role != role {
			reply(w, 401, nil, "登录已失效或没有访问权限")
			return false
		}
		return true
	}
	read := func(scope string, kind auth.SessionKind, role string) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if !authorize(w, r, kind, role) {
				return
			}
			out, err := s.List(r.Context(), scope, true)
			if err != nil {
				reply(w, 500, nil, "读取导航失败")
				return
			}
			reply(w, 200, out, "")
		}
	}
	server.AddRoutes([]rest.Route{
		{Method: "GET", Path: "/api/v1/navigation", Handler: read("console", auth.SessionUser, "customer")},
		{Method: "GET", Path: "/admin/v1/navigation", Handler: read("admin", auth.SessionAdmin, "admin")},
		{Method: "GET", Path: "/admin/v1/sales/navigation", Handler: read("sales", auth.SessionSales, "sales")},
	})
	handler := func(operation string) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if !authorize(w, r, auth.SessionAdmin, "admin") {
				return
			}
			vars := pathvar.Vars(r)
			scope := vars["scope"]
			if !validScope(scope) {
				reply(w, 400, nil, "入口类型无效")
				return
			}
			if operation == "list" {
				out, err := s.List(r.Context(), scope, false)
				if err != nil {
					reply(w, 500, nil, "读取导航失败")
					return
				}
				reply(w, 200, out, "")
				return
			}
			var in Input
			decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 32768))
			decoder.DisallowUnknownFields()
			if decoder.Decode(&in) != nil || decoder.Decode(&struct{}{}) != io.EOF {
				reply(w, 400, nil, "请求格式无效")
				return
			}
			err := s.Change(r.Context(), scope, vars["id"], operation, in)
			if err != nil {
				status := 400
				if errors.Is(err, errStale) {
					status = 409
				}
				reply(w, status, nil, err.Error())
				return
			}
			reply(w, 200, nil, "保存成功")
		}
	}
	server.AddRoutes([]rest.Route{
		{Method: "GET", Path: "/admin/v1/navigation-config/:scope", Handler: handler("list")},
		{Method: "POST", Path: "/admin/v1/navigation-config/:scope/nodes", Handler: handler("save")},
		{Method: "PUT", Path: "/admin/v1/navigation-config/:scope/nodes/:id", Handler: handler("save")},
		{Method: "DELETE", Path: "/admin/v1/navigation-config/:scope/nodes/:id", Handler: handler("delete")},
		{Method: "PUT", Path: "/admin/v1/navigation-config/:scope/sort", Handler: handler("sort")},
	})
}
