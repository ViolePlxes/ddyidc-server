package customer

import (
	"ddyidc/pkg/auth"
	"ddyidc/pkg/db"
	"encoding/json"
	"net/http"
	"strconv"
)

type legacyHistoryRow struct {
	ID           int64          `json:"id,string"`
	SourceID     int64          `json:"sourceId,string"`
	OrderID      *int64         `json:"-"`
	Kind         string         `json:"kind"`
	Amount       string         `json:"amountYuan"`
	BalanceAfter string         `json:"balanceAfterYuan"`
	Payload      string         `json:"-"`
	Data         map[string]any `json:"data" gorm:"-"`
}

var historyPublicFields = map[string][]string{
	"balance": {"type", "order_id", "create_time", "client_notes"},
	"payment": {"order_id", "gateway", "gateway_name", "transaction_number", "create_time"},
	"ticket":  {"ticket_num", "title", "content", "attachment", "create_time", "update_time", "last_reply_time", "status_name", "type_name"},
	"reply":   {"ticket_id", "type", "content", "attachment", "create_time", "quote_reply_id"},
}

func publicHistoryRows(rows []legacyHistoryRow) {
	for i := range rows {
		rows[i].SourceID = rows[i].ID
		var payload map[string]any
		_ = json.Unmarshal([]byte(rows[i].Payload), &payload)
		rows[i].Data = map[string]any{}
		for _, key := range historyPublicFields[rows[i].Kind] {
			rows[i].Data[key] = payload[key]
		}
		if rows[i].Kind == "ticket" {
			rows[i].Data["ticket_num"] = strconv.FormatInt(rows[i].ID, 10)
		}
		if rows[i].Kind == "reply" {
			delete(rows[i].Data, "ticket_id")
			delete(rows[i].Data, "quote_reply_id")
		}
		if rows[i].Kind == "balance" || rows[i].Kind == "payment" {
			rows[i].SourceID = rows[i].ID
			rows[i].Data["order_id"] = "0"
			if rows[i].OrderID != nil {
				rows[i].Data["order_id"] = strconv.FormatInt(*rows[i].OrderID, 10)
			}
		}
	}
}
func (h *authHandler) legacyHistory(w http.ResponseWriter, r *http.Request) {
	claims, ok := h.sessionClaims(w, r, auth.SessionUser)
	if !ok {
		return
	}
	kind := r.URL.Query().Get("kind")
	if kind != "balance" && kind != "payment" && kind != "ticket" {
		writeError(w, 400, "历史记录类型无效")
		return
	}
	page, size := 1, 20
	if n, e := strconv.Atoi(r.URL.Query().Get("page")); e == nil && n > 0 && n <= 100000 {
		page = n
	}
	if n, e := strconv.Atoi(r.URL.Query().Get("pageSize")); e == nil && n > 0 && n <= 100 {
		size = n
	}
	rows := []legacyHistoryRow{}
	var total, tables int64
	if err := db.G.Raw("SELECT COUNT(*) FROM information_schema.TABLES WHERE TABLE_SCHEMA='system' AND TABLE_NAME='v10_history_records'").Scan(&tables).Error; err != nil {
		writeError(w, 500, "历史记录查询失败")
		return
	}
	if tables > 0 {
		q := db.G.WithContext(r.Context()).Table("system.v10_history_records").Where("customer_id=? AND kind=? AND read_only=1", claims.UserID, kind)
		if err := q.Count(&total).Error; err != nil {
			writeError(w, 500, "历史记录查询失败")
			return
		}
		if err := q.Order("created_at DESC,id DESC").Offset((page - 1) * size).Limit(size).Find(&rows).Error; err != nil {
			writeError(w, 500, "历史记录查询失败")
			return
		}
	}
	publicHistoryRows(rows)
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, map[string]any{"list": rows, "total": total, "page": page, "pageSize": size, "readOnly": true})
}
func (h *authHandler) legacyTicket(w http.ResponseWriter, r *http.Request) {
	claims, ok := h.sessionClaims(w, r, auth.SessionUser)
	if !ok {
		return
	}
	id, err := strconv.ParseInt(r.URL.Query().Get("id"), 10, 64)
	if err != nil || id <= 0 {
		writeError(w, 400, "历史工单标识无效")
		return
	}
	rows := []legacyHistoryRow{}
	q := db.G.WithContext(r.Context()).Table("system.v10_history_records").Where("customer_id=? AND kind='ticket' AND id=? AND read_only=1", claims.UserID, id)
	if err := q.Find(&rows).Error; err != nil {
		writeError(w, 500, "历史工单查询失败")
		return
	}
	if len(rows) != 1 {
		writeError(w, 404, "历史工单不存在")
		return
	}
	replies := []legacyHistoryRow{}
	if err := db.G.WithContext(r.Context()).Table("system.v10_history_records").Where("customer_id=? AND kind='reply' AND source_ticket_id=? AND read_only=1", claims.UserID, rows[0].SourceID).Order("created_at,source_id").Find(&replies).Error; err != nil {
		writeError(w, 500, "历史回复查询失败")
		return
	}
	publicHistoryRows(rows)
	publicHistoryRows(replies)
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, map[string]any{"ticket": rows[0], "replies": replies, "readOnly": true})
}
