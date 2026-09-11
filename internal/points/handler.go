package points

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
	routes := []rest.Route{
		{Method: http.MethodGet, Path: "/api/v1/points/status", Handler: h.status},
		{Method: http.MethodGet, Path: "/api/v1/points/account", Handler: h.account},
		{Method: http.MethodGet, Path: "/api/v1/points/records", Handler: h.records},
		{Method: http.MethodPost, Path: "/api/v1/points/signin", Handler: h.signin},
		{Method: http.MethodGet, Path: "/api/v1/points/signin/calendar", Handler: h.calendar},
		{Method: http.MethodGet, Path: "/api/v1/points/tasks", Handler: h.tasks},
		{Method: http.MethodPost, Path: "/api/v1/points/tasks/:id/claim", Handler: h.claimTask},
		{Method: http.MethodGet, Path: "/api/v1/points/goods", Handler: h.goods},
		{Method: http.MethodGet, Path: "/api/v1/points/goods/:id", Handler: h.goodsOne},
		{Method: http.MethodPost, Path: "/api/v1/points/goods/:id/exchange", Handler: h.exchange},
		{Method: http.MethodGet, Path: "/api/v1/points/orders", Handler: h.orders},
		{Method: http.MethodGet, Path: "/api/v1/points/orders/:id", Handler: h.orderOne},
		{Method: http.MethodPost, Path: "/api/v1/points/orders/:id/confirm", Handler: h.confirm},
		{Method: http.MethodGet, Path: "/api/v1/points/rules", Handler: h.publicRules},
		{Method: http.MethodGet, Path: "/admin/v1/points/settings", Handler: h.adminSettings},
		{Method: http.MethodPut, Path: "/admin/v1/points/settings", Handler: h.saveSettings},
		{Method: http.MethodGet, Path: "/admin/v1/points/rules", Handler: h.adminRules},
		{Method: http.MethodPost, Path: "/admin/v1/points/rules", Handler: h.saveRule},
		{Method: http.MethodPut, Path: "/admin/v1/points/rules/:id", Handler: h.saveRule},
		{Method: http.MethodDelete, Path: "/admin/v1/points/rules/:id", Handler: h.deleteRule},
		{Method: http.MethodGet, Path: "/admin/v1/points/tasks", Handler: h.adminTasks},
		{Method: http.MethodPost, Path: "/admin/v1/points/tasks", Handler: h.saveTask},
		{Method: http.MethodPut, Path: "/admin/v1/points/tasks/:id", Handler: h.saveTask},
		{Method: http.MethodDelete, Path: "/admin/v1/points/tasks/:id", Handler: h.deleteTask},
		{Method: http.MethodGet, Path: "/admin/v1/points/goods", Handler: h.adminGoods},
		{Method: http.MethodPost, Path: "/admin/v1/points/goods", Handler: h.saveGoods},
		{Method: http.MethodPut, Path: "/admin/v1/points/goods/:id", Handler: h.saveGoods},
		{Method: http.MethodDelete, Path: "/admin/v1/points/goods/:id", Handler: h.deleteGoods},
		{Method: http.MethodGet, Path: "/admin/v1/points/categories", Handler: h.categories},
		{Method: http.MethodPost, Path: "/admin/v1/points/categories", Handler: h.saveCategory},
		{Method: http.MethodPut, Path: "/admin/v1/points/categories/:id", Handler: h.saveCategory},
		{Method: http.MethodDelete, Path: "/admin/v1/points/categories/:id", Handler: h.deleteCategory},
		{Method: http.MethodGet, Path: "/admin/v1/points/cards", Handler: h.cards},
		{Method: http.MethodPost, Path: "/admin/v1/points/cards", Handler: h.importCards},
		{Method: http.MethodGet, Path: "/admin/v1/points/orders", Handler: h.adminOrders},
		{Method: http.MethodPost, Path: "/admin/v1/points/orders/:id/deliver", Handler: h.deliver},
		{Method: http.MethodPost, Path: "/admin/v1/points/orders/:id/status", Handler: h.setOrderStatus},
		{Method: http.MethodGet, Path: "/admin/v1/points/accounts", Handler: h.accounts},
		{Method: http.MethodPost, Path: "/admin/v1/points/accounts/:id/status", Handler: h.setAccountStatus},
		{Method: http.MethodGet, Path: "/admin/v1/points/accounts/:id/records", Handler: h.accountRecords},
		{Method: http.MethodPost, Path: "/admin/v1/points/accounts/:id/tag", Handler: h.setTag},
		{Method: http.MethodPost, Path: "/admin/v1/points/grant", Handler: h.grant},
		{Method: http.MethodPost, Path: "/admin/v1/points/trigger", Handler: h.trigger},
		{Method: http.MethodGet, Path: "/admin/v1/points/stats", Handler: h.stats},
		{Method: http.MethodGet, Path: "/admin/v1/points/risk-tags", Handler: h.riskTags},
		{Method: http.MethodPost, Path: "/admin/v1/points/risk-tags", Handler: h.saveRiskTag},
		{Method: http.MethodPut, Path: "/admin/v1/points/risk-tags/:id", Handler: h.saveRiskTag},
		{Method: http.MethodDelete, Path: "/admin/v1/points/risk-tags/:id", Handler: h.deleteRiskTag},
		{Method: http.MethodGet, Path: "/admin/v1/points/risk-rules", Handler: h.riskRules},
		{Method: http.MethodPost, Path: "/admin/v1/points/risk-rules", Handler: h.saveRiskRule},
		{Method: http.MethodPut, Path: "/admin/v1/points/risk-rules/:id", Handler: h.saveRiskRule},
		{Method: http.MethodDelete, Path: "/admin/v1/points/risk-rules/:id", Handler: h.deleteRiskRule},
		{Method: http.MethodGet, Path: "/admin/v1/points/risk-logs", Handler: h.riskLogs},
	}
	for i := range routes {
		routes[i].Handler = h.enabled(routes[i].Handler)
	}
	server.AddRoutes(routes)
}

func (h *Handler) enabled(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		enabled, err := h.service.Enabled(r.Context())
		if err != nil || !enabled {
			writeMessage(w, http.StatusNotFound, "积分中心未开启")
			return
		}
		next(w, r)
	}
}

func (h *Handler) status(w http.ResponseWriter, r *http.Request) {
	v, e := h.service.Settings(r.Context())
	if e != nil {
		writeErr(w, 500, e)
		return
	}
	writeOK(w, map[string]any{"enabled": v.Enabled, "showMall": v.ShowMall, "showSignin": v.ShowSignin, "showTasks": v.ShowTasks})
}
func (h *Handler) claims(w http.ResponseWriter, r *http.Request, role string) (int64, bool) {
	head := r.Header.Get("Authorization")
	if !strings.HasPrefix(head, "Bearer ") {
		writeMessage(w, 401, "请先登录")
		return 0, false
	}
	kind := auth.SessionUser
	if role == "admin" {
		kind = auth.SessionAdmin
	}
	c, e := h.jwt.ParseAccessTokenFor(r.Context(), kind, strings.TrimPrefix(head, "Bearer "))
	if e != nil || c == nil {
		writeMessage(w, 401, "登录已失效")
		return 0, false
	}
	if c.Role != role {
		writeMessage(w, 403, "权限不足")
		return 0, false
	}
	return c.UserID, true
}
func (h *Handler) user(w http.ResponseWriter, r *http.Request) (int64, bool) {
	return h.claims(w, r, "customer")
}
func (h *Handler) admin(w http.ResponseWriter, r *http.Request) (int64, bool) {
	return h.claims(w, r, "admin")
}
func (h *Handler) account(w http.ResponseWriter, r *http.Request) {
	id, ok := h.user(w, r)
	if !ok {
		return
	}
	v, e := h.service.Account(r.Context(), id)
	writeResult(w, v, e)
}
func (h *Handler) records(w http.ResponseWriter, r *http.Request) {
	id, ok := h.user(w, r)
	if !ok {
		return
	}
	p, z := queryPage(r)
	rows, total, e := h.service.Records(r.Context(), id, r.URL.Query().Get("type"), r.URL.Query().Get("source"), r.URL.Query().Get("keyword"), r.URL.Query().Get("from"), r.URL.Query().Get("to"), p, z)
	writePage(w, rows, total, p, z, e)
}
func (h *Handler) signin(w http.ResponseWriter, r *http.Request) {
	id, ok := h.user(w, r)
	if !ok {
		return
	}
	var in struct {
		Mode string `json:"mode"`
	}
	if !decode(w, r, &in) {
		return
	}
	v, e := h.service.Signin(r.Context(), id, in.Mode)
	writeResult(w, v, e)
}
func (h *Handler) calendar(w http.ResponseWriter, r *http.Request) {
	id, ok := h.user(w, r)
	if !ok {
		return
	}
	v, e := h.service.SigninCalendar(r.Context(), id, r.URL.Query().Get("month"))
	writeResult(w, v, e)
}
func (h *Handler) tasks(w http.ResponseWriter, r *http.Request) {
	id, ok := h.user(w, r)
	if !ok {
		return
	}
	v, e := h.service.Tasks(r.Context(), id, r.URL.Query().Get("category"))
	writeResult(w, v, e)
}
func (h *Handler) claimTask(w http.ResponseWriter, r *http.Request) {
	user, ok := h.user(w, r)
	if !ok {
		return
	}
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	v, e := h.service.ClaimTask(r.Context(), user, id)
	writeResult(w, v, e)
}
func (h *Handler) goods(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.user(w, r); !ok {
		return
	}
	p, z := queryPage(r)
	cat, _ := strconv.ParseInt(r.URL.Query().Get("categoryId"), 10, 64)
	rows, total, e := h.service.Goods(r.Context(), cat, p, z)
	writePage(w, rows, total, p, z, e)
}
func (h *Handler) goodsOne(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.user(w, r); !ok {
		return
	}
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	v, e := h.service.GoodsOne(r.Context(), id, false)
	writeResult(w, v, e)
}
func (h *Handler) exchange(w http.ResponseWriter, r *http.Request) {
	user, ok := h.user(w, r)
	if !ok {
		return
	}
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var in struct {
		Quantity  int             `json:"quantity"`
		Recipient json.RawMessage `json:"recipient"`
	}
	if !decode(w, r, &in) {
		return
	}
	if len(in.Recipient) == 0 {
		in.Recipient = json.RawMessage(`{}`)
	}
	v, e := h.service.Exchange(r.Context(), user, id, in.Quantity, in.Recipient)
	writeResult(w, v, e)
}
func (h *Handler) orders(w http.ResponseWriter, r *http.Request) {
	user, ok := h.user(w, r)
	if !ok {
		return
	}
	p, z := queryPage(r)
	rows, total, e := h.service.Orders(r.Context(), user, false, r.URL.Query().Get("status"), p, z)
	writePage(w, rows, total, p, z, e)
}
func (h *Handler) orderOne(w http.ResponseWriter, r *http.Request) {
	user, ok := h.user(w, r)
	if !ok {
		return
	}
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	v, e := h.service.Order(r.Context(), user, id, false)
	writeResult(w, v, e)
}
func (h *Handler) confirm(w http.ResponseWriter, r *http.Request) {
	user, ok := h.user(w, r)
	if !ok {
		return
	}
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	writeResult(w, map[string]bool{"updated": true}, h.service.Confirm(r.Context(), user, id))
}
func (h *Handler) publicRules(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.user(w, r); !ok {
		return
	}
	v, e := h.service.ListRules(r.Context(), true)
	writeResult(w, v, e)
}

func (h *Handler) adminSettings(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.admin(w, r); !ok {
		return
	}
	v, e := h.service.Settings(r.Context())
	writeResult(w, v, e)
}
func (h *Handler) saveSettings(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.admin(w, r); !ok {
		return
	}
	var v Settings
	if !decode(w, r, &v) {
		return
	}
	e := h.service.SaveSettings(r.Context(), &v)
	writeResult(w, &v, e)
}
func (h *Handler) adminRules(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.admin(w, r); !ok {
		return
	}
	v, e := h.service.ListRules(r.Context(), false)
	writeResult(w, v, e)
}
func (h *Handler) saveRule(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.admin(w, r); !ok {
		return
	}
	var v Rule
	if !decode(w, r, &v) {
		return
	}
	if id, ok := optionalPathID(r); ok {
		v.ID = id
	}
	e := h.service.SaveRule(r.Context(), &v)
	writeResult(w, &v, e)
}
func (h *Handler) deleteRule(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.admin(w, r); !ok {
		return
	}
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	writeResult(w, map[string]bool{"deleted": true}, h.service.DeleteRule(r.Context(), id))
}
func (h *Handler) adminTasks(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.admin(w, r); !ok {
		return
	}
	v, e := h.service.ListTasksAdmin(r.Context())
	writeResult(w, v, e)
}
func (h *Handler) saveTask(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.admin(w, r); !ok {
		return
	}
	var v Task
	if !decode(w, r, &v) {
		return
	}
	if id, ok := optionalPathID(r); ok {
		v.ID = id
	}
	e := h.service.SaveTask(r.Context(), &v)
	writeResult(w, &v, e)
}
func (h *Handler) deleteTask(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.admin(w, r); !ok {
		return
	}
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	writeResult(w, map[string]bool{"deleted": true}, h.service.DeleteTask(r.Context(), id))
}
func (h *Handler) adminGoods(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.admin(w, r); !ok {
		return
	}
	v, e := h.service.ListGoodsAdmin(r.Context())
	writeResult(w, v, e)
}
func (h *Handler) saveGoods(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.admin(w, r); !ok {
		return
	}
	var v Goods
	if !decode(w, r, &v) {
		return
	}
	if id, ok := optionalPathID(r); ok {
		v.ID = id
	}
	e := h.service.SaveGoods(r.Context(), &v)
	writeResult(w, &v, e)
}
func (h *Handler) deleteGoods(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.admin(w, r); !ok {
		return
	}
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	writeResult(w, map[string]bool{"deleted": true}, h.service.DeleteGoods(r.Context(), id))
}
func (h *Handler) categories(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.admin(w, r); !ok {
		return
	}
	v, e := h.service.Categories(r.Context())
	writeResult(w, v, e)
}
func (h *Handler) saveCategory(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.admin(w, r); !ok {
		return
	}
	var v Category
	if !decode(w, r, &v) {
		return
	}
	if id, ok := optionalPathID(r); ok {
		v.ID = id
	}
	e := h.service.SaveCategory(r.Context(), &v)
	writeResult(w, &v, e)
}
func (h *Handler) deleteCategory(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.admin(w, r); !ok {
		return
	}
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	writeResult(w, map[string]bool{"deleted": true}, h.service.DeleteCategory(r.Context(), id))
}
func (h *Handler) cards(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.admin(w, r); !ok {
		return
	}
	goods, _ := strconv.ParseInt(r.URL.Query().Get("goodsId"), 10, 64)
	var rows []Card
	e := h.service.db.WithContext(r.Context()).Table(cardsTable).Where("goods_id=?", goods).Order("id DESC").Limit(500).Find(&rows).Error
	writeResult(w, rows, e)
}
func (h *Handler) importCards(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.admin(w, r); !ok {
		return
	}
	var in struct {
		GoodsID string   `json:"goodsId"`
		Values  []string `json:"values"`
		Text    string   `json:"text"`
	}
	if !decode(w, r, &in) {
		return
	}
	id, e := strconv.ParseInt(in.GoodsID, 10, 64)
	if e != nil {
		writeMessage(w, 400, "商品ID无效")
		return
	}
	values := in.Values
	if len(values) == 0 {
		values = strings.FieldsFunc(in.Text, func(v rune) bool { return v == '\n' || v == '\r' })
	}
	v, e := h.service.ImportCards(r.Context(), id, values)
	writeResult(w, v, e)
}
func (h *Handler) adminOrders(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.admin(w, r); !ok {
		return
	}
	p, z := queryPage(r)
	rows, total, e := h.service.Orders(r.Context(), 0, true, r.URL.Query().Get("status"), p, z)
	writePage(w, rows, total, p, z, e)
}
func (h *Handler) deliver(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.admin(w, r); !ok {
		return
	}
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var in json.RawMessage
	if !decode(w, r, &in) {
		return
	}
	writeResult(w, map[string]bool{"updated": true}, h.service.Deliver(r.Context(), id, in))
}
func (h *Handler) setOrderStatus(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.admin(w, r); !ok {
		return
	}
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var in struct {
		Status string `json:"status"`
	}
	if !decode(w, r, &in) {
		return
	}
	writeResult(w, map[string]bool{"updated": true}, h.service.SetOrderStatus(r.Context(), id, in.Status))
}
func (h *Handler) accounts(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.admin(w, r); !ok {
		return
	}
	p, z := queryPage(r)
	var status *bool
	if raw := r.URL.Query().Get("status"); raw != "" {
		v := raw == "1" || raw == "true"
		status = &v
	}
	rows, total, e := h.service.Accounts(r.Context(), r.URL.Query().Get("keyword"), status, p, z)
	writePage(w, rows, total, p, z, e)
}
func (h *Handler) setAccountStatus(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.admin(w, r); !ok {
		return
	}
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var in struct {
		Status bool `json:"status"`
	}
	if !decode(w, r, &in) {
		return
	}
	writeResult(w, map[string]bool{"updated": true}, h.service.SetAccountStatus(r.Context(), id, in.Status))
}
func (h *Handler) accountRecords(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.admin(w, r); !ok {
		return
	}
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	p, z := queryPage(r)
	rows, total, e := h.service.Records(r.Context(), id, r.URL.Query().Get("type"), r.URL.Query().Get("source"), r.URL.Query().Get("keyword"), r.URL.Query().Get("from"), r.URL.Query().Get("to"), p, z)
	writePage(w, rows, total, p, z, e)
}
func (h *Handler) setTag(w http.ResponseWriter, r *http.Request) {
	admin, ok := h.admin(w, r)
	if !ok {
		return
	}
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var in struct {
		TagID  string `json:"tagId"`
		Locked bool   `json:"locked"`
		Remark string `json:"remark"`
	}
	if !decode(w, r, &in) {
		return
	}
	tag, e := strconv.ParseInt(in.TagID, 10, 64)
	if e != nil {
		writeMessage(w, 400, "标签ID无效")
		return
	}
	writeResult(w, map[string]bool{"updated": true}, h.service.SetUserTag(r.Context(), id, tag, admin, in.Locked, in.Remark))
}
func (h *Handler) grant(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.admin(w, r); !ok {
		return
	}
	var in struct {
		UserIDs []string `json:"userIds"`
		Points  int64    `json:"points"`
		Remark  string   `json:"remark"`
	}
	if !decode(w, r, &in) {
		return
	}
	ids := parseIDs(in.UserIDs)
	n, e := h.service.AdminGrant(r.Context(), ids, in.Points, in.Remark)
	writeResult(w, map[string]int{"affected": n}, e)
}
func (h *Handler) trigger(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.admin(w, r); !ok {
		return
	}
	var in struct {
		UserIDs     []string `json:"userIds"`
		Event       string   `json:"event"`
		AmountCents int64    `json:"amountCents"`
		Remark      string   `json:"remark"`
	}
	if !decode(w, r, &in) {
		return
	}
	ids := parseIDs(in.UserIDs)
	if len(ids) == 0 {
		_ = h.service.db.WithContext(r.Context()).Table("customer.customers").Where("status=1").Pluck("id", &ids).Error
	}
	affected := 0
	for _, id := range ids {
		if _, e := h.service.GrantByEvent(r.Context(), in.Event, id, EventMeta{RefID: "admin-trigger-" + strconv.FormatInt(id, 10) + "-" + strconv.FormatInt(h.service.now().UnixNano(), 10), AmountCents: in.AmountCents, Remark: in.Remark}); e == nil {
			affected++
		}
	}
	writeOK(w, map[string]int{"affected": affected})
}
func (h *Handler) stats(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.admin(w, r); !ok {
		return
	}
	v, e := h.service.Stats(r.Context())
	writeResult(w, v, e)
}
func (h *Handler) riskTags(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.admin(w, r); !ok {
		return
	}
	v, e := h.service.RiskTags(r.Context())
	writeResult(w, v, e)
}
func (h *Handler) saveRiskTag(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.admin(w, r); !ok {
		return
	}
	var v RiskTag
	if !decode(w, r, &v) {
		return
	}
	if id, ok := optionalPathID(r); ok {
		v.ID = id
	}
	e := h.service.SaveRiskTag(r.Context(), &v)
	writeResult(w, &v, e)
}
func (h *Handler) deleteRiskTag(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.admin(w, r); !ok {
		return
	}
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	writeResult(w, map[string]bool{"deleted": true}, h.service.DeleteRiskTag(r.Context(), id))
}
func (h *Handler) riskRules(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.admin(w, r); !ok {
		return
	}
	v, e := h.service.RiskRules(r.Context())
	writeResult(w, v, e)
}
func (h *Handler) saveRiskRule(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.admin(w, r); !ok {
		return
	}
	var v RiskRule
	if !decode(w, r, &v) {
		return
	}
	if id, ok := optionalPathID(r); ok {
		v.ID = id
	}
	e := h.service.SaveRiskRule(r.Context(), &v)
	writeResult(w, &v, e)
}
func (h *Handler) deleteRiskRule(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.admin(w, r); !ok {
		return
	}
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	writeResult(w, map[string]bool{"deleted": true}, h.service.DeleteRiskRule(r.Context(), id))
}
func (h *Handler) riskLogs(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.admin(w, r); !ok {
		return
	}
	p, z := queryPage(r)
	rows, total, e := h.service.RiskLogs(r.Context(), p, z)
	writePage(w, rows, total, p, z, e)
}

func parseIDs(raw []string) []int64 {
	out := make([]int64, 0, len(raw))
	for _, v := range raw {
		if id, e := strconv.ParseInt(v, 10, 64); e == nil && id > 0 {
			out = append(out, id)
		}
	}
	return out
}
func optionalPathID(r *http.Request) (int64, bool) {
	v := pathvar.Vars(r)["id"]
	if v == "" {
		return 0, false
	}
	id, e := strconv.ParseInt(v, 10, 64)
	return id, e == nil && id > 0
}
func pathID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, ok := optionalPathID(r)
	if !ok {
		writeMessage(w, 400, "ID 无效")
	}
	return id, ok
}
func queryPage(r *http.Request) (int, int) {
	p, _ := strconv.Atoi(r.URL.Query().Get("page"))
	z, _ := strconv.Atoi(r.URL.Query().Get("pageSize"))
	return page(p, z)
}
func decode(w http.ResponseWriter, r *http.Request, to any) bool {
	if e := json.NewDecoder(http.MaxBytesReader(w, r.Body, 2<<20)).Decode(to); e != nil {
		writeMessage(w, 400, "请求参数格式错误")
		return false
	}
	return true
}
func writeOK(w http.ResponseWriter, data any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": data})
}
func writePage(w http.ResponseWriter, rows any, total int64, p, z int, e error) {
	if e != nil {
		writeErr(w, 500, e)
		return
	}
	writeOK(w, map[string]any{"list": rows, "total": total, "page": p, "pageSize": z})
}
func writeResult(w http.ResponseWriter, data any, e error) {
	if e != nil {
		writeErr(w, 400, e)
		return
	}
	writeOK(w, data)
}
func writeErr(w http.ResponseWriter, fallback int, e error) {
	status := fallback
	if errors.Is(e, ErrNotFound) {
		status = 404
	}
	if errors.Is(e, ErrForbidden) {
		status = 403
	}
	if errors.Is(e, ErrDisabled) {
		status = 404
	}
	writeMessage(w, status, e.Error())
}
func writeMessage(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"code": 1, "message": message})
}
