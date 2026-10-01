package portal

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"ddyidc/internal/admintodo"
	"ddyidc/internal/shared/distsync"
	"ddyidc/pkg/auth"
	"ddyidc/pkg/clientip"
	"github.com/google/uuid"
	"github.com/zeromicro/go-zero/rest"
	"github.com/zeromicro/go-zero/rest/pathvar"
)

func reply(w http.ResponseWriter, status int, data any, message string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	code := 0
	if status >= 400 {
		code = status
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"code": code, "data": data, "message": message})
}
func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 2<<20))
	d.DisallowUnknownFields()
	if d.Decode(v) != nil || d.Decode(&struct{}{}) != io.EOF {
		reply(w, 400, nil, "配置格式无效或超过大小限制")
		return false
	}
	return true
}
func (s *Service) publicDocument(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Query().Get("path")
	if path == "" {
		path = "/"
	}
	if !pagePath.MatchString(path) && path != "/robots.txt" && path != "/sitemap.xml" && path != "/llms.txt" {
		http.NotFound(w, r)
		return
	}
	if strings.HasSuffix(path, "/") && path != "/" {
		http.Redirect(w, r, strings.TrimSuffix(path, "/"), 301)
		return
	}
	if e := s.Bootstrap(r.Context()); e != nil {
		http.Error(w, "公开内容暂时不可用，请稍后重试", 503)
		return
	}
	snap, dir, e := s.Current(r.Context())
	if e != nil {
		w.Header().Set("Cache-Control", "no-store")
		http.Error(w, "公开内容暂时不可用，请稍后重试", 503)
		return
	}
	if !snap.Document.Site.Enabled {
		http.Error(w, "门户维护中", 503)
		return
	}
	// Resolve only an allowlisted public path, never arbitrary files in a generation.
	found := path == "/robots.txt" || path == "/sitemap.xml" || path == "/llms.txt" && snap.Document.Site.LLMS
	for _, p := range pagesFor(snap) {
		if p.Path == path {
			found = true
		}
	}
	if !found {
		w.Header().Set("X-Robots-Tag", "noindex")
		http.NotFound(w, r)
		return
	}
	data, e := os.ReadFile(fileFor(dir, path))
	if e != nil {
		http.Error(w, "公开内容暂时不可用", 503)
		return
	}
	w.Header().Set("Cache-Control", "no-cache, must-revalidate")
	w.Header().Set("X-Portal-Revision", fmt.Sprint(snap.Document.Revision))
	w.Header().Set("X-Content-Type-Options", "nosniff")
	hash := sha256.Sum256(data)
	etag := fmt.Sprintf(`"%x"`, hash)
	w.Header().Set("ETag", etag)
	if r.Header.Get("If-None-Match") == etag {
		w.WriteHeader(304)
		return
	}
	contentType := "text/html; charset=utf-8"
	if path == "/sitemap.xml" {
		contentType = "application/xml; charset=utf-8"
	}
	if strings.HasSuffix(path, ".txt") {
		contentType = "text/plain; charset=utf-8"
	}
	w.Header().Set("Content-Type", contentType)
	_, _ = w.Write(data)
}
func Register(server *rest.Server, s *Service, manager *auth.Manager) {
	authorize := func(w http.ResponseWriter, r *http.Request) *auth.Claims {
		claims, e := manager.ParseAccessTokenFor(r.Context(), auth.SessionAdmin, strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
		if e != nil || claims.Role != "admin" {
			reply(w, 401, nil, "请使用管理员会话")
			return nil
		}
		if !admintodo.IsSuper(r.Context(), s.db, claims.UserID) {
			reply(w, 403, nil, "门户内容管理需要超级管理员权限")
			return nil
		}
		return claims
	}
	server.AddRoute(rest.Route{Method: "GET", Path: "/api/v1/portal/document", Handler: s.publicDocument})
	for _, path := range []string{"site", "revision", "pages"} {
		scope := path
		server.AddRoute(rest.Route{Method: "GET", Path: "/api/v1/portal/" + scope, Handler: func(w http.ResponseWriter, r *http.Request) {
			snap, _, e := s.Current(r.Context())
			if e != nil || !snap.Document.Site.Enabled {
				reply(w, 503, nil, "公开内容暂时不可用")
				return
			}
			switch scope {
			case "site":
				reply(w, 200, map[string]any{"site": snap.Document.Site, "revision": snap.Document.Revision}, "")
			case "revision":
				reply(w, 200, map[string]any{"revision": snap.Document.Revision, "generation": snap.Fingerprint}, "")
			case "pages":
				out := []Page{}
				for _, p := range pagesFor(snap) {
					out = append(out, p)
				}
				reply(w, 200, out, "")
			}
		}})
	}
	server.AddRoute(rest.Route{Method: "GET", Path: "/admin/v1/portal/config", Handler: func(w http.ResponseWriter, r *http.Request) {
		if authorize(w, r) == nil {
			return
		}
		d, e := s.Draft(r.Context())
		if e != nil {
			reply(w, 503, nil, "读取门户配置失败")
			return
		}
		reply(w, 200, d, "")
	}})
	server.AddRoute(rest.Route{Method: "GET", Path: "/admin/v1/portal/themes", Handler: func(w http.ResponseWriter, r *http.Request) {
		if authorize(w, r) == nil {
			return
		}
		themes, e := s.Themes()
		if e != nil {
			reply(w, 503, nil, "主题目录暂时不可用")
			return
		}
		reply(w, 200, map[string]any{"themes": themes, "modules": Definitions}, "")
	}})
	server.AddRoute(rest.Route{Method: "GET", Path: "/admin/v1/portal/products", Handler: func(w http.ResponseWriter, r *http.Request) {
		if authorize(w, r) == nil {
			return
		}
		rows, e := s.products(r.Context())
		if e != nil {
			reply(w, 503, nil, "读取公开产品目录失败")
			return
		}
		reply(w, 200, rows, "")
	}})
	server.AddRoute(rest.Route{Method: "PUT", Path: "/admin/v1/portal/config", Handler: func(w http.ResponseWriter, r *http.Request) {
		claims := authorize(w, r)
		if claims == nil {
			return
		}
		var in struct {
			Document Document `json:"document"`
			Publish  bool     `json:"publish"`
		}
		if !decode(w, r, &in) {
			return
		}
		action := "draft"
		if in.Publish {
			action = "publish"
		}
		d, e := s.Save(r.Context(), in.Document, in.Publish, claims.UserID, clientip.FromRequest(r), action)
		if e != nil {
			status := 400
			if strings.Contains(e.Error(), "请刷新") {
				status = 409
			}
			if Validate(in.Document) == nil && status != 409 {
				reply(w, 503, nil, "发布失败，公开版本保持原状；请检查服务日志和主题资源")
				return
			}
			reply(w, status, nil, e.Error())
			return
		}
		reply(w, 200, d, "配置已保存")
	}})
	server.AddRoute(rest.Route{Method: "GET", Path: "/admin/v1/portal/revisions", Handler: func(w http.ResponseWriter, r *http.Request) {
		if authorize(w, r) == nil {
			return
		}
		rows, e := s.Revisions(r.Context())
		if e != nil {
			reply(w, 503, nil, "读取发布记录失败")
			return
		}
		reply(w, 200, rows, "")
	}})
	server.AddRoute(rest.Route{Method: "POST", Path: "/admin/v1/portal/revisions/:id/rollback", Handler: func(w http.ResponseWriter, r *http.Request) {
		claims := authorize(w, r)
		if claims == nil {
			return
		}
		d, e := s.Rollback(r.Context(), pathvar.Vars(r)["id"], claims.UserID, clientip.FromRequest(r))
		if e != nil {
			reply(w, 400, nil, "回滚失败，请检查版本兼容性并刷新重试")
			return
		}
		reply(w, 200, d, "已回滚并发布")
	}})
	server.AddRoute(rest.Route{Method: "POST", Path: "/admin/v1/portal/preview", Handler: func(w http.ResponseWriter, r *http.Request) {
		if authorize(w, r) == nil {
			return
		}
		var in struct {
			Document Document `json:"document"`
			Path     string   `json:"path"`
		}
		if !decode(w, r, &in) {
			return
		}
		if !pagePath.MatchString(in.Path) {
			reply(w, 400, nil, "预览路径无效")
			return
		}
		in.Document.Site.SEO.NoIndex = true
		s.mu.Lock()
		_, g, e := s.generate(r.Context(), in.Document)
		s.mu.Unlock()
		if e != nil {
			reply(w, 400, nil, "无法生成预览，请检查配置和主题资源")
			return
		}
		dir := filepath.Join(s.root, "generations", g)
		defer os.RemoveAll(dir)
		data, e := os.ReadFile(fileFor(dir, in.Path))
		if e != nil {
			reply(w, 404, nil, "预览页面不存在")
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("X-Robots-Tag", "noindex, nofollow")
		w.Header().Set("Cache-Control", "no-store")
		_, _ = w.Write(data)
	}})
	server.AddRoute(rest.Route{Method: "GET", Path: "/admin/v1/portal/media", Handler: func(w http.ResponseWriter, r *http.Request) {
		if authorize(w, r) == nil {
			return
		}
		rows := []mediaRow{}
		if s.db.WithContext(r.Context()).Order("created_at DESC").Limit(300).Find(&rows).Error != nil {
			reply(w, 503, nil, "读取媒体失败")
			return
		}
		reply(w, 200, rows, "")
	}})
	server.AddRoute(rest.Route{Method: "POST", Path: "/admin/v1/portal/media", Handler: func(w http.ResponseWriter, r *http.Request) {
		claims := authorize(w, r)
		if claims == nil {
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 8<<20)
		if r.ParseMultipartForm(8<<20) != nil {
			reply(w, 400, nil, "图片必须小于 8 MB")
			return
		}
		defer r.MultipartForm.RemoveAll()
		f, h, e := r.FormFile("file")
		if e != nil {
			reply(w, 400, nil, "请选择图片")
			return
		}
		defer f.Close()
		data, e := io.ReadAll(f)
		if e != nil {
			reply(w, 400, nil, "图片读取失败")
			return
		}
		config, format, e := image.DecodeConfig(strings.NewReader(string(data)))
		if e != nil || (format != "png" && format != "jpeg" && format != "gif") || config.Width <= 0 || config.Height <= 0 || int64(config.Width)*int64(config.Height) > 25000000 {
			reply(w, 400, nil, "只支持有效 PNG、JPEG、GIF，最大 2500 万像素")
			return
		}
		id := uuid.NewString()
		path := filepath.Join(s.root, "media", id)
		if e = os.WriteFile(path, data, 0640); e != nil {
			reply(w, 503, nil, "媒体保存失败")
			return
		}
		mime := "image/" + format
		row := mediaRow{ID: id, Name: filepath.Base(h.Filename), Path: "/portal-media/" + id, Mime: mime, ActorID: claims.UserID, CreatedAt: time.Now()}
		if s.db.WithContext(r.Context()).Create(&row).Error != nil {
			_ = os.Remove(path)
			reply(w, 503, nil, "媒体登记失败")
			return
		}
		reply(w, 200, row, "上传完成")
	}})
	server.AddRoute(rest.Route{Method: "GET", Path: "/api/v1/portal/media/:id", Handler: func(w http.ResponseWriter, r *http.Request) {
		id := pathvar.Vars(r)["id"]
		if _, e := uuid.Parse(id); e != nil {
			http.NotFound(w, r)
			return
		}
		var row mediaRow
		if s.db.WithContext(r.Context()).Where("id=?", id).First(&row).Error != nil {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", row.Mime)
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		http.ServeFile(w, r, filepath.Join(s.root, "media", id))
	}})
}
func (s *Service) Start(ctx context.Context) {
	go func() {
		refresh := func() {
			_ = distsync.Work(ctx, func(work context.Context) error {
				if e := s.Bootstrap(work); e != nil {
					return e
				}
				_, _, e := s.Current(work)
				return e
			})
		}
		refresh()
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				refresh()
			}
		}
	}()
}
