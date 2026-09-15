package certification

import (
	"ddyidc/pkg/auth"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/zeromicro/go-zero/rest"
	"github.com/zeromicro/go-zero/rest/pathvar"
	"gorm.io/gorm"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

type handler struct {
	s   *Service
	jwt *auth.Manager
}

func Register(server *rest.Server, s *Service, jwt *auth.Manager) {
	h := &handler{s, jwt}
	routes := []rest.Route{}
	for _, prefix := range []string{"/console/v1/certification", "/api/v1/certification"} {
		for _, x := range []struct {
			method, path string
			fn           http.HandlerFunc
		}{{"GET", "/info", h.info}, {"POST", "/person", h.submit}, {"POST", "/company", h.submit}, {"POST", "/uploads", h.upload}, {"GET", "/files/:file", h.file}} {
			routes = append(routes, rest.Route{Method: x.method, Path: prefix + x.path, Handler: x.fn})
		}
	}
	for _, x := range []struct {
		method, path string
		fn           http.HandlerFunc
	}{{"GET", "", h.list}, {"POST", "/search", h.list}, {"GET", "/settings", h.settings}, {"PUT", "/settings", h.settings}, {"GET", "/:id", h.detail}, {"POST", "/:id/pass", h.review}, {"POST", "/:id/reject", h.review}, {"POST", "/:id/unlock", h.review}, {"GET", "/files/:file", h.file}} {
		routes = append(routes, rest.Route{Method: x.method, Path: "/admin/v1/certifications" + x.path, Handler: x.fn})
	}
	server.AddRoutes(routes, rest.WithMaxBytes(6<<20))
}
func reply(w http.ResponseWriter, code int, data any, message string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store, private")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]any{"code": map[bool]int{true: 0, false: 1}[code < 400], "data": data, "message": message})
}
func fail(w http.ResponseWriter, code int, message string) { reply(w, code, nil, message) }
func ip(r *http.Request) string {
	v, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return v
}
func originOK(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return r.Method == "GET"
	}
	u, err := url.Parse(origin)
	if err != nil || u.Scheme != "http" && u.Scheme != "https" {
		return false
	}
	if u.Host == r.Host {
		return true
	}
	host := strings.Split(r.Host, ":")[0]
	return (host == "localhost" || host == "127.0.0.1") && (u.Host == "127.0.0.1:13100" || u.Host == "localhost:3100" || u.Host == "127.0.0.1:3100")
}
func (h *handler) actor(w http.ResponseWriter, r *http.Request, admin bool) (*auth.Claims, bool) {
	if !originOK(r) {
		fail(w, 403, "请求来源不允许")
		return nil, false
	}
	kind := auth.SessionUser
	role := "customer"
	if admin {
		kind = auth.SessionAdmin
		role = "admin"
	}
	cookie, err := r.Cookie(h.jwt.CookieName(kind))
	if err != nil {
		fail(w, 401, "请先登录")
		return nil, false
	}
	claims, err := h.jwt.ParseAccessTokenFor(r.Context(), kind, cookie.Value)
	if err != nil || claims.Role != role {
		fail(w, 403, "没有访问权限")
		return nil, false
	}
	w.Header().Set("Cache-Control", "no-store, private")
	return claims, true
}
func (h *handler) limit(w http.ResponseWriter, r *http.Request, id int64, bucket string, max int64, seconds int) bool {
	if h.s.redis == nil {
		fail(w, 503, "安全服务暂不可用")
		return false
	}
	script := `local n=redis.call('INCR',KEYS[1]); if n==1 then redis.call('EXPIRE',KEYS[1],ARGV[1]); end; return n`
	for _, key := range []string{fmt.Sprintf("certification:%s:user:%d", bucket, id), "certification:" + bucket + ":ip:" + h.s.hash(ip(r))} {
		n, err := h.s.redis.Eval(r.Context(), script, []string{key}, seconds).Int64()
		if err != nil {
			fail(w, 503, "安全服务暂不可用")
			return false
		}
		if n > max {
			w.Header().Set("Retry-After", strconv.Itoa(seconds))
			fail(w, 429, "请求过于频繁，请稍后再试")
			return false
		}
	}
	return true
}
func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 32<<10))
	d.DisallowUnknownFields()
	if err := d.Decode(v); err != nil {
		fail(w, 400, "请求参数格式无效")
		return false
	}
	if err := d.Decode(&struct{}{}); err != io.EOF {
		fail(w, 400, "请求参数格式无效")
		return false
	}
	return true
}
func (h *handler) info(w http.ResponseWriter, r *http.Request) {
	a, ok := h.actor(w, r, false)
	if !ok {
		return
	}
	cfg, err := LoadSettings(h.s.db.WithContext(r.Context()))
	if err != nil {
		fail(w, 500, "读取实名设置失败")
		return
	}
	var row Record
	err = h.s.db.WithContext(r.Context()).Where("customer_id=?", a.UserID).Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		reply(w, 200, map[string]any{"settings": cfg, "status": 0, "record": nil}, "")
		return
	}
	if err != nil {
		fail(w, 500, "读取认证失败")
		return
	}
	masked, err := h.s.view(row, false)
	if err != nil {
		fail(w, 500, err.Error())
		return
	}
	reply(w, 200, map[string]any{"settings": cfg, "status": row.Status, "record": masked}, "")
}
func (h *handler) submit(w http.ResponseWriter, r *http.Request) {
	a, ok := h.actor(w, r, false)
	if !ok {
		return
	}
	if !h.limit(w, r, a.UserID, "submit", 5, 60) {
		return
	}
	var in Input
	if !decode(w, r, &in) {
		_ = audit(h.s.db, a.UserID, false, "submit_failed", a.UserID, map[string]any{"result": "invalid_request"}, ip(r))
		return
	}
	kind := 1
	if strings.HasSuffix(r.URL.Path, "/company") {
		kind = 2
	}
	row, err := h.s.Submit(r.Context(), a.UserID, kind, in, ip(r))
	if err != nil {
		_ = audit(h.s.db, a.UserID, false, "submit_failed", a.UserID, map[string]any{"result": "rejected"}, ip(r))
		fail(w, 400, err.Error())
		return
	}
	reply(w, 200, row, "")
}
func (h *handler) upload(w http.ResponseWriter, r *http.Request) {
	a, ok := h.actor(w, r, false)
	if !ok {
		return
	}
	if !h.limit(w, r, a.UserID, "upload", 5, 60) || !h.limit(w, r, a.UserID, "upload_daily", 20, 86400) {
		return
	}
	var n int64
	if err := h.s.db.Model(&Record{}).Where("customer_id=?", a.UserID).Count(&n).Error; err != nil {
		fail(w, 500, "读取认证失败")
		return
	}
	if n > 0 {
		fail(w, 400, ErrUsed.Error())
		return
	}
	cfg, err := LoadSettings(h.s.db)
	if err != nil || !cfg.Enabled {
		fail(w, 400, "实名认证暂未开放")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, (5<<20)+(64<<10))
	if err := r.ParseMultipartForm(5 << 20); err != nil {
		fail(w, 400, "上传文件无效或超过5MB")
		return
	}
	defer r.MultipartForm.RemoveAll()
	id, err := h.s.saveUpload(r, a.UserID)
	if err != nil {
		fail(w, 400, "仅支持有效的 JPG/PNG 图片，最大5MB、2500万像素")
		return
	}
	reply(w, 200, map[string]any{"id": id, "url": "/api/v1/certification/files/" + id}, "")
}
func (h *handler) file(w http.ResponseWriter, r *http.Request) {
	admin := strings.HasPrefix(r.URL.Path, "/admin/")
	a, ok := h.actor(w, r, admin)
	if !ok {
		return
	}
	fileID := pathvar.Vars(r)["file"]
	if admin {
		var count int64
		if err := h.s.db.Model(&Record{}).Where("id_card_front=? OR id_card_back=? OR company_license_img=? OR authorization=?", fileID, fileID, fileID, fileID).Count(&count).Error; err != nil || count == 0 {
			fail(w, 404, "文件不存在")
			return
		}
	}
	file, err := h.s.readUpload(fileID, a.UserID, admin)
	if err != nil {
		fail(w, 404, "文件不存在")
		return
	}
	if admin {
		if err := audit(h.s.db, a.UserID, true, "view_file", file.Owner, map[string]any{"fileId": fileID}, ip(r)); err != nil {
			fail(w, 500, "审计写入失败")
			return
		}
	}
	w.Header().Set("Content-Type", file.Type)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Security-Policy", "default-src 'none'")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Write(file.Data)
}
func (h *handler) settings(w http.ResponseWriter, r *http.Request) {
	a, ok := h.actor(w, r, true)
	if !ok {
		return
	}
	if r.Method == "GET" {
		cfg, err := LoadSettings(h.s.db)
		if err != nil {
			fail(w, 500, "读取设置失败")
			return
		}
		reply(w, 200, cfg, "")
		return
	}
	var cfg Settings
	if !decode(w, r, &cfg) {
		return
	}
	encoded, _ := json.Marshal(cfg)
	err := h.s.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Table("system.system_configs").Where("`key`='certification.settings'").Update("value", string(encoded)).Error; err != nil {
			return err
		}
		return audit(tx, a.UserID, true, "settings", 0, map[string]any{"settings": cfg}, ip(r))
	})
	if err != nil {
		fail(w, 500, "保存设置失败")
		return
	}
	reply(w, 200, cfg, "")
}
func (h *handler) review(w http.ResponseWriter, r *http.Request) {
	a, ok := h.actor(w, r, true)
	if !ok {
		return
	}
	id, err := strconv.ParseInt(pathvar.Vars(r)["id"], 10, 64)
	if err != nil || id <= 0 {
		fail(w, 400, "记录ID无效")
		return
	}
	var in struct {
		Reason string `json:"reason"`
	}
	if !decode(w, r, &in) {
		return
	}
	action := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
	if err := h.s.Review(r.Context(), id, a.UserID, action, in.Reason, ip(r)); err != nil {
		_ = audit(h.s.db, a.UserID, true, action+"_failed", 0, map[string]any{"recordId": id, "result": "rejected"}, ip(r))
		fail(w, 400, err.Error())
		return
	}
	reply(w, 200, map[string]bool{"ok": true}, "")
}
func (h *handler) detail(w http.ResponseWriter, r *http.Request) {
	a, ok := h.actor(w, r, true)
	if !ok {
		return
	}
	id, err := strconv.ParseInt(pathvar.Vars(r)["id"], 10, 64)
	if err != nil || id <= 0 {
		fail(w, 400, "记录ID无效")
		return
	}
	var row Record
	if err := h.s.db.First(&row, id).Error; err != nil {
		fail(w, 404, "认证记录不存在")
		return
	}
	full, err := h.s.view(row, true)
	if err != nil {
		fail(w, 500, err.Error())
		return
	}
	if err := audit(h.s.db, a.UserID, true, "view_detail", row.CustomerID, map[string]any{"recordId": id}, ip(r)); err != nil {
		fail(w, 500, "审计写入失败")
		return
	}
	var logs []struct {
		ID         int64           `json:"id,string"`
		OperatorID int64           `json:"operatorId,string"`
		Action     string          `json:"action"`
		Detail     json.RawMessage `json:"detail"`
		CreatedAt  time.Time       `json:"createdAt"`
	}
	h.s.db.Table("system.audit_logs").Select("id,operator_id,action,detail,created_at").Where("target_type='certification' AND target_id=?", row.CustomerID).Order("id DESC").Limit(50).Scan(&logs)
	reply(w, 200, map[string]any{"record": full, "audits": logs}, "")
}
func (h *handler) list(w http.ResponseWriter, r *http.Request) {
	_, ok := h.actor(w, r, true)
	if !ok {
		return
	}
	in := struct {
		Page       int    `json:"page"`
		PageSize   int    `json:"pageSize"`
		Statuses   []int  `json:"statuses"`
		Keyword    string `json:"keyword"`
		CustomerID string `json:"customerId"`
		Sort       string `json:"sort"`
	}{Page: 1, PageSize: 20}
	if r.Method == "POST" {
		if !decode(w, r, &in) {
			return
		}
	} else {
		in.Page, _ = strconv.Atoi(r.URL.Query().Get("page"))
		in.PageSize, _ = strconv.Atoi(r.URL.Query().Get("pageSize"))
		in.CustomerID = r.URL.Query().Get("customerId")
		in.Keyword = r.URL.Query().Get("keyword")
		for _, part := range strings.Split(r.URL.Query().Get("status"), ",") {
			if part != "" {
				status, _ := strconv.Atoi(part)
				in.Statuses = append(in.Statuses, status)
			}
		}
		r.URL.RawQuery = ""
	}
	if in.Page < 1 {
		in.Page = 1
	}
	if in.PageSize < 1 || in.PageSize > 100 {
		in.PageSize = 20
	}
	if !cleanText(in.Keyword, 128) {
		fail(w, 400, "搜索条件无效")
		return
	}
	q := h.s.db.Model(&Record{})
	if in.CustomerID != "" {
		q = q.Where("customer_id=?", in.CustomerID)
	}
	for _, status := range in.Statuses {
		if status < 1 || status > 3 {
			fail(w, 400, "状态无效")
			return
		}
	}
	if len(in.Statuses) > 0 {
		q = q.Where("status IN ?", in.Statuses)
	}
	if in.Keyword != "" {
		like := "%" + in.Keyword + "%"
		q = q.Where("name LIKE ? OR company_name LIKE ? OR document_hash=? OR company_hash=? OR customer_id IN (SELECT id FROM customer.customers WHERE username LIKE ?)", like, like, h.s.hash(in.Keyword), h.s.hash(in.Keyword), like)
	}
	var total int64
	if err := q.Count(&total).Error; err != nil {
		fail(w, 500, "查询失败")
		return
	}
	order := "id DESC"
	if in.Sort == "oldest" {
		order = "id ASC"
	}
	var rows []Record
	if err := q.Order(order).Offset((in.Page - 1) * in.PageSize).Limit(in.PageSize).Find(&rows).Error; err != nil {
		fail(w, 500, "查询失败")
		return
	}
	items := []map[string]any{}
	for _, row := range rows {
		masked, err := h.s.view(row, false)
		if err != nil {
			fail(w, 500, err.Error())
			return
		}
		var customer struct{ Username string }
		h.s.db.Table("customer.customers").Select("username").Where("id=?", row.CustomerID).Take(&customer)
		items = append(items, map[string]any{"record": masked, "username": customer.Username})
	}
	reply(w, 200, map[string]any{"items": items, "total": total}, "")
}
