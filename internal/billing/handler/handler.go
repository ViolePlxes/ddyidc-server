package billing

import (
	api "ddyidc/internal/billing/api"
	balance "ddyidc/internal/billing/balance"
	typ "ddyidc/internal/billing/types"
	"ddyidc/pkg/auth"
	"encoding/json"
	"errors"
	"github.com/zeromicro/go-zero/rest"
	"github.com/zeromicro/go-zero/rest/pathvar"
	"io"
	"net/http"
	"strconv"
	"strings"
)

type handler struct {
	app *api.Application
	jwt *auth.Manager
}

func RegisterHandlers(server *rest.Server, app *api.Application, jwt *auth.Manager) {
	h := &handler{app: app, jwt: jwt}
	server.AddRoutes([]rest.Route{
		{Method: http.MethodGet, Path: "/api/v1/billing/invoices", Handler: h.list},
		{Method: http.MethodGet, Path: "/api/v1/billing/invoices/:invoiceId", Handler: h.detail},
		{Method: http.MethodPost, Path: "/api/v1/billing/invoices/:invoiceId/pay", Handler: h.pay},
	})
}
func respond(w http.ResponseWriter, status int, data any, message string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	code := 0
	if status >= 400 {
		code = 1
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"code": code, "data": data, "message": message})
}
func (h *handler) customer(w http.ResponseWriter, r *http.Request) (int64, bool) {
	token := r.Header.Get("Authorization")
	if !strings.HasPrefix(token, "Bearer ") {
		respond(w, 401, nil, "请先登录")
		return 0, false
	}
	claims, e := h.jwt.ParseAccessToken(strings.TrimPrefix(token, "Bearer "))
	if e != nil || claims == nil {
		respond(w, 401, nil, "登录已失效")
		return 0, false
	}
	if claims.Role != "customer" || claims.UserID <= 0 {
		respond(w, 403, nil, "没有访问权限")
		return 0, false
	}
	return claims.UserID, true
}
func failure(w http.ResponseWriter, e error) {
	var public *api.UserError
	switch {
	case errors.Is(e, typ.ErrNotFound):
		respond(w, 404, nil, "账单不存在")
	case errors.As(e, &public):
		respond(w, 409, nil, public.Message)
	case errors.Is(e, balance.ErrInsufficient):
		respond(w, 409, nil, "账户余额不足，请充值或选择在线支付")
	case errors.Is(e, balance.ErrLedgerIntegrity):
		respond(w, 409, nil, "账户收支正在核对，请勿重复支付")
	case errors.Is(e, typ.ErrDisabled):
		respond(w, 409, nil, "账单服务暂不可用，请稍后重试")
	case errors.Is(e, typ.ErrConflict):
		respond(w, 409, nil, "账单状态已变化，请刷新核对后重试")
	case errors.Is(e, balance.ErrBusiness):
		respond(w, 409, nil, "对应业务的状态或金额已变化，请刷新核对后重试")
	case errors.Is(e, balance.ErrReplay):
		respond(w, 409, nil, "本次请求与已有支付记录不一致，请勿重复提交")
	case errors.Is(e, balance.ErrInvalid):
		respond(w, 409, nil, "账单金额或业务关联无效，请刷新核对后重试")
	default:
		respond(w, 500, nil, "账单操作未完成，请核对状态后重试")
	}
}
func (h *handler) list(w http.ResponseWriter, r *http.Request) {
	user, ok := h.customer(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	page, size := 1, 10
	for key, values := range q {
		if len(values) != 1 || (key != "page" && key != "pageSize" && key != "kind" && key != "status" && key != "keyword") {
			respond(w, 400, nil, "账单查询参数无效")
			return
		}
	}
	if q.Has("page") {
		page, _ = strconv.Atoi(q.Get("page"))
	}
	if q.Has("pageSize") {
		size, _ = strconv.Atoi(q.Get("pageSize"))
	}
	f := api.Filter{Kind: q.Get("kind"), Status: q.Get("status"), Keyword: q.Get("keyword"), Page: page, PageSize: size}
	if e := f.Validate(); e != nil {
		respond(w, 400, nil, e.Error())
		return
	}
	rows, total, e := h.app.List(r.Context(), user, f)
	if e != nil {
		failure(w, e)
		return
	}
	respond(w, 200, map[string]any{"list": rows, "total": total, "page": page, "pageSize": size}, "")
}
func (h *handler) detail(w http.ResponseWriter, r *http.Request) {
	user, ok := h.customer(w, r)
	if !ok {
		return
	}
	out, e := h.app.Detail(r.Context(), user, pathvar.Vars(r)["invoiceId"])
	if e != nil {
		failure(w, e)
		return
	}
	respond(w, 200, out, "")
}
func (h *handler) pay(w http.ResponseWriter, r *http.Request) {
	user, ok := h.customer(w, r)
	if !ok {
		return
	}
	var in struct{ Method, Mode string }
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
	decoder.DisallowUnknownFields()
	if e := decoder.Decode(&in); e != nil {
		respond(w, 400, nil, "支付参数无效")
		return
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		respond(w, 400, nil, "支付参数无效")
		return
	}
	if in.Method != "balance" && in.Method != "alipay" && in.Method != "wechat" {
		respond(w, 400, nil, "支付方式无效")
		return
	}
	out, e := h.app.Pay(r.Context(), user, pathvar.Vars(r)["invoiceId"], in.Method, in.Mode)
	if e != nil {
		failure(w, e)
		return
	}
	respond(w, 200, out, "")
}
