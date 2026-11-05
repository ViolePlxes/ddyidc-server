package billing

import (
	"context"
	"database/sql"
	dom "ddyidc/internal/billing/domain"
	rep "ddyidc/internal/billing/repository"
	typ "ddyidc/internal/billing/types"
	"errors"
	"gorm.io/gorm"
	"strings"
	"time"
)

type Filter struct {
	Kind, Status, Keyword string
	Page, PageSize        int
}

func (f Filter) Validate() error {
	if f.Page < 1 || f.Page > 100000 || f.PageSize < 1 || f.PageSize > 100 || len([]rune(f.Keyword)) > 100 {
		return errors.New("账单查询参数无效")
	}
	switch f.Kind {
	case "", "order", "recharge", "marketplace", "groupbuy":
	default:
		return errors.New("账单类型无效")
	}
	switch f.Status {
	case "", "pending", "paid", "closed", "failed", "review", "refunded", "partial_refund":
	default:
		return errors.New("账单状态无效")
	}
	return nil
}

type View struct {
	ID         string     `json:"id"`
	Kind       string     `json:"kind"`
	SourceID   int64      `json:"sourceId,string"`
	SourceNo   string     `json:"sourceNo"`
	Number     string     `json:"number"`
	Title      string     `json:"title"`
	Category   string     `json:"category"`
	Status     string     `json:"status"`
	Amount     string     `json:"amount"`
	PaidAmount string     `json:"paidAmount"`
	Revision   uint       `json:"revision"`
	Historical bool       `json:"historical"`
	CreatedAt  time.Time  `json:"createdAt"`
	PaidAt     *time.Time `json:"paidAt,omitempty"`
}
type LineView struct {
	Description string `json:"description"`
	Amount      string `json:"amount"`
}
type Detail struct {
	View
	Items []LineView `json:"items"`
}
type readRow struct {
	dom.Invoice
	Title, Category, EffectiveStatus string
}

// A late receipt on a superseded revision blocks the current unpaid bill too.
// Match the original payment source so an unmatched verified receipt is not
// advertised as payable merely because its old invoice link is unavailable.
const effectiveStatusSQL = `CASE WHEN i.status='open' AND EXISTS(
 SELECT 1 FROM payment.payments p WHERE p.customer_id=i.customer_id
 AND p.biz_id=i.source_id AND p.status='received_review'
 AND (p.biz_type=i.source_type OR (i.source_type='order' AND p.biz_type='marketplace'))
 ) THEN 'review' ELSE i.status END`

func view(row readRow) View {
	status := row.Status
	if row.EffectiveStatus != "" {
		status = row.EffectiveStatus
	}
	if status == "open" {
		status = "pending"
	}
	if status == "void" {
		status = "closed"
	}
	return View{ID: row.ID, Kind: row.SourceType, SourceID: row.SourceID, SourceNo: row.SourceNo, Number: row.InvoiceNo, Title: row.Title, Category: row.Category, Status: status, Amount: row.AmountDue.String(), PaidAmount: row.AmountPaid.String(), Revision: row.Revision, Historical: row.Historical, CreatedAt: row.CreatedAt, PaidAt: row.PaidAt}
}
func query(db *gorm.DB, user int64) *gorm.DB {
	return db.Table("billing.payable_invoices AS i").Select("i.*,COALESCE((SELECT description FROM billing.invoice_items WHERE invoice_id=i.id AND module_type<>'adjustment' ORDER BY id LIMIT 1),'产品与服务') title,COALESCE((SELECT module_type FROM billing.invoice_items WHERE invoice_id=i.id AND module_type<>'adjustment' ORDER BY id LIMIT 1),i.source_type) category,"+effectiveStatusSQL+" effective_status").Where("i.customer_id=?", user)
}
func List(ctx context.Context, db *gorm.DB, user int64, f Filter) ([]View, int64, error) {
	if user <= 0 {
		return nil, 0, typ.ErrNotFound
	}
	if e := f.Validate(); e != nil {
		return nil, 0, e
	}
	rows := []readRow{}
	var total int64
	e := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		q := query(tx, user)
		if f.Kind != "" {
			q = q.Where("i.source_type=?", f.Kind)
		}
		status := f.Status
		if status == "pending" {
			status = "open"
		}
		if status == "closed" {
			status = "void"
		}
		if status != "" {
			q = q.Where(effectiveStatusSQL+"=?", status)
		}
		if key := strings.TrimSpace(f.Keyword); key != "" {
			key = "%" + strings.NewReplacer("!", "!!", "%", "!%", "_", "!_").Replace(key) + "%"
			q = q.Where("(i.invoice_no LIKE ? ESCAPE '!' OR i.source_no LIKE ? ESCAPE '!' OR EXISTS(SELECT 1 FROM billing.invoice_items li WHERE li.invoice_id=i.id AND li.description LIKE ? ESCAPE '!'))", key, key, key)
		}
		if e := q.Count(&total).Error; e != nil {
			return e
		}
		return q.Order("i.created_at DESC,i.id DESC").Offset((f.Page - 1) * f.PageSize).Limit(f.PageSize).Scan(&rows).Error
	}, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	out := make([]View, 0, len(rows))
	for _, row := range rows {
		out = append(out, view(row))
	}
	return out, total, e
}
func Get(ctx context.Context, db *gorm.DB, user int64, id string) (Detail, error) {
	var out Detail
	e := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var row readRow
		if e := query(tx, user).Where("i.id=?", id).Take(&row).Error; e != nil {
			if errors.Is(e, gorm.ErrRecordNotFound) {
				return typ.ErrNotFound
			}
			return e
		}
		out.View = view(row)
		var lines []dom.InvoiceItem
		if e := tx.Where("invoice_id=?", id).Order("id").Find(&lines).Error; e != nil {
			return e
		}
		out.Items = make([]LineView, 0, len(lines))
		for _, line := range lines {
			out.Items = append(out.Items, LineView{Description: line.Description, Amount: line.Amount.String()})
		}
		return nil
	}, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	return out, e
}

// Resolve never issues a bill. A stale revision must not charge its replacement.
func Resolve(ctx context.Context, db *gorm.DB, user int64, id string) (dom.Invoice, error) {
	var doc dom.Invoice
	e := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if e := rep.EnabledTx(tx); e != nil {
			return e
		}
		candidate, e := rep.FindInvoiceTx(tx, id, user, false)
		if e != nil {
			return e
		}
		source, e := rep.ReadSourceTx(tx, candidate.SourceType, candidate.SourceID, user)
		if e != nil {
			return e
		}
		doc, e = rep.FindInvoiceTx(tx, id, user, true)
		if e != nil {
			return e
		}
		var binding dom.InvoiceBinding
		if e = tx.Where("source_type=? AND source_id=? AND invoice_id=?", doc.SourceType, doc.SourceID, id).Take(&binding).Error; e != nil {
			return typ.ErrConflict
		}
		if doc.Status != "open" || source.Status != "open" || source.Amount != doc.AmountDue {
			return typ.ErrConflict
		}
		return nil
	})
	return doc, e
}
