package instance

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	svc "ddyidc/internal/instance/service"
	"ddyidc/pkg/auth"
	"ddyidc/pkg/clientip"
	"github.com/zeromicro/go-zero/rest"
	"github.com/zeromicro/go-zero/rest/pathvar"
)

func (h *handler) adminListRoutes() []rest.Route {
	return []rest.Route{
		{Method: http.MethodGet, Path: "/admin/v1/instances", Handler: h.adminInstances},
		{Method: http.MethodGet, Path: "/admin/v1/instances/renewal-quote-audit", Handler: h.adminRenewalQuoteAudit},
		{Method: http.MethodGet, Path: "/admin/v1/instances/filter-options", Handler: h.adminInstanceOptions},
		{Method: http.MethodGet, Path: "/admin/v1/instances/export", Handler: h.adminInstanceExport},
		{Method: http.MethodPost, Path: "/admin/v1/instances/sync", Handler: h.adminInstanceSync},
		{Method: http.MethodGet, Path: "/admin/v1/instances/sync/:taskId", Handler: h.adminInstanceSyncResult},
		{Method: http.MethodPut, Path: "/admin/v1/instances/:instanceId/remark", Handler: h.adminInstanceRemark},
	}
}
func (h *handler) globalAccess(w http.ResponseWriter, r *http.Request) (svc.AdminAccess, bool) {
	w.Header().Set("Cache-Control", "no-store")
	var empty svc.AdminAccess
	token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	claims, e := h.jwt.ParseAccessTokenFor(r.Context(), auth.SessionAdmin, token)
	if e != nil || claims == nil || claims.Role != "admin" {
		writeError(w, 401, "需要管理员登录")
		return empty, false
	}
	if !h.jwt.ValidateCSRF(r, auth.SessionAdmin, claims) {
		writeError(w, 403, "请求安全校验未通过")
		return empty, false
	}
	a, e := h.service.AdminAccess(r.Context(), claims.UserID)
	if e != nil {
		if errors.Is(e, svc.ErrForbidden) {
			writeError(w, 403, "没有全局产品列表权限")
		} else {
			writeError(w, 503, "权限核验暂不可用")
		}
		return empty, false
	}
	return a, true
}
func adminListInput(r *http.Request) (svc.AdminListInput, error) {
	q := r.URL.Query()
	in := svc.AdminListInput{Search: q.Get("search"), View: q.Get("view"), CustomerID: q.Get("customerId"), ProductID: q.Get("productId"), SupplierID: q.Get("supplierId"), Status: q.Get("status"), Cycle: q.Get("cycle"), AutoRenew: q.Get("autoRenew"), CreatedFrom: q.Get("createdFrom"), CreatedTo: q.Get("createdTo"), ExpiresFrom: q.Get("expiresFrom"), ExpiresTo: q.Get("expiresTo"), MinAmount: q.Get("minAmount"), MaxAmount: q.Get("maxAmount"), Sort: q.Get("sort"), Direction: q.Get("direction")}
	in.ActivatedFrom = q.Get("activatedFrom")
	in.ActivatedTo = q.Get("activatedTo")
	for key, target := range map[string]*int{"page": &in.Page, "pageSize": &in.PageSize} {
		if q.Get(key) != "" {
			v, e := strconv.Atoi(q.Get(key))
			if e != nil || v < 1 {
				return in, svc.ErrAdminListInput
			}
			*target = v
		}
	}
	return in, nil
}
func (h *handler) adminInstances(w http.ResponseWriter, r *http.Request) {
	a, ok := h.globalAccess(w, r)
	if !ok {
		return
	}
	in, e := adminListInput(r)
	if e != nil {
		writeError(w, 400, e.Error())
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	result, e := h.service.AdminList(ctx, a, in)
	if e != nil {
		adminListError(w, e)
		return
	}
	writeJSON(w, result)
}
func adminListError(w http.ResponseWriter, e error) {
	if errors.Is(e, svc.ErrForbidden) {
		writeError(w, 403, "没有权限")
	} else if errors.Is(e, svc.ErrAdminListInput) || errors.Is(e, svc.ErrInvalidRemark) {
		writeError(w, 400, e.Error())
	} else if errors.Is(e, svc.ErrNotFound) {
		writeError(w, 404, "记录不存在或不可见")
	} else {
		writeError(w, 422, "处理失败，请刷新后重试；同步不会执行续费或机器操作")
	}
}
func (h *handler) adminInstanceOptions(w http.ResponseWriter, r *http.Request) {
	a, ok := h.globalAccess(w, r)
	if !ok {
		return
	}
	out, e := h.service.AdminFilterOptions(r.Context(), a)
	if e != nil {
		adminListError(w, e)
		return
	}
	writeJSON(w, out)
}
func csvCell(v string) string {
	v = strings.ReplaceAll(v, "\x00", "")
	trimmed := strings.TrimLeft(v, " \t\r\n\ufeff")
	if trimmed != "" && strings.ContainsRune("=+-@", rune(trimmed[0])) {
		return "'" + v
	}
	return v
}
func (h *handler) adminInstanceExport(w http.ResponseWriter, r *http.Request) {
	a, ok := h.globalAccess(w, r)
	if !ok {
		return
	}
	in, e := adminListInput(r)
	if e != nil {
		writeError(w, 400, e.Error())
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	rows, e := h.service.AdminExportRows(ctx, a, in)
	if e != nil {
		adminListError(w, e)
		return
	}
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=instances-%s.csv", time.Now().UTC().Format("20060102T150405Z")))
	_, _ = w.Write([]byte{0xef, 0xbb, 0xbf})
	out := csv.NewWriter(w)
	_ = out.Write([]string{"导出时间(北京时间)", "筛选范围", "实例ID", "编号", "商品", "标识", "客户ID", "用户名", "手机号", "邮箱", "主IP", "附加IP", "生命周期", "缓存电源状态", "到期时间(北京时间)", "续费金额(元)", "周期", "用户备注", "供应商", "资源ID", "订单号", "创建时间(北京时间)", "最后同步时间(北京时间)", "自动续费"})
	zone := time.FixedZone("Asia/Shanghai", 8*3600)
	filterJSON, _ := json.Marshal(in)
	now := time.Now().In(zone).Format("2006-01-02 15:04:05")
	date := func(t *time.Time) string {
		if t == nil {
			return ""
		}
		return t.In(zone).Format("2006-01-02 15:04:05")
	}
	for _, row := range rows {
		amount := ""
		if row.RenewalAmount != nil {
			amount = *row.RenewalAmount
		}
		record := []string{now, string(filterJSON), "'" + row.ID, row.InstanceNo, row.ProductName, row.Hostname, "'" + row.CustomerID, row.Username, row.Phone, row.Email, row.PrimaryIP, strings.Join(row.IPs, ";"), strconv.Itoa(int(row.Status)), row.PowerStatus, date(row.ExpiresAt), amount, row.Cycle, row.Remark, row.SupplierName, row.UpstreamInstanceID, row.OrderNo, date(&row.CreatedAt), date(row.LastSyncedAt), strconv.FormatBool(row.AutoRenew)}
		for i := range record {
			record[i] = csvCell(record[i])
		}
		if out.Write(record) != nil {
			return
		}
	}
	out.Flush()
}
func (h *handler) adminInstanceSync(w http.ResponseWriter, r *http.Request) {
	a, ok := h.globalAccess(w, r)
	if !ok {
		return
	}
	var in svc.AdminSyncInput
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 32768))
	d.DisallowUnknownFields()
	if d.Decode(&in) != nil {
		writeError(w, 400, "同步参数错误")
		return
	}
	out, e := h.service.StartAdminSync(r.Context(), a, clientip.FromRequest(r), in)
	if e != nil {
		adminListError(w, e)
		return
	}
	writeJSON(w, out)
}
func (h *handler) adminInstanceSyncResult(w http.ResponseWriter, r *http.Request) {
	a, ok := h.globalAccess(w, r)
	if !ok {
		return
	}
	id, e := strconv.ParseInt(pathvar.Vars(r)["taskId"], 10, 64)
	if e != nil || id <= 0 {
		writeError(w, 400, "任务 ID 无效")
		return
	}
	out, e := h.service.AdminSyncResult(r.Context(), a, id)
	if e != nil {
		adminListError(w, e)
		return
	}
	writeJSON(w, out)
}
func (h *handler) adminInstanceRemark(w http.ResponseWriter, r *http.Request) {
	a, ok := h.globalAccess(w, r)
	if !ok {
		return
	}
	id := pathID(w, r)
	if id == 0 {
		return
	}
	var in svc.AdminRemarkInput
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8192))
	d.DisallowUnknownFields()
	if d.Decode(&in) != nil {
		writeError(w, 400, "备注参数错误")
		return
	}
	if e := h.service.SaveAdminRemark(r.Context(), a, clientip.FromRequest(r), id, in); e != nil {
		adminListError(w, e)
		return
	}
	writeJSON(w, map[string]bool{"saved": true})
}

func (h *handler) adminRenewalQuoteAudit(w http.ResponseWriter, r *http.Request) {
	access, ok := h.globalAccess(w, r)
	if !ok {
		return
	}
	page := 1
	if raw := r.URL.Query().Get("page"); raw != "" {
		var e error
		page, e = strconv.Atoi(raw)
		if e != nil {
			writeError(w, 400, "分页参数无效")
			return
		}
	}
	result, e := h.service.AuditRenewalQuotes(r.Context(), access, page)
	if e != nil {
		writeError(w, 503, "续费核查暂不可用")
		return
	}
	writeJSON(w, result)
}
