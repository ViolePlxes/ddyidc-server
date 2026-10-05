package ticket

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	dom "ddyidc/internal/ticket/domain"
	repo "ddyidc/internal/ticket/repository"
	svc "ddyidc/internal/ticket/service"
	types "ddyidc/internal/ticket/types"
	"ddyidc/pkg/auth"
	"ddyidc/pkg/clientip"
	"github.com/google/uuid"
	"github.com/zeromicro/go-zero/rest"
	"github.com/zeromicro/go-zero/rest/pathvar"
	"time"
)

type handler struct {
	service     *svc.Service
	jwt         *auth.Manager
	storageRoot string
}

func RegisterHandlers(server *rest.Server, service *svc.Service, jwt *auth.Manager, root string) {
	if !filepath.IsAbs(root) {
		panic("ticket storage must be absolute")
	}
	if err := os.MkdirAll(root, 0750); err != nil {
		panic(err)
	}
	h := &handler{service: service, jwt: jwt, storageRoot: root}
	server.AddRoutes([]rest.Route{
		{Method: http.MethodGet, Path: "/api/v1/tickets", Handler: h.userList},
		{Method: http.MethodGet, Path: "/api/v1/tickets/statistic", Handler: h.statistic},
		{Method: http.MethodGet, Path: "/api/v1/tickets/types", Handler: h.userTypes},
		{Method: http.MethodGet, Path: "/api/v1/tickets/mine/products", Handler: h.products},
		{Method: http.MethodPost, Path: "/api/v1/tickets", Handler: h.create},
		{Method: http.MethodGet, Path: "/api/v1/tickets/unread", Handler: h.userUnread},
		{Method: http.MethodGet, Path: "/api/v1/tickets/notifications", Handler: h.userNotifications},
		{Method: http.MethodPost, Path: "/api/v1/tickets/attachments", Handler: h.userUpload},
		{Method: http.MethodGet, Path: "/api/v1/tickets/:id", Handler: h.userDetail},
		{Method: http.MethodGet, Path: "/api/v1/tickets/:id/replies", Handler: h.userReplies},
		{Method: http.MethodPost, Path: "/api/v1/tickets/:id/replies", Handler: h.userReply},
		{Method: http.MethodPut, Path: "/api/v1/tickets/:id/urge", Handler: h.urge},
		{Method: http.MethodPut, Path: "/api/v1/tickets/:id/close", Handler: h.userClose},
		{Method: http.MethodGet, Path: "/api/v1/tickets/:id/attachments/:file", Handler: h.userDownload},
		{Method: http.MethodGet, Path: "/admin/v1/tickets", Handler: h.adminList},
		{Method: http.MethodGet, Path: "/admin/v1/tickets/unread", Handler: h.adminUnread},
		{Method: http.MethodGet, Path: "/admin/v1/tickets/notifications", Handler: h.adminNotifications},
		{Method: http.MethodGet, Path: "/admin/v1/tickets/admins", Handler: h.admins},
		{Method: http.MethodPost, Path: "/admin/v1/tickets/attachments", Handler: h.adminUpload},
		{Method: http.MethodGet, Path: "/admin/v1/tickets/:id", Handler: h.adminDetail},
		{Method: http.MethodGet, Path: "/admin/v1/tickets/:id/replies", Handler: h.adminReplies},
		{Method: http.MethodPost, Path: "/admin/v1/tickets/:id/replies", Handler: h.adminReply},
		{Method: http.MethodPut, Path: "/admin/v1/tickets/:id/receive", Handler: h.receive},
		{Method: http.MethodPut, Path: "/admin/v1/tickets/:id/resolved", Handler: h.resolve},
		{Method: http.MethodPut, Path: "/admin/v1/tickets/:id/close", Handler: h.adminClose},
		{Method: http.MethodPut, Path: "/admin/v1/tickets/:id/reopen", Handler: h.reopen},
		{Method: http.MethodPost, Path: "/admin/v1/tickets/:id/forward", Handler: h.forward},
		{Method: http.MethodGet, Path: "/admin/v1/tickets/:id/log", Handler: h.logs},
		{Method: http.MethodGet, Path: "/admin/v1/tickets/:id/attachments/:file", Handler: h.adminDownload},
		{Method: http.MethodGet, Path: "/admin/v1/tickets/types", Handler: h.adminTypes},
		{Method: http.MethodPost, Path: "/admin/v1/tickets/types", Handler: h.createType},
		{Method: http.MethodPut, Path: "/admin/v1/tickets/types/:configId", Handler: h.updateType},
		{Method: http.MethodDelete, Path: "/admin/v1/tickets/types/:configId", Handler: h.deleteType},
		{Method: http.MethodGet, Path: "/admin/v1/tickets/statuses", Handler: h.adminStatuses},
		{Method: http.MethodPost, Path: "/admin/v1/tickets/statuses", Handler: h.createStatus},
		{Method: http.MethodPut, Path: "/admin/v1/tickets/statuses/:configId", Handler: h.updateStatus},
		{Method: http.MethodDelete, Path: "/admin/v1/tickets/statuses/:configId", Handler: h.deleteStatus},
		{Method: http.MethodGet, Path: "/admin/v1/tickets/prereplies", Handler: h.adminPrereplies},
		{Method: http.MethodPost, Path: "/admin/v1/tickets/prereplies", Handler: h.createPrereply},
		{Method: http.MethodPut, Path: "/admin/v1/tickets/prereplies/order", Handler: h.orderPrereplies},
		{Method: http.MethodPut, Path: "/admin/v1/tickets/prereplies/:configId", Handler: h.updatePrereply},
		{Method: http.MethodDelete, Path: "/admin/v1/tickets/prereplies/:configId", Handler: h.deletePrereply},
		{Method: http.MethodPost, Path: "/admin/v1/tickets/:id/notes", Handler: h.addNote},
		{Method: http.MethodPut, Path: "/admin/v1/tickets/:id/notes/:noteId", Handler: h.updateNote},
		{Method: http.MethodDelete, Path: "/admin/v1/tickets/:id/notes/:noteId", Handler: h.deleteNote},
	})
}

func writeJSON(w http.ResponseWriter, data any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": data})
}
func writeError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"code": 1, "message": message})
}
func writeServiceError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, svc.ErrReopenInput):
		writeError(w, http.StatusBadRequest, err.Error())
	case errors.Is(err, svc.ErrForbidden):
		writeError(w, http.StatusForbidden, err.Error())
	case errors.Is(err, svc.ErrNotFound):
		writeError(w, http.StatusNotFound, err.Error())
	case errors.Is(err, svc.ErrClosed), errors.Is(err, svc.ErrRevision):
		writeError(w, http.StatusConflict, err.Error())
	default:
		writeError(w, http.StatusUnprocessableEntity, err.Error())
	}
}
func decode(w http.ResponseWriter, r *http.Request, to any) bool {
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(to); err != nil {
		writeError(w, http.StatusBadRequest, "请求参数格式错误")
		return false
	}
	return true
}
func pathID(w http.ResponseWriter, r *http.Request, key string) int64 {
	raw := strings.Trim(pathvar.Vars(r)[key], "/")
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || id <= 0 {
		writeError(w, http.StatusBadRequest, "路径参数错误")
		return 0
	}
	return id
}
func pageParams(r *http.Request) (int, int) {
	page, pageSize := 1, 20
	if v, e := strconv.Atoi(r.URL.Query().Get("page")); e == nil && v > 0 {
		page = v
	}
	if v, e := strconv.Atoi(r.URL.Query().Get("pageSize")); e == nil && v > 0 && v <= 100 {
		pageSize = v
	}
	return page, pageSize
}
func optionalID(r *http.Request, key string) *int64 {
	raw := strings.TrimSpace(r.URL.Query().Get(key))
	if raw == "" {
		return nil
	}
	v, e := strconv.ParseInt(raw, 10, 64)
	if e != nil || v <= 0 {
		return nil
	}
	return &v
}
func sinceReply(r *http.Request) int64 {
	v, _ := strconv.ParseInt(strings.TrimSpace(r.URL.Query().Get("since")), 10, 64)
	return v
}

func (h *handler) claims(w http.ResponseWriter, r *http.Request, kind auth.SessionKind, role string) (int64, bool) {
	header := r.Header.Get("Authorization")
	if !strings.HasPrefix(header, "Bearer ") {
		writeError(w, http.StatusUnauthorized, "登录已失效，请重新登录")
		return 0, false
	}
	claims, err := h.jwt.ParseAccessTokenFor(r.Context(), kind, strings.TrimPrefix(header, "Bearer "))
	if err != nil || claims.Role != role {
		writeError(w, http.StatusForbidden, "没有访问权限")
		return 0, false
	}
	return claims.UserID, true
}
func (h *handler) user(w http.ResponseWriter, r *http.Request) (int64, bool) {
	return h.claims(w, r, auth.SessionUser, "customer")
}
func (h *handler) admin(w http.ResponseWriter, r *http.Request) (int64, bool) {
	return h.claims(w, r, auth.SessionAdmin, "admin")
}

func (h *handler) userList(w http.ResponseWriter, r *http.Request) {
	id, ok := h.user(w, r)
	if !ok {
		return
	}
	page, size := pageParams(r)
	result, err := h.service.List(r.Context(), dom.ActorClient, id, page, size, optionalID(r, "status"), nil, "", r.URL.Query().Get("since"))
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeJSON(w, result)
}
func (h *handler) adminList(w http.ResponseWriter, r *http.Request) {
	id, ok := h.admin(w, r)
	if !ok {
		return
	}
	page, size := pageParams(r)
	result, err := h.service.List(r.Context(), dom.ActorAdmin, id, page, size, optionalID(r, "status"), optionalID(r, "typeId"), r.URL.Query().Get("keyword"), r.URL.Query().Get("since"))
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeJSON(w, result)
}
func (h *handler) create(w http.ResponseWriter, r *http.Request) {
	id, ok := h.user(w, r)
	if !ok {
		return
	}
	var input types.CreateInput
	if !decode(w, r, &input) {
		return
	}
	result, err := h.service.Create(r.Context(), id, input)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeJSON(w, result)
}
func (h *handler) statistic(w http.ResponseWriter, r *http.Request) {
	id, ok := h.user(w, r)
	if !ok {
		return
	}
	result, err := h.service.Statistic(r.Context(), id)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeJSON(w, result)
}
func (h *handler) products(w http.ResponseWriter, r *http.Request) {
	id, ok := h.user(w, r)
	if !ok {
		return
	}
	result, err := h.service.Products(r.Context(), id)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeJSON(w, map[string]any{"list": result})
}
func (h *handler) userDetail(w http.ResponseWriter, r *http.Request) {
	id, ok := h.user(w, r)
	if !ok {
		return
	}
	ticketID := pathID(w, r, "id")
	if ticketID == 0 {
		return
	}
	result, err := h.service.Detail(r.Context(), dom.ActorClient, id, ticketID)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeJSON(w, result)
}
func (h *handler) adminDetail(w http.ResponseWriter, r *http.Request) {
	id, ok := h.admin(w, r)
	if !ok {
		return
	}
	ticketID := pathID(w, r, "id")
	if ticketID == 0 {
		return
	}
	result, err := h.service.Detail(r.Context(), dom.ActorAdmin, id, ticketID)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeJSON(w, result)
}
func (h *handler) userReplies(w http.ResponseWriter, r *http.Request) {
	id, ok := h.user(w, r)
	if !ok {
		return
	}
	ticketID := pathID(w, r, "id")
	if ticketID == 0 {
		return
	}
	result, err := h.service.Replies(r.Context(), dom.ActorClient, id, ticketID, sinceReply(r))
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeJSON(w, map[string]any{"list": result})
}
func (h *handler) userReply(w http.ResponseWriter, r *http.Request) { h.reply(w, r, dom.ActorClient) }
func (h *handler) adminReplies(w http.ResponseWriter, r *http.Request) {
	id, ok := h.admin(w, r)
	if !ok {
		return
	}
	ticketID := pathID(w, r, "id")
	if ticketID == 0 {
		return
	}
	result, err := h.service.Replies(r.Context(), dom.ActorAdmin, id, ticketID, sinceReply(r))
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeJSON(w, map[string]any{"list": result})
}
func (h *handler) adminReply(w http.ResponseWriter, r *http.Request) { h.reply(w, r, dom.ActorAdmin) }
func (h *handler) reply(w http.ResponseWriter, r *http.Request, actorType string) {
	var id int64
	var ok bool
	if actorType == dom.ActorClient {
		id, ok = h.user(w, r)
	} else {
		id, ok = h.admin(w, r)
	}
	if !ok {
		return
	}
	ticketID := pathID(w, r, "id")
	if ticketID == 0 {
		return
	}
	var input types.ReplyInput
	if !decode(w, r, &input) {
		return
	}
	result, err := h.service.Reply(r.Context(), actorType, id, ticketID, input)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeJSON(w, result)
}
func (h *handler) urge(w http.ResponseWriter, r *http.Request) {
	id, ok := h.user(w, r)
	if !ok {
		return
	}
	ticketID := pathID(w, r, "id")
	if ticketID == 0 {
		return
	}
	if err := h.service.Urge(r.Context(), id, ticketID); err != nil {
		writeServiceError(w, err)
		return
	}
	writeJSON(w, map[string]bool{"urged": true})
}
func (h *handler) userClose(w http.ResponseWriter, r *http.Request)  { h.close(w, r, dom.ActorClient) }
func (h *handler) adminClose(w http.ResponseWriter, r *http.Request) { h.close(w, r, dom.ActorAdmin) }
func (h *handler) close(w http.ResponseWriter, r *http.Request, actorType string) {
	var id int64
	var ok bool
	if actorType == dom.ActorClient {
		id, ok = h.user(w, r)
	} else {
		id, ok = h.admin(w, r)
	}
	if !ok {
		return
	}
	ticketID := pathID(w, r, "id")
	if ticketID == 0 {
		return
	}
	if err := h.service.Close(r.Context(), actorType, id, ticketID); err != nil {
		writeServiceError(w, err)
		return
	}
	writeJSON(w, map[string]bool{"closed": true})
}
func (h *handler) receive(w http.ResponseWriter, r *http.Request) {
	id, ok := h.admin(w, r)
	if !ok {
		return
	}
	ticketID := pathID(w, r, "id")
	if ticketID == 0 {
		return
	}
	if err := h.service.Receive(r.Context(), id, ticketID); err != nil {
		writeServiceError(w, err)
		return
	}
	writeJSON(w, map[string]bool{"received": true})
}
func (h *handler) resolve(w http.ResponseWriter, r *http.Request) {
	id, ok := h.admin(w, r)
	if !ok {
		return
	}
	ticketID := pathID(w, r, "id")
	if ticketID == 0 {
		return
	}
	if err := h.service.Resolve(r.Context(), id, ticketID); err != nil {
		writeServiceError(w, err)
		return
	}
	writeJSON(w, map[string]bool{"resolved": true})
}
func (h *handler) reopen(w http.ResponseWriter, r *http.Request) {
	admin, ok := h.admin(w, r)
	if !ok {
		return
	}
	id := pathID(w, r, "id")
	if id == 0 {
		return
	}
	var in struct {
		Reason, RequestID string
		ExpectedUpdatedAt time.Time
	}
	if !decode(w, r, &in) {
		return
	}
	if err := h.service.Reopen(r.Context(), admin, id, in.Reason, in.RequestID, clientip.FromRequest(r), in.ExpectedUpdatedAt); err != nil {
		writeServiceError(w, err)
		return
	}
	writeJSON(w, map[string]bool{"reopened": true})
}
func (h *handler) forward(w http.ResponseWriter, r *http.Request) {
	id, ok := h.admin(w, r)
	if !ok {
		return
	}
	ticketID := pathID(w, r, "id")
	if ticketID == 0 {
		return
	}
	var input types.ForwardInput
	if !decode(w, r, &input) {
		return
	}
	if err := h.service.Forward(r.Context(), id, ticketID, input); err != nil {
		writeServiceError(w, err)
		return
	}
	writeJSON(w, map[string]bool{"forwarded": true})
}
func (h *handler) logs(w http.ResponseWriter, r *http.Request) {
	id, ok := h.admin(w, r)
	if !ok {
		return
	}
	ticketID := pathID(w, r, "id")
	if ticketID == 0 {
		return
	}
	rows, err := h.service.Logs(r.Context(), id, ticketID)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeJSON(w, map[string]any{"list": rows})
}

func (h *handler) userTypes(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.user(w, r); !ok {
		return
	}
	h.types(w, r)
}
func (h *handler) adminTypes(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.admin(w, r); !ok {
		return
	}
	h.types(w, r)
}
func (h *handler) types(w http.ResponseWriter, r *http.Request) {
	rows, err := h.service.Types(r.Context())
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeJSON(w, map[string]any{"list": rows})
}
func (h *handler) adminStatuses(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.admin(w, r); !ok {
		return
	}
	rows, err := h.service.Statuses(r.Context())
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeJSON(w, map[string]any{"list": rows})
}
func (h *handler) adminPrereplies(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.admin(w, r); !ok {
		return
	}
	rows, err := h.service.Prereplies(r.Context())
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeJSON(w, map[string]any{"list": rows})
}
func (h *handler) admins(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.admin(w, r); !ok {
		return
	}
	rows, err := h.service.Admins(r.Context())
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeJSON(w, map[string]any{"list": rows})
}
func (h *handler) userUnread(w http.ResponseWriter, r *http.Request) {
	id, ok := h.user(w, r)
	if !ok {
		return
	}
	count, err := h.service.Unread(r.Context(), dom.ActorClient, id)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeJSON(w, map[string]int64{"unreadCount": count})
}
func (h *handler) adminUnread(w http.ResponseWriter, r *http.Request) {
	id, ok := h.admin(w, r)
	if !ok {
		return
	}
	count, err := h.service.Unread(r.Context(), dom.ActorAdmin, id)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeJSON(w, map[string]int64{"unreadCount": count})
}
func (h *handler) userNotifications(w http.ResponseWriter, r *http.Request) {
	id, ok := h.user(w, r)
	if !ok {
		return
	}
	result, err := h.service.Notifications(r.Context(), dom.ActorClient, id)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeJSON(w, result)
}
func (h *handler) adminNotifications(w http.ResponseWriter, r *http.Request) {
	id, ok := h.admin(w, r)
	if !ok {
		return
	}
	result, err := h.service.Notifications(r.Context(), dom.ActorAdmin, id)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeJSON(w, result)
}

func (h *handler) createType(w http.ResponseWriter, r *http.Request) { h.saveType(w, r, 0) }
func (h *handler) updateType(w http.ResponseWriter, r *http.Request) {
	h.saveType(w, r, pathID(w, r, "configId"))
}
func (h *handler) saveType(w http.ResponseWriter, r *http.Request, id int64) {
	if _, ok := h.admin(w, r); !ok {
		return
	}
	var input types.ConfigInput
	if !decode(w, r, &input) {
		return
	}
	v, err := h.service.SaveType(r.Context(), id, input)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeJSON(w, v)
}
func (h *handler) deleteType(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.admin(w, r); !ok {
		return
	}
	id := pathID(w, r, "configId")
	if id == 0 {
		return
	}
	if err := h.service.DeleteType(r.Context(), id); err != nil {
		writeServiceError(w, err)
		return
	}
	writeJSON(w, map[string]bool{"deleted": true})
}
func (h *handler) createStatus(w http.ResponseWriter, r *http.Request) { h.saveStatus(w, r, 0) }
func (h *handler) updateStatus(w http.ResponseWriter, r *http.Request) {
	h.saveStatus(w, r, pathID(w, r, "configId"))
}
func (h *handler) saveStatus(w http.ResponseWriter, r *http.Request, id int64) {
	if _, ok := h.admin(w, r); !ok {
		return
	}
	var input types.ConfigInput
	if !decode(w, r, &input) {
		return
	}
	v, err := h.service.SaveStatus(r.Context(), id, input)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeJSON(w, v)
}
func (h *handler) deleteStatus(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.admin(w, r); !ok {
		return
	}
	id := pathID(w, r, "configId")
	if id == 0 {
		return
	}
	if err := h.service.DeleteStatus(r.Context(), id); err != nil {
		writeServiceError(w, err)
		return
	}
	writeJSON(w, map[string]bool{"deleted": true})
}
func (h *handler) createPrereply(w http.ResponseWriter, r *http.Request) { h.savePrereply(w, r, 0) }
func (h *handler) updatePrereply(w http.ResponseWriter, r *http.Request) {
	h.savePrereply(w, r, pathID(w, r, "configId"))
}
func (h *handler) savePrereply(w http.ResponseWriter, r *http.Request, id int64) {
	if _, ok := h.admin(w, r); !ok {
		return
	}
	var input types.ConfigInput
	if !decode(w, r, &input) {
		return
	}
	v, err := h.service.SavePrereply(r.Context(), id, input)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeJSON(w, v)
}
func (h *handler) deletePrereply(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.admin(w, r); !ok {
		return
	}
	id := pathID(w, r, "configId")
	if id == 0 {
		return
	}
	if err := h.service.DeletePrereply(r.Context(), id); err != nil {
		writeServiceError(w, err)
		return
	}
	writeJSON(w, map[string]bool{"deleted": true})
}
func (h *handler) orderPrereplies(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.admin(w, r); !ok {
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
		id, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || id <= 0 {
			writeError(w, http.StatusBadRequest, "预设回复 ID 格式错误")
			return
		}
		ids = append(ids, id)
	}
	if err := h.service.OrderPrereplies(r.Context(), ids); err != nil {
		writeServiceError(w, err)
		return
	}
	writeJSON(w, map[string]bool{"ordered": true})
}
func (h *handler) addNote(w http.ResponseWriter, r *http.Request) {
	adminID, ok := h.admin(w, r)
	if !ok {
		return
	}
	ticketID := pathID(w, r, "id")
	if ticketID == 0 {
		return
	}
	var input struct {
		Content string `json:"content"`
	}
	if !decode(w, r, &input) {
		return
	}
	v, err := h.service.AddNote(r.Context(), adminID, ticketID, input.Content)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeJSON(w, v)
}
func (h *handler) deleteNote(w http.ResponseWriter, r *http.Request) {
	adminID, ok := h.admin(w, r)
	if !ok {
		return
	}
	ticketID := pathID(w, r, "id")
	noteID := pathID(w, r, "noteId")
	if ticketID == 0 || noteID == 0 {
		return
	}
	if err := h.service.DeleteNote(r.Context(), adminID, ticketID, noteID); err != nil {
		writeServiceError(w, err)
		return
	}
	writeJSON(w, map[string]bool{"deleted": true})
}
func (h *handler) updateNote(w http.ResponseWriter, r *http.Request) {
	adminID, ok := h.admin(w, r)
	if !ok {
		return
	}
	ticketID := pathID(w, r, "id")
	noteID := pathID(w, r, "noteId")
	if ticketID == 0 || noteID == 0 {
		return
	}
	var input struct {
		Content string `json:"content"`
	}
	if !decode(w, r, &input) {
		return
	}
	if err := h.service.UpdateNote(r.Context(), adminID, ticketID, noteID, input.Content); err != nil {
		writeServiceError(w, err)
		return
	}
	writeJSON(w, map[string]bool{"updated": true})
}

var allowedExtensions = map[string]bool{".png": true, ".jpg": true, ".jpeg": true, ".gif": true, ".webp": true, ".pdf": true, ".txt": true, ".log": true, ".zip": true}

func (h *handler) userUpload(w http.ResponseWriter, r *http.Request) {
	id, ok := h.user(w, r)
	if !ok {
		return
	}
	h.upload(w, r, "/api/v1/tickets", dom.ActorClient, id)
}
func (h *handler) adminUpload(w http.ResponseWriter, r *http.Request) {
	id, ok := h.admin(w, r)
	if !ok {
		return
	}
	h.upload(w, r, "/admin/v1/tickets", dom.ActorAdmin, id)
}
func (h *handler) upload(w http.ResponseWriter, r *http.Request, prefix, ownerType string, ownerID int64) {
	r.Body = http.MaxBytesReader(w, r.Body, 52<<20)
	if err := r.ParseMultipartForm(52 << 20); err != nil {
		writeError(w, http.StatusRequestEntityTooLarge, "附件总大小不能超过 50MB")
		return
	}
	defer r.MultipartForm.RemoveAll()
	headers := r.MultipartForm.File["attachments"]
	if len(headers) == 0 {
		headers = r.MultipartForm.File["files"]
	}
	if len(headers) == 0 || len(headers) > 5 {
		writeError(w, http.StatusBadRequest, "请选择 1-5 个附件")
		return
	}
	items := make([]dom.Attachment, 0, len(headers))
	for _, header := range headers {
		item, err := h.saveUpload(r, header, prefix, ownerType, ownerID)
		if err != nil {
			for _, saved := range items {
				h.service.DiscardUpload(r.Context(), saved.ID, h.storageRoot)
			}
			writeError(w, http.StatusUnprocessableEntity, err.Error())
			return
		}
		items = append(items, item)
	}
	writeJSON(w, map[string]any{"list": items})
}
func (h *handler) saveUpload(r *http.Request, header *multipart.FileHeader, prefix, ownerType string, ownerID int64) (dom.Attachment, error) {
	name := filepath.Base(strings.TrimSpace(header.Filename))
	ext := strings.ToLower(filepath.Ext(name))
	if name == "" || len([]rune(name)) > 255 || !allowedExtensions[ext] {
		return dom.Attachment{}, errors.New("仅支持图片、PDF、文本、日志和 ZIP 附件")
	}
	if header.Size <= 0 || header.Size > 10<<20 {
		return dom.Attachment{}, errors.New("单个附件大小应在 10MB 以内")
	}
	source, err := header.Open()
	if err != nil {
		return dom.Attachment{}, err
	}
	defer source.Close()
	head := make([]byte, 512)
	n, readErr := source.Read(head)
	if readErr != nil && readErr != io.EOF {
		return dom.Attachment{}, errors.New("附件读取失败")
	}
	contentType := http.DetectContentType(head[:n])
	if !validUploadMIME(ext, contentType) {
		return dom.Attachment{}, errors.New("附件内容与文件类型不匹配")
	}
	if _, err = source.Seek(0, io.SeekStart); err != nil {
		return dom.Attachment{}, err
	}
	id := uuid.NewString()
	if err = h.service.ReserveUpload(r.Context(), &repo.Upload{ID: id, OwnerType: ownerType, OwnerID: ownerID, Name: name, Size: header.Size, ContentType: contentType}); err != nil {
		return dom.Attachment{}, err
	}
	complete := false
	defer func() {
		if !complete {
			h.service.DiscardUpload(r.Context(), id, h.storageRoot)
		}
	}()
	targetPath := filepath.Join(h.storageRoot, id)
	target, err := os.OpenFile(targetPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0640)
	if err != nil {
		return dom.Attachment{}, err
	}
	hash := sha256.New()
	written, copyErr := io.Copy(io.MultiWriter(target, hash), io.LimitReader(source, (10<<20)+1))
	closeErr := target.Close()
	if copyErr != nil || closeErr != nil || written > 10<<20 {
		_ = os.Remove(targetPath)
		return dom.Attachment{}, errors.New("附件保存失败或超过 10MB")
	}
	if err = h.service.CompleteUpload(r.Context(), id, hex.EncodeToString(hash.Sum(nil)), written); err != nil {
		return dom.Attachment{}, err
	}
	complete = true
	return dom.Attachment{ID: id, Name: name, Size: written, ContentType: contentType, URL: prefix + "/attachments/" + id}, nil
}
func validUploadMIME(ext, contentType string) bool {
	if ext == ".txt" || ext == ".log" {
		return strings.HasPrefix(contentType, "text/plain")
	}
	expected := map[string]string{".png": "image/png", ".jpg": "image/jpeg", ".jpeg": "image/jpeg", ".gif": "image/gif", ".webp": "image/webp", ".pdf": "application/pdf", ".zip": "application/zip"}
	return expected[ext] != "" && expected[ext] == contentType
}
func (h *handler) userDownload(w http.ResponseWriter, r *http.Request) {
	id, ok := h.user(w, r)
	if !ok {
		return
	}
	h.download(w, r, dom.ActorClient, id)
}
func (h *handler) adminDownload(w http.ResponseWriter, r *http.Request) {
	id, ok := h.admin(w, r)
	if !ok {
		return
	}
	h.download(w, r, dom.ActorAdmin, id)
}
func (h *handler) download(w http.ResponseWriter, r *http.Request, actorType string, actorID int64) {
	ticketID := pathID(w, r, "id")
	fileID := strings.Trim(pathvar.Vars(r)["file"], "/")
	_, parseErr := uuid.Parse(fileID)
	if ticketID == 0 || parseErr != nil {
		writeError(w, http.StatusBadRequest, "附件参数错误")
		return
	}
	item, err := h.service.Attachment(r.Context(), actorType, actorID, ticketID, fileID)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	path := filepath.Join(h.storageRoot, fileID)
	if _, err := os.Stat(path); err != nil {
		writeError(w, http.StatusNotFound, "附件文件不存在")
		return
	}
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename*=UTF-8''%s", url.PathEscape(item.Name)))
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "private, no-store")
	if item.ContentType != "" {
		w.Header().Set("Content-Type", item.ContentType)
	}
	http.ServeFile(w, r, path)
}
