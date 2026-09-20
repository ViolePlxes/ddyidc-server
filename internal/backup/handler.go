package backup

import (
	"context"
	"ddyidc/pkg/auth"
	"encoding/json"
	"errors"
	"github.com/zeromicro/go-zero/rest"
	"github.com/zeromicro/go-zero/rest/pathvar"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type handler struct {
	s    *Service
	auth *auth.Manager
}

func Register(server *rest.Server, s *Service, a *auth.Manager) {
	h := &handler{s, a}
	server.AddRoutes([]rest.Route{
		{Method: "GET", Path: "/admin/v1/backup/config", Handler: h.config},
		{Method: "PUT", Path: "/admin/v1/backup/config", Handler: h.config},
		{Method: "POST", Path: "/admin/v1/backup/test", Handler: h.test},
		{Method: "POST", Path: "/admin/v1/backup/run", Handler: h.run},
		{Method: "POST", Path: "/admin/v1/backup/restore", Handler: h.restore},
		{Method: "GET", Path: "/admin/v1/backup/history", Handler: h.history},
	})
	server.AddRoute(rest.Route{Method: "GET", Path: "/admin/v1/backup/:id/download", Handler: h.download}, rest.WithTimeout(30*time.Minute))
}
func reply(w http.ResponseWriter, status int, data any, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	code := 0
	if status >= 400 {
		code = status
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"code": code, "data": data, "message": message})
}
func (h *handler) actor(w http.ResponseWriter, r *http.Request) (int64, bool) {
	c, e := r.Cookie(h.auth.CookieName(auth.SessionAdmin))
	if e != nil {
		reply(w, 401, nil, "请先登录管理员账号")
		return 0, false
	}
	claims, e := h.auth.ParseAccessTokenFor(r.Context(), auth.SessionAdmin, c.Value)
	if e != nil {
		reply(w, 401, nil, "管理员会话已失效")
		return 0, false
	}
	if claims.Role != "admin" {
		reply(w, 403, nil, "仅管理员可管理备份")
		return 0, false
	}
	return claims.UserID, true
}
func ip(r *http.Request) string { host, _, _ := net.SplitHostPort(r.RemoteAddr); return host }
func decode(w http.ResponseWriter, r *http.Request, dst any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if decoder.Decode(dst) != nil {
		reply(w, 400, nil, "请求格式无效")
		return false
	}
	return true
}
func fail(w http.ResponseWriter, e error) {
	status := 400
	if errors.Is(e, ErrBusy) {
		status = 409
	}
	reply(w, status, nil, e.Error())
}
func (h *handler) config(w http.ResponseWriter, r *http.Request) {
	actor, ok := h.actor(w, r)
	if !ok {
		return
	}
	if r.Method == "PUT" {
		var in Config
		if !decode(w, r, &in) {
			return
		}
		if e := h.s.SaveConfig(r.Context(), in, actor, ip(r)); e != nil {
			fail(w, e)
			return
		}
	}
	c, e := h.s.Config(r.Context())
	if e != nil {
		fail(w, e)
		return
	}
	reply(w, 200, c.public(), "")
}
func (h *handler) test(w http.ResponseWriter, r *http.Request) {
	actor, ok := h.actor(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 25*time.Second)
	defer cancel()
	if e := h.s.Test(ctx, actor, ip(r)); e != nil {
		fail(w, e)
		return
	}
	reply(w, 200, map[string]bool{"success": true}, "")
}
func (h *handler) run(w http.ResponseWriter, r *http.Request) {
	actor, ok := h.actor(w, r)
	if !ok {
		return
	}
	j, e := h.s.Trigger(r.Context(), "manual", "", actor, ip(r))
	if e != nil {
		fail(w, e)
		return
	}
	reply(w, 202, j, "")
}
func (h *handler) restore(w http.ResponseWriter, r *http.Request) {
	actor, ok := h.actor(w, r)
	if !ok {
		return
	}
	var in struct {
		ID           string `json:"id"`
		Confirmation string `json:"confirmation"`
	}
	if !decode(w, r, &in) {
		return
	}
	if in.Confirmation != "恢复 "+in.ID {
		reply(w, 400, nil, "请输入完整恢复确认文字")
		return
	}
	if _, e := h.s.Job(r.Context(), in.ID); e != nil {
		reply(w, 404, nil, "备份不存在")
		return
	}
	j, e := h.s.Trigger(r.Context(), "restore", in.ID, actor, ip(r))
	if e != nil {
		fail(w, e)
		return
	}
	reply(w, 202, j, "")
}
func (h *handler) history(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.actor(w, r); !ok {
		return
	}
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	items, total, e := h.s.History(r.Context(), page)
	if e != nil {
		reply(w, 500, nil, "读取备份历史失败")
		return
	}
	var control Control
	h.s.db.First(&control, 1)
	next := ""
	ctx, cancel := context.WithTimeout(r.Context(), time.Second)
	defer cancel()
	if raw, e := exec.CommandContext(ctx, "/usr/bin/systemctl", "show", "ddyidc-backup.timer", "-p", "NextElapseUSecRealtime", "--value").Output(); e == nil {
		next = strings.TrimSpace(string(raw))
	}
	reply(w, 200, map[string]any{"items": items, "total": total, "activeId": control.ActiveID, "nextScheduledAt": next}, "")
}
func (h *handler) download(w http.ResponseWriter, r *http.Request) {
	actor, ok := h.actor(w, r)
	if !ok {
		return
	}
	j, e := h.s.Job(r.Context(), pathvar.Vars(r)["id"])
	if e != nil {
		reply(w, 404, nil, "备份不存在")
		return
	}
	path, e := h.s.Archive(r.Context(), j)
	if e != nil {
		fail(w, e)
		return
	}
	if filepath.Dir(path) == filepath.Join(h.s.Root, "backups/downloads") {
		defer os.Remove(path)
	}
	if e = h.s.db.Create(&Audit{ActorID: actor, Action: "download", JobID: j.ID, IP: ip(r)}).Error; e != nil {
		reply(w, 500, nil, "记录下载审计失败")
		return
	}
	w.Header().Set("Content-Type", "application/gzip")
	w.Header().Set("Content-Disposition", `attachment; filename="business-`+j.ID+`.tar.gz"`)
	w.Header().Set("Cache-Control", "no-store")
	http.ServeFile(w, r, path)
}
