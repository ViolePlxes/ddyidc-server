package groupbuy

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"ddyidc/pkg/auth"
	"github.com/zeromicro/go-zero/rest"
	"github.com/zeromicro/go-zero/rest/pathvar"
)

type Handler struct {
	service *Service
	jwt     *auth.Manager
}

func RegisterHandlers(server *rest.Server, service *Service, jwt *auth.Manager) {
	h := &Handler{service: service, jwt: jwt}
	server.AddRoutes([]rest.Route{
		{Method: http.MethodGet, Path: "/api/v1/groupbuy/public/activities", Handler: h.publicActivities},
		{Method: http.MethodGet, Path: "/api/v1/groupbuy/public/activities/:id", Handler: h.publicActivity},
		{Method: http.MethodGet, Path: "/api/v1/groupbuy/public/share/:inviteCode", Handler: h.share},
		{Method: http.MethodGet, Path: "/api/v1/groupbuy/activities", Handler: h.userActivities},
		{Method: http.MethodGet, Path: "/api/v1/groupbuy/activities/:id", Handler: h.userActivity},
		{Method: http.MethodGet, Path: "/api/v1/groupbuy/groups", Handler: h.userGroups},
		{Method: http.MethodPost, Path: "/api/v1/groupbuy/groups", Handler: h.start},
		{Method: http.MethodPost, Path: "/api/v1/groupbuy/groups/:id/join", Handler: h.join},
		{Method: http.MethodGet, Path: "/api/v1/groupbuy/my", Handler: h.my},
		{Method: http.MethodGet, Path: "/api/v1/groupbuy/my/:id", Handler: h.myDetail},
		{Method: http.MethodPost, Path: "/api/v1/groupbuy/my/:id/refund", Handler: h.userRefund},
		{Method: http.MethodPut, Path: "/api/v1/groupbuy/my/:id/toggle-public", Handler: h.togglePublic},
		{Method: http.MethodGet, Path: "/api/v1/groupbuy/share/:inviteCode", Handler: h.share},
		{Method: http.MethodGet, Path: "/api/v1/groupbuy/public-groups", Handler: h.groups},
		{Method: http.MethodGet, Path: "/api/v1/groupbuy/tab-counts", Handler: h.tabCounts},
		{Method: http.MethodGet, Path: "/admin/v1/groupbuy/activities", Handler: h.adminActivities},
		{Method: http.MethodGet, Path: "/admin/v1/groupbuy/activities/:id", Handler: h.adminActivity},
		{Method: http.MethodPost, Path: "/admin/v1/groupbuy/activities", Handler: h.createActivity},
		{Method: http.MethodPut, Path: "/admin/v1/groupbuy/activities/:id", Handler: h.updateActivity},
		{Method: http.MethodDelete, Path: "/admin/v1/groupbuy/activities/:id", Handler: h.deleteActivity},
		{Method: http.MethodPut, Path: "/admin/v1/groupbuy/activities/:id/status", Handler: h.activityStatus},
		{Method: http.MethodPut, Path: "/admin/v1/groupbuy/activities/order", Handler: h.activityOrder},
		{Method: http.MethodGet, Path: "/admin/v1/groupbuy/leaders", Handler: h.leaders},
		{Method: http.MethodGet, Path: "/admin/v1/groupbuy/leaders/:id/detail", Handler: h.leaderDetail},
		{Method: http.MethodPost, Path: "/admin/v1/groupbuy/members/:id/refund", Handler: h.adminRefund},
		{Method: http.MethodPut, Path: "/admin/v1/groupbuy/leaders/:id/force-public", Handler: h.forcePublic},
		{Method: http.MethodGet, Path: "/admin/v1/groupbuy/settings", Handler: h.settings},
		{Method: http.MethodPut, Path: "/admin/v1/groupbuy/settings", Handler: h.saveSettings},
	})
}

func writeOK(w http.ResponseWriter, data any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": data})
}
func writeFail(w http.ResponseWriter, status int, err error) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"code": 1, "message": err.Error()})
}
func decode(w http.ResponseWriter, r *http.Request, to any) bool {
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 2<<20)).Decode(to); err != nil {
		writeFail(w, http.StatusBadRequest, errors.New("请求参数格式错误"))
		return false
	}
	return true
}
func pathInt64(w http.ResponseWriter, r *http.Request, name string) (int64, bool) {
	value, err := strconv.ParseInt(strings.Trim(pathvar.Vars(r)[name], "/"), 10, 64)
	if err != nil || value <= 0 {
		writeFail(w, http.StatusBadRequest, errors.New("路径参数错误"))
		return 0, false
	}
	return value, true
}
func queryInt64(r *http.Request, name string) *int64 {
	value, err := strconv.ParseInt(strings.TrimSpace(r.URL.Query().Get(name)), 10, 64)
	if err != nil || value <= 0 {
		return nil
	}
	return &value
}
func queryStatus(r *http.Request) *uint8 {
	value, err := strconv.ParseUint(strings.TrimSpace(r.URL.Query().Get("status")), 10, 8)
	if err != nil {
		return nil
	}
	out := uint8(value)
	return &out
}
func pages(r *http.Request) (int, int) {
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	size, _ := strconv.Atoi(r.URL.Query().Get("pageSize"))
	return normalizePage(page, size)
}
func (h *Handler) claims(w http.ResponseWriter, r *http.Request, role string) (int64, bool) {
	header := r.Header.Get("Authorization")
	if !strings.HasPrefix(header, "Bearer ") {
		writeFail(w, http.StatusUnauthorized, errors.New("登录已失效，请重新登录"))
		return 0, false
	}
	claims, err := h.jwt.ParseAccessToken(strings.TrimPrefix(header, "Bearer "))
	if err != nil || claims.Role != role {
		writeFail(w, http.StatusForbidden, errors.New("没有访问权限"))
		return 0, false
	}
	return claims.UserID, true
}

func (h *Handler) publicActivities(w http.ResponseWriter, r *http.Request) {
	p, z := pages(r)
	rows, total, err := h.service.ListActivities(r.Context(), r.URL.Query().Get("keyword"), nil, p, z, true)
	if err != nil {
		writeFail(w, 500, err)
		return
	}
	writeOK(w, map[string]any{"list": rows, "total": total, "page": p, "pageSize": z})
}
func (h *Handler) publicActivity(w http.ResponseWriter, r *http.Request) {
	id, ok := pathInt64(w, r, "id")
	if !ok {
		return
	}
	row, err := h.service.Activity(r.Context(), id, true)
	if err != nil {
		writeFail(w, 404, err)
		return
	}
	writeOK(w, row)
}
func (h *Handler) userActivities(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.claims(w, r, "customer"); !ok {
		return
	}
	h.publicActivities(w, r)
}
func (h *Handler) userActivity(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.claims(w, r, "customer"); !ok {
		return
	}
	h.publicActivity(w, r)
}
func (h *Handler) share(w http.ResponseWriter, r *http.Request) {
	row, err := h.service.Share(r.Context(), pathvar.Vars(r)["inviteCode"])
	if err != nil {
		writeFail(w, 404, err)
		return
	}
	writeOK(w, row)
}
func (h *Handler) groups(w http.ResponseWriter, r *http.Request) {
	p, z := pages(r)
	rows, total, err := h.service.PublicGroups(r.Context(), queryInt64(r, "activityId"), p, z)
	if err != nil {
		writeFail(w, 500, err)
		return
	}
	writeOK(w, map[string]any{"list": rows, "total": total, "page": p, "pageSize": z})
}

func (h *Handler) userGroups(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.claims(w, r, "customer"); !ok {
		return
	}
	h.groups(w, r)
}

func (h *Handler) start(w http.ResponseWriter, r *http.Request) {
	client, ok := h.claims(w, r, "customer")
	if !ok {
		return
	}
	var input struct {
		ActivityID        string  `json:"activityId"`
		GroupType         string  `json:"groupType"`
		IsPublic          bool    `json:"isPublic"`
		RecommendIdentify string  `json:"recommendIdentify"`
		RecommendClientID *string `json:"recommendClientId"`
	}
	if !decode(w, r, &input) {
		return
	}
	activityID, err := strconv.ParseInt(input.ActivityID, 10, 64)
	if err != nil || activityID <= 0 {
		writeFail(w, 400, errors.New("活动ID无效"))
		return
	}
	var recommendID *int64
	if input.RecommendClientID != nil {
		v, e := strconv.ParseInt(*input.RecommendClientID, 10, 64)
		if e == nil && v > 0 {
			recommendID = &v
		}
	}
	row, err := h.service.StartGroup(r.Context(), client, StartInput{ActivityID: activityID, GroupType: input.GroupType, IsPublic: input.IsPublic, RecommendIdentify: input.RecommendIdentify, RecommendClientID: recommendID})
	if err != nil {
		writeFail(w, 400, err)
		return
	}
	writeOK(w, map[string]any{"groupId": strconv.FormatInt(row.ID, 10), "inviteCode": row.InviteCode, "sharePath": "/groupbuy/share/" + row.InviteCode})
}
func (h *Handler) join(w http.ResponseWriter, r *http.Request) {
	client, ok := h.claims(w, r, "customer")
	if !ok {
		return
	}
	leaderID, ok := pathInt64(w, r, "id")
	if !ok {
		return
	}
	var input struct {
		RecommendIdentify string  `json:"recommendIdentify"`
		RecommendClientID *string `json:"recommendClientId"`
	}
	if r.ContentLength > 0 && !decode(w, r, &input) {
		return
	}
	var recommendID *int64
	if input.RecommendClientID != nil {
		v, e := strconv.ParseInt(*input.RecommendClientID, 10, 64)
		if e == nil && v > 0 {
			recommendID = &v
		}
	}
	row, err := h.service.JoinGroup(r.Context(), client, leaderID, input.RecommendIdentify, recommendID)
	if err != nil {
		writeFail(w, 400, err)
		return
	}
	writeOK(w, row)
}
func (h *Handler) my(w http.ResponseWriter, r *http.Request) {
	client, ok := h.claims(w, r, "customer")
	if !ok {
		return
	}
	p, z := pages(r)
	rows, total, err := h.service.MyGroups(r.Context(), client, queryStatus(r), p, z)
	if err != nil {
		writeFail(w, 500, err)
		return
	}
	writeOK(w, map[string]any{"list": rows, "total": total, "page": p, "pageSize": z})
}
func (h *Handler) myDetail(w http.ResponseWriter, r *http.Request) {
	client, ok := h.claims(w, r, "customer")
	if !ok {
		return
	}
	id, ok := pathInt64(w, r, "id")
	if !ok {
		return
	}
	row, err := h.service.Group(r.Context(), id, client, false)
	if err != nil {
		writeFail(w, 404, err)
		return
	}
	writeOK(w, row)
}
func (h *Handler) userRefund(w http.ResponseWriter, r *http.Request) {
	client, ok := h.claims(w, r, "customer")
	if !ok {
		return
	}
	id, ok := pathInt64(w, r, "id")
	if !ok {
		return
	}
	detail, err := h.service.Group(r.Context(), id, client, false)
	if err != nil {
		writeFail(w, 404, err)
		return
	}
	var selected Record
	for _, member := range detail.Members {
		if member.ID == id {
			selected = member
			break
		}
	}
	if selected.ClientID != client {
		writeFail(w, 403, errors.New("只能退款自己的参团记录"))
		return
	}
	if err = h.service.Refund(r.Context(), id, selected.IsLeader); err != nil {
		writeFail(w, 400, err)
		return
	}
	writeOK(w, map[string]bool{"refunded": true})
}
func (h *Handler) togglePublic(w http.ResponseWriter, r *http.Request) {
	client, ok := h.claims(w, r, "customer")
	if !ok {
		return
	}
	id, ok := pathInt64(w, r, "id")
	if !ok {
		return
	}
	var input struct {
		IsPublic bool `json:"isPublic"`
	}
	if !decode(w, r, &input) {
		return
	}
	if err := h.service.TogglePublic(r.Context(), client, id, input.IsPublic); err != nil {
		writeFail(w, 400, err)
		return
	}
	writeOK(w, map[string]bool{"updated": true})
}
func (h *Handler) tabCounts(w http.ResponseWriter, r *http.Request) {
	client, ok := h.claims(w, r, "customer")
	if !ok {
		return
	}
	row, err := h.service.TabCounts(r.Context(), client)
	if err != nil {
		writeFail(w, 500, err)
		return
	}
	writeOK(w, row)
}

func (h *Handler) adminActivities(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.claims(w, r, "admin"); !ok {
		return
	}
	p, z := pages(r)
	rows, total, err := h.service.ListActivities(r.Context(), r.URL.Query().Get("keyword"), queryStatus(r), p, z, false)
	if err != nil {
		writeFail(w, 500, err)
		return
	}
	writeOK(w, map[string]any{"list": rows, "total": total, "page": p, "pageSize": z})
}
func (h *Handler) adminActivity(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.claims(w, r, "admin"); !ok {
		return
	}
	id, ok := pathInt64(w, r, "id")
	if !ok {
		return
	}
	row, err := h.service.Activity(r.Context(), id, false)
	if err != nil {
		writeFail(w, 404, err)
		return
	}
	writeOK(w, row)
}
func (h *Handler) createActivity(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.claims(w, r, "admin"); !ok {
		return
	}
	var row Activity
	if !decode(w, r, &row) {
		return
	}
	row.ID = 0
	if err := h.service.SaveActivity(r.Context(), &row); err != nil {
		writeFail(w, 400, err)
		return
	}
	writeOK(w, &row)
}
func (h *Handler) updateActivity(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.claims(w, r, "admin"); !ok {
		return
	}
	id, ok := pathInt64(w, r, "id")
	if !ok {
		return
	}
	var row Activity
	if !decode(w, r, &row) {
		return
	}
	row.ID = id
	if err := h.service.SaveActivity(r.Context(), &row); err != nil {
		writeFail(w, 400, err)
		return
	}
	writeOK(w, &row)
}
func (h *Handler) deleteActivity(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.claims(w, r, "admin"); !ok {
		return
	}
	id, ok := pathInt64(w, r, "id")
	if !ok {
		return
	}
	if err := h.service.DeleteActivity(r.Context(), id); err != nil {
		writeFail(w, 400, err)
		return
	}
	writeOK(w, map[string]bool{"deleted": true})
}
func (h *Handler) activityStatus(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.claims(w, r, "admin"); !ok {
		return
	}
	id, ok := pathInt64(w, r, "id")
	if !ok {
		return
	}
	var input struct {
		Status uint8 `json:"status"`
	}
	if !decode(w, r, &input) {
		return
	}
	if err := h.service.SetActivityStatus(r.Context(), id, input.Status); err != nil {
		writeFail(w, 400, err)
		return
	}
	writeOK(w, map[string]bool{"updated": true})
}
func (h *Handler) activityOrder(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.claims(w, r, "admin"); !ok {
		return
	}
	var input struct {
		IDs []string `json:"ids"`
	}
	if !decode(w, r, &input) {
		return
	}
	ids := make([]int64, 0, len(input.IDs))
	for _, raw := range input.IDs {
		v, e := strconv.ParseInt(raw, 10, 64)
		if e != nil {
			writeFail(w, 400, errors.New("排序活动ID无效"))
			return
		}
		ids = append(ids, v)
	}
	if err := h.service.SortActivities(r.Context(), ids); err != nil {
		writeFail(w, 400, err)
		return
	}
	writeOK(w, map[string]bool{"updated": true})
}
func (h *Handler) leaders(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.claims(w, r, "admin"); !ok {
		return
	}
	p, z := pages(r)
	rows, total, err := h.service.ListLeaders(r.Context(), queryInt64(r, "activityId"), queryStatus(r), r.URL.Query().Get("keyword"), p, z)
	if err != nil {
		writeFail(w, 500, err)
		return
	}
	writeOK(w, map[string]any{"list": rows, "total": total, "page": p, "pageSize": z})
}
func (h *Handler) leaderDetail(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.claims(w, r, "admin"); !ok {
		return
	}
	id, ok := pathInt64(w, r, "id")
	if !ok {
		return
	}
	row, err := h.service.Group(r.Context(), id, 0, true)
	if err != nil {
		writeFail(w, 404, err)
		return
	}
	writeOK(w, row)
}
func (h *Handler) adminRefund(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.claims(w, r, "admin"); !ok {
		return
	}
	id, ok := pathInt64(w, r, "id")
	if !ok {
		return
	}
	var row Record
	if err := h.service.db.WithContext(r.Context()).Table(recordTable).Where("id=?", id).Take(&row).Error; err != nil {
		writeFail(w, 404, ErrNotFound)
		return
	}
	if err := h.service.Refund(r.Context(), id, row.IsLeader); err != nil {
		writeFail(w, 400, err)
		return
	}
	writeOK(w, map[string]bool{"refunded": true})
}
func (h *Handler) forcePublic(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.claims(w, r, "admin"); !ok {
		return
	}
	id, ok := pathInt64(w, r, "id")
	if !ok {
		return
	}
	var input struct {
		Closed bool `json:"closed"`
	}
	if !decode(w, r, &input) {
		return
	}
	if err := h.service.ForceClosePublic(r.Context(), id, input.Closed); err != nil {
		writeFail(w, 400, err)
		return
	}
	writeOK(w, map[string]bool{"updated": true})
}
func (h *Handler) settings(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.claims(w, r, "admin"); !ok {
		return
	}
	row, err := h.service.Settings(r.Context())
	if err != nil {
		writeFail(w, 500, err)
		return
	}
	writeOK(w, row)
}
func (h *Handler) saveSettings(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.claims(w, r, "admin"); !ok {
		return
	}
	var row Settings
	if !decode(w, r, &row) {
		return
	}
	if err := h.service.SaveSettings(r.Context(), &row); err != nil {
		writeFail(w, 400, err)
		return
	}
	writeOK(w, &row)
}
