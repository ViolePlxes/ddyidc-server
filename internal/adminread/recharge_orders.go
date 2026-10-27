package adminread

import (
	"errors"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/zeromicro/go-zero/rest/pathvar"
	"gorm.io/gorm"
)

type rechargeRow struct {
	ID             string     `json:"id"`
	RechargeNo     string     `json:"rechargeNo"`
	CustomerID     string     `json:"customerId"`
	Username       string     `json:"username"`
	Amount         string     `json:"amount"`
	Bonus          string     `json:"bonus"`
	Channel        string     `json:"channel"`
	Status         uint8      `json:"status"`
	PaidAt         *time.Time `json:"paidAt"`
	CreatedAt      time.Time  `json:"createdAt"`
	UpdatedAt      time.Time  `json:"updatedAt"`
	PaymentNo      string     `json:"paymentNo"`
	PaymentStatus  string     `json:"paymentStatus"`
	CreditCount    int64      `json:"creditCount"`
	CreditedAmount string     `json:"creditedAmount"`
	CreditedAt     *time.Time `json:"creditedAt"`
}
type rechargePayment struct {
	ID            string     `json:"id"`
	PaymentNo     string     `json:"paymentNo"`
	Channel       string     `json:"channel"`
	OutTradeNo    string     `json:"outTradeNo"`
	TransactionID string     `json:"transactionId"`
	Amount        string     `json:"amount"`
	Status        string     `json:"status"`
	PaidAt        *time.Time `json:"paidAt"`
	CreatedAt     time.Time  `json:"createdAt"`
}
type rechargeCredit struct {
	ID        string    `json:"id"`
	TxNo      string    `json:"txNo"`
	Amount    string    `json:"amount"`
	CreatedAt time.Time `json:"createdAt"`
}
type rechargeFilter struct {
	Status           *uint8
	Channel, Keyword string
}

var rechargeChannel = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,32}$`)

func parseRechargeFilter(r *http.Request) (rechargeFilter, error) {
	status, ok := optionalStatus(r, 1, 4)
	if !ok {
		return rechargeFilter{}, errors.New("充值状态筛选无效")
	}
	f := rechargeFilter{Status: status, Channel: strings.TrimSpace(r.URL.Query().Get("channel")), Keyword: strings.TrimSpace(r.URL.Query().Get("keyword"))}
	if f.Channel != "" && !rechargeChannel.MatchString(f.Channel) {
		return f, errors.New("充值渠道筛选无效")
	}
	if utf8.RuneCountInString(f.Keyword) > 200 {
		return f, errors.New("搜索关键词不能超过200字")
	}
	return f, nil
}
func validRechargeID(raw string) bool {
	id, err := strconv.ParseInt(raw, 10, 64)
	return err == nil && id > 0 && strconv.FormatInt(id, 10) == raw
}

// All reads are local SELECTs. Never invoke payment status polling, reconciliation or balance services here.
func rechargeQuery(db *gorm.DB, f rechargeFilter) *gorm.DB {
	q := db.Table("payment.recharge_orders r").Joins("LEFT JOIN customer.customers c ON c.id=r.customer_id")
	if f.Status != nil {
		q = q.Where("r.status=?", *f.Status)
	}
	if f.Channel != "" {
		q = q.Where("r.channel=?", f.Channel)
	}
	if f.Keyword != "" {
		like := "%" + strings.NewReplacer("!", "!!", "%", "!%", "_", "!_").Replace(f.Keyword) + "%"
		q = q.Where(`(r.recharge_no LIKE ? ESCAPE '!' OR CAST(r.id AS CHAR) LIKE ? ESCAPE '!' OR CAST(r.customer_id AS CHAR) LIKE ? ESCAPE '!' OR c.username LIKE ? ESCAPE '!' OR c.phone LIKE ? ESCAPE '!' OR c.email LIKE ? ESCAPE '!' OR EXISTS(SELECT 1 FROM payment.payments sp WHERE sp.biz_type='recharge' AND sp.biz_id=r.id AND sp.customer_id=r.customer_id AND (sp.payment_no LIKE ? ESCAPE '!' OR sp.out_trade_no LIKE ? ESCAPE '!' OR sp.transaction_id LIKE ? ESCAPE '!')))`, like, like, like, like, like, like, like, like, like)
	}
	return q
}

const rechargeLedgerMatch = " FROM billing.transactions bt JOIN payment.payments lp ON lp.id=bt.related_id AND lp.biz_type='recharge' AND lp.biz_id=r.id AND lp.customer_id=r.customer_id WHERE bt.related_type='payment' AND bt.customer_id=r.customer_id AND bt.type='recharge' AND bt.direction=1"
const rechargeProjection = "CAST(r.id AS CHAR) id,r.recharge_no,CAST(r.customer_id AS CHAR) customer_id,CAST(r.customer_id AS CHAR) username,CAST(r.amount/100 AS DECIMAL(14,2)) amount,CAST(COALESCE(r.bonus,0)/100 AS DECIMAL(14,2)) bonus,r.channel,r.status,r.paid_at,r.created_at,r.updated_at," +
	"COALESCE((SELECT p.payment_no FROM payment.payments p WHERE p.biz_type='recharge' AND p.biz_id=r.id AND p.customer_id=r.customer_id ORDER BY p.created_at DESC,p.id DESC LIMIT 1),'') payment_no," +
	"COALESCE((SELECT p.status FROM payment.payments p WHERE p.biz_type='recharge' AND p.biz_id=r.id AND p.customer_id=r.customer_id ORDER BY p.created_at DESC,p.id DESC LIMIT 1),'') payment_status," +
	"(SELECT COUNT(*)" + rechargeLedgerMatch + ") credit_count,(SELECT CAST(COALESCE(SUM(bt.amount),0) AS DECIMAL(14,2))" + rechargeLedgerMatch + ") credited_amount,(SELECT MIN(bt.created_at)" + rechargeLedgerMatch + ") credited_at"

func (h *handler) rechargeOrders(w http.ResponseWriter, r *http.Request) {
	if !h.authorize(w, r) {
		return
	}
	f, err := parseRechargeFilter(r)
	if err != nil {
		writeError(w, 400, err.Error())
		return
	}
	page, size := pageParams(r)
	q := rechargeQuery(h.db.WithContext(r.Context()), f)
	var total int64
	if err = q.Session(&gorm.Session{}).Count(&total).Error; err != nil {
		writeError(w, 500, "读取充值订单数量失败")
		return
	}
	rows := []rechargeRow{}
	if err = q.Select(rechargeProjection).Order("r.created_at DESC,r.id DESC").Offset((page - 1) * size).Limit(size).Scan(&rows).Error; err != nil {
		writeError(w, 500, "读取充值订单失败")
		return
	}
	writeJSON(w, map[string]any{"items": rows, "total": total, "page": page, "pageSize": size})
}
func (h *handler) rechargeOrderDetail(w http.ResponseWriter, r *http.Request) {
	if !h.authorize(w, r) {
		return
	}
	id := pathvar.Vars(r)["id"]
	if !validRechargeID(id) {
		writeError(w, 400, "充值订单编号无效")
		return
	}
	row := rechargeRow{}
	db := h.db.WithContext(r.Context())
	result := rechargeQuery(db, rechargeFilter{}).Where("r.id=?", id).Select(rechargeProjection).Limit(1).Scan(&row)
	if result.Error != nil {
		writeError(w, 500, "读取充值订单失败")
		return
	}
	if result.RowsAffected == 0 {
		writeError(w, 404, "充值订单不存在")
		return
	}
	payments := []rechargePayment{}
	if err := db.Table("payment.payments").Where("biz_type='recharge' AND biz_id=? AND customer_id=?", id, row.CustomerID).Select("CAST(id AS CHAR) id,payment_no,channel,out_trade_no,COALESCE(transaction_id,'') transaction_id,CAST(amount_fen/100 AS DECIMAL(14,2)) amount,status,paid_at,created_at").Order("created_at DESC,id DESC").Scan(&payments).Error; err != nil {
		writeError(w, 500, "读取充值支付记录失败")
		return
	}
	credits := []rechargeCredit{}
	if err := db.Table("billing.transactions bt").Where("bt.customer_id=? AND bt.type='recharge' AND bt.direction=1 AND bt.related_type='payment' AND EXISTS(SELECT 1 FROM payment.payments p WHERE p.id=bt.related_id AND p.biz_type='recharge' AND p.biz_id=? AND p.customer_id=bt.customer_id)", row.CustomerID, id).Select("CAST(bt.id AS CHAR) id,bt.tx_no,CAST(bt.amount AS DECIMAL(14,2)) amount,bt.created_at").Order("bt.created_at,bt.id").Scan(&credits).Error; err != nil {
		writeError(w, 500, "读取关联入账流水失败")
		return
	}
	writeJSON(w, map[string]any{"order": row, "payments": payments, "credits": credits})
}
