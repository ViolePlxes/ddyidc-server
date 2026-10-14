package customer

import (
	"bytes"
	balance "ddyidc/internal/billing/balance"
	"ddyidc/pkg/auth"
	"ddyidc/pkg/db"
	"errors"
	"net/http"
	"strconv"
	"time"
)

func (h *authHandler) balanceStatement(w http.ResponseWriter, r *http.Request) {
	claims, ok := h.sessionClaims(w, r, auth.SessionUser)
	if !ok {
		return
	}
	q := r.URL.Query()
	for _, key := range []string{"customerId", "customer_id", "userId", "user_id"} {
		if q.Has(key) {
			writeError(w, 400, "不允许指定客户身份")
			return
		}
	}
	f := balance.StatementFilter{Page: 1, PageSize: 20, Type: q.Get("type"), Direction: q.Get("direction"), Keyword: q.Get("keyword")}
	for key, dest := range map[string]*int{"page": &f.Page, "pageSize": &f.PageSize} {
		if q.Has(key) {
			n, e := strconv.Atoi(q.Get(key))
			if e != nil {
				writeError(w, 400, "分页参数无效")
				return
			}
			*dest = n
		}
	}
	if q.Has("snapshotVersion") {
		v, e := strconv.ParseInt(q.Get("snapshotVersion"), 10, 64)
		if e != nil {
			writeError(w, 400, "快照参数无效")
			return
		}
		f.Version = &v
	}
	for key, dest := range map[string]**time.Time{"startAt": &f.Start, "endAt": &f.End} {
		if q.Get(key) != "" {
			v, e := time.Parse(time.RFC3339Nano, q.Get(key))
			if e != nil {
				writeError(w, 400, "时间须为ISO8601")
				return
			}
			*dest = &v
		}
	}
	export := q.Get("format") == "csv"
	if q.Get("format") != "" && !export {
		writeError(w, 400, "导出格式无效")
		return
	}
	result, e := balance.StatementFor(r.Context(), db.G, claims.UserID, f, export)
	if e != nil {
		if errors.Is(e, balance.ErrInvalid) {
			writeError(w, 400, e.Error())
		} else {
			writeError(w, 503, "余额流水查询失败，请重试")
		}
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	if export {
		var csv bytes.Buffer
		if e = balance.WriteStatementCSV(&csv, result); e != nil {
			writeError(w, 503, "余额流水导出失败")
			return
		}
		w.Header().Set("Content-Type", "text/csv; charset=utf-8")
		w.Header().Set("Content-Disposition", "attachment; filename=balance-transactions.csv")
		_, _ = w.Write([]byte("\xef\xbb\xbf"))
		_, _ = w.Write(csv.Bytes())
		return
	}
	writeJSON(w, 200, result)
}
