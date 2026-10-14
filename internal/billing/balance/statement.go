package billing

import (
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"gorm.io/gorm"
	"io"
	"math/big"
	"strconv"
	"strings"
	"time"
)

var TypeLabels = map[string]string{
	"recharge": "充值", "manual_credit": "后台人工增加余额", "manual_debit": "后台人工扣减余额", "freeze": "冻结余额", "unfreeze": "解除冻结", "order_pay": "产品订购", "instance_renewal_pay": "产品续费", "instance_upgrade_pay": "产品配置变更", "refund": "退款入账", "withdraw": "提现扣款", "overpayment": "多付入账", "underpayment": "补扣费用", "points_credit": "积分兑换余额", "commission": "佣金入账", "marketplace_income": "交易市场收入", "withdrawal_hold": "提现申请冻结", "withdrawal_release": "提现冻结释放", "promo_coupon_pay": "优惠券购买", "promo_coupon_refund": "优惠券退款",
}

type StatementFilter struct {
	Page, PageSize           int
	Type, Direction, Keyword string
	Start, End               *time.Time
	Version                  *int64
}
type StatementRow struct {
	ID            string    `json:"id"`
	Type          string    `json:"type"`
	TypeLabel     string    `json:"typeLabel"`
	Amount        string    `json:"amount"`
	BalanceBefore string    `json:"balanceBefore"`
	BalanceAfter  string    `json:"balanceAfter"`
	FrozenBefore  string    `json:"frozenBefore"`
	FrozenAfter   string    `json:"frozenAfter"`
	Version       int64     `json:"accountVersion"`
	CreatedAt     time.Time `json:"createdAt"`
	Description   string    `json:"description"`
	RelatedID     string    `json:"relatedId,omitempty"`
	RelatedType   string    `json:"relatedType,omitempty"`
	Href          string    `json:"href,omitempty"`
}
type StatementAccount struct {
	Exists  bool   `json:"exists"`
	Balance string `json:"balance"`
	Frozen  string `json:"frozenBalance"`
	Version int64  `json:"version"`
}
type Statement struct {
	Items           []StatementRow   `json:"items"`
	Total           int64            `json:"total"`
	Page            int              `json:"page"`
	PageSize        int              `json:"pageSize"`
	SnapshotVersion int64            `json:"snapshotVersion"`
	AsOf            time.Time        `json:"asOf"`
	Account         StatementAccount `json:"account"`
	Integrity       string           `json:"integrity"`
	Increase        string           `json:"increase"`
	Decrease        string           `json:"decrease"`
	Net             string           `json:"net"`
	PageNet         string           `json:"pageNet"`
}

func statementRow(t Transaction) StatementRow {
	label := TypeLabels[t.Type]
	if label == "" {
		label = "其他余额变动"
	}
	if strings.HasPrefix(t.TxNo, "v10:") && strings.Contains(t.TxNo, ":opening:") {
		label = "迁移期初余额"
	}
	if strings.HasPrefix(t.TxNo, "v10d:") {
		label = "迁移余额调整"
	}
	desc := strings.TrimSpace(t.ClientNotes)
	if desc == "" {
		desc = label
	}
	row := StatementRow{ID: strconv.FormatInt(t.ID, 10), Type: t.Type, TypeLabel: label, Amount: t.Amount.String(), BalanceBefore: t.BalanceBefore.String(), BalanceAfter: t.BalanceAfter.String(), FrozenBefore: t.FrozenBefore.String(), FrozenAfter: t.FrozenAfter.String(), Version: t.AccountVersion, CreatedAt: t.CreatedAt, Description: desc}
	// Only locally authorized, customer-owned order routes are exposed. Migration
	// customer references and supplier identifiers are not a client-visible link.
	if t.RelatedType == "order" {
		row.RelatedType = "order"
		row.RelatedID = strconv.FormatInt(t.RelatedID, 10)
		row.Href = "/console/orders/" + row.RelatedID
	}
	if t.RelatedType == "balance_withdrawal" {
		row.RelatedType = "balance_withdrawal"
		row.RelatedID = strconv.FormatInt(t.RelatedID, 10)
	}
	return row
}
func decimalTotal(cents *big.Int) string {
	sign := ""
	n := new(big.Int).Set(cents)
	if n.Sign() < 0 {
		sign = "-"
		n.Neg(n)
	}
	whole, frac := new(big.Int), new(big.Int)
	whole.QuoRem(n, big.NewInt(100), frac)
	return fmt.Sprintf("%s%s.%02d", sign, whole.String(), frac.Int64())
}
func StatementFor(ctx context.Context, db *gorm.DB, customer int64, f StatementFilter, export bool) (Statement, error) {
	out := Statement{Items: []StatementRow{}, Page: f.Page, PageSize: f.PageSize, AsOf: time.Now(), Integrity: "consistent"}
	if customer <= 0 || f.Page < 1 || f.Page > 100000 || f.PageSize < 1 || f.PageSize > 100 || len([]rune(f.Keyword)) > 100 || f.Direction != "" && f.Direction != "increase" && f.Direction != "decrease" {
		return out, ErrInvalid
	}
	if f.Type != "" && TypeLabels[f.Type] == "" {
		return out, ErrInvalid
	}
	if f.Start != nil && f.End != nil && !f.End.After(*f.Start) {
		return out, ErrInvalid
	}
	err := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var account Account
		err := tx.Where("customer_id=?", customer).Take(&account).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			account = Account{CustomerID: customer}
		} else if err != nil {
			return err
		}
		if err = InspectAccountLedger(tx, account); err != nil {
			if !errors.Is(err, ErrLedgerIntegrity) {
				return err
			}
			out.Integrity = "needs_review"
		}
		var ledger struct{ Version int64 }
		if err = tx.Model(&Transaction{}).Select("COALESCE(MAX(account_version),0) version").Where("customer_id=?", customer).Scan(&ledger).Error; err != nil {
			return err
		}
		currentVersion := account.Version
		if ledger.Version > currentVersion {
			currentVersion = ledger.Version
		}
		version := currentVersion
		if f.Version != nil {
			version = *f.Version
			if version < 0 || version > currentVersion {
				return ErrInvalid
			}
		}
		out.SnapshotVersion = version
		snapshotAccount := account
		if version < currentVersion {
			snapshotAccount.Balance, snapshotAccount.FrozenBalance, snapshotAccount.Version = 0, 0, version
			if version > 0 {
				var t Transaction
				if err = tx.Where("customer_id=? AND account_version=?", customer, version).Take(&t).Error; err != nil {
					return err
				}
				snapshotAccount.Balance = t.BalanceAfter
				snapshotAccount.FrozenBalance = t.FrozenAfter
				out.AsOf = t.CreatedAt
			}
		}
		out.Account = StatementAccount{Exists: account.ID > 0, Balance: snapshotAccount.Balance.String(), Frozen: snapshotAccount.FrozenBalance.String(), Version: snapshotAccount.Version}
		base := tx.Model(&Transaction{}).Where("customer_id=? AND account_version<=?", customer, version)
		if f.Type != "" {
			base = base.Where("type=?", f.Type)
		}
		if f.Direction == "increase" {
			base = base.Where("amount>0")
		}
		if f.Direction == "decrease" {
			base = base.Where("amount<0")
		}
		if f.Start != nil {
			base = base.Where("created_at>=?", *f.Start)
		}
		if f.End != nil {
			base = base.Where("created_at<?", *f.End)
		}
		if f.Keyword != "" {
			base = base.Where("CAST(id AS CHAR)=? OR client_notes LIKE ?", f.Keyword, "%"+strings.ReplaceAll(strings.ReplaceAll(strings.ReplaceAll(f.Keyword, "\\", "\\\\"), "%", "\\%"), "_", "\\_")+"%")
		}
		if err = base.Session(&gorm.Session{}).Count(&out.Total).Error; err != nil {
			return err
		}
		if export && out.Total > 10000 {
			return fmt.Errorf("%w: 导出最多10000条，请缩小查询范围", ErrInvalid)
		}
		var totals struct{ Increase, Decrease string }
		if err = base.Session(&gorm.Session{}).Select("CAST(COALESCE(SUM(CASE WHEN amount>0 THEN amount ELSE 0 END),0) AS CHAR) increase,CAST(COALESCE(SUM(CASE WHEN amount<0 THEN -amount ELSE 0 END),0) AS CHAR) decrease").Scan(&totals).Error; err != nil {
			return err
		}
		// DECIMAL SUM may exceed one transaction's Money bound. Parse exact cents with big.Int.
		parse := func(v string) (*big.Int, error) {
			parts := strings.Split(v, ".")
			if len(parts) > 2 {
				return nil, ErrInvalid
			}
			frac := "00"
			if len(parts) == 2 {
				frac = parts[1]
				if len(frac) > 2 {
					return nil, ErrInvalid
				}
				frac += strings.Repeat("0", 2-len(frac))
			}
			n, ok := new(big.Int).SetString(parts[0]+frac, 10)
			if !ok {
				return nil, ErrInvalid
			}
			return n, nil
		}
		inc, e := parse(totals.Increase)
		if e != nil {
			return e
		}
		dec, e := parse(totals.Decrease)
		if e != nil {
			return e
		}
		out.Increase = decimalTotal(inc)
		out.Decrease = decimalTotal(dec)
		out.Net = decimalTotal(new(big.Int).Sub(inc, dec))
		q := base.Session(&gorm.Session{}).Order("created_at DESC,account_version DESC,id DESC")
		if !export {
			q = q.Offset((f.Page - 1) * f.PageSize).Limit(f.PageSize)
		}
		var rows []Transaction
		if err = q.Find(&rows).Error; err != nil {
			return err
		}
		orderIDs := []int64{}
		for _, t := range rows {
			if t.RelatedType == "order" {
				orderIDs = append(orderIDs, t.RelatedID)
			}
		}
		allowedOrders := map[int64]bool{}
		if len(orderIDs) > 0 {
			var ids []int64
			if err = tx.Table("`order`.orders").Where("id IN ? AND customer_id=?", orderIDs, customer).Pluck("id", &ids).Error; err != nil {
				return err
			}
			for _, id := range ids {
				allowedOrders[id] = true
			}
		}
		pageNet := new(big.Int)
		for _, t := range rows {
			row := statementRow(t)
			if t.RelatedType == "order" && !allowedOrders[t.RelatedID] {
				row.Href = ""
				row.RelatedID = ""
				row.RelatedType = ""
			}
			out.Items = append(out.Items, row)
			pageNet.Add(pageNet, big.NewInt(int64(t.Amount)))
		}
		out.PageNet = decimalTotal(pageNet)
		return nil
	})
	return out, err
}
func WriteStatementCSV(w io.Writer, s Statement) error {
	c := csv.NewWriter(w)
	safe := func(v string) string {
		trim := strings.TrimLeft(v, " \t\r\n")
		if trim != "" && strings.ContainsAny(trim[:1], "=+-@") || strings.ContainsAny(v, "\t\r\n") {
			return "'" + v
		}
		return v
	}
	if err := c.Write([]string{"流水编号", "时间（北京时间）", "类型", "可用余额变动（元）", "变动前可用余额", "变动后可用余额", "冻结前", "冻结后", "客户说明"}); err != nil {
		return err
	}
	zone := time.FixedZone("Asia/Shanghai", 8*3600)
	for _, r := range s.Items {
		if err := c.Write([]string{safe(r.ID), r.CreatedAt.In(zone).Format("2006-01-02 15:04:05"), safe(r.TypeLabel), safe(r.Amount), r.BalanceBefore, r.BalanceAfter, r.FrozenBefore, r.FrozenAfter, safe(r.Description)}); err != nil {
			return err
		}
	}
	c.Flush()
	return c.Error()
}
