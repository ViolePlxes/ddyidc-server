package customer

import (
	"net/http"
	"strconv"
	"time"

	"ddyidc/pkg/auth"
	"ddyidc/pkg/db"
)

type legacyOrder struct {
	ID            int64      `json:"id,string"`
	OrderNo       string     `json:"orderNo"`
	Type          string     `json:"type"`
	Status        string     `json:"status"`
	Amount        string     `json:"amountYuan"`
	BalanceAmount string     `json:"balanceAmountYuan"`
	RefundAmount  string     `json:"refundAmountYuan"`
	CreatedAt     time.Time  `json:"createdAt"`
	PaidAt        *time.Time `json:"paidAt"`
	ProductName   string     `json:"productName"`
}

// legacyOrders is query-only. These records never enter the live order service,
// payment state machine, callback dispatch, renewal queue, or delivery engine.
func (h *authHandler) legacyOrders(w http.ResponseWriter, r *http.Request) {
	claims, ok := h.sessionClaims(w, r, auth.SessionUser)
	if !ok {
		return
	}
	page, size := 1, 10
	if n, e := strconv.Atoi(r.URL.Query().Get("page")); e == nil && n > 0 {
		page = n
	}
	if n, e := strconv.Atoi(r.URL.Query().Get("pageSize")); e == nil && n > 0 && n <= 100 {
		size = n
	}
	var tables int64
	if err := db.G.WithContext(r.Context()).Raw("SELECT COUNT(*) FROM information_schema.TABLES WHERE TABLE_SCHEMA='system' AND TABLE_NAME='v10_historical_orders'").Scan(&tables).Error; err != nil {
		writeError(w, 500, "查询历史订单失败")
		return
	}
	rows := []legacyOrder{}
	var total int64
	if tables != 0 {
		query := db.G.WithContext(r.Context()).Table("system.v10_historical_orders").Where("customer_id=? AND read_only=1", claims.UserID)
		if err := query.Count(&total).Error; err != nil {
			writeError(w, 500, "查询历史订单失败")
			return
		}
		if err := query.Select("*,COALESCE((SELECT GROUP_CONCAT(i.description ORDER BY i.source_id SEPARATOR ' / ') FROM system.v10_historical_order_items i WHERE i.order_id=system.v10_historical_orders.id),'历史订单') product_name").Order("created_at DESC,id DESC").Offset((page - 1) * size).Limit(size).Find(&rows).Error; err != nil {
			writeError(w, 500, "查询历史订单失败")
			return
		}
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, map[string]any{"list": rows, "total": total, "page": page, "pageSize": size, "readOnly": true})
}
