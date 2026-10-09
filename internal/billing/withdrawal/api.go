package withdrawal

import (
	"context"
	billing "ddyidc/internal/billing/balance"
)

type Page struct {
	List  []BalanceWithdrawal `json:"list"`
	Total int64               `json:"total"`
}

func (s WithdrawalService) Get(ctx context.Context, customer, id int64) (BalanceWithdrawal, error) {
	var row BalanceWithdrawal
	err := s.DB.WithContext(ctx).Where("id=? AND customer_id=?", id, customer).Take(&row).Error
	return row, err
}
func (s WithdrawalService) List(ctx context.Context, customer int64, page, size int, status string) (Page, error) {
	out := Page{List: []BalanceWithdrawal{}}
	if customer <= 0 || page < 1 || page > 100000 || size < 1 || size > 100 {
		return out, ErrQuery
	}
	q := s.DB.WithContext(ctx).Model(&BalanceWithdrawal{}).Where("customer_id=?", customer)
	if status != "" {
		if status != "pending" && status != "approved" && status != "paid" && status != "rejected" {
			return out, ErrQuery
		}
		q = q.Where("status=?", status)
	}
	if err := q.Count(&out.Total).Error; err != nil {
		return out, err
	}
	err := q.Order("created_at DESC,id DESC").Offset((page - 1) * size).Limit(size).Find(&out.List).Error
	return out, err
}

type ReviewCommand struct {
	ID, Revision, AdminID                                                     int64
	AdminName, OperatorIP, Reason, Action, RequestID, TransactionNo, Evidence string
	ConfirmLarge                                                              bool
}

func (s WithdrawalService) Review(ctx context.Context, c ReviewCommand) (BalanceWithdrawal, error) {
	return ReviewWithdrawal(ctx, s.DB, c.ID, c.Revision, c.AdminID, c.AdminName, c.OperatorIP, c.Reason, c.Action, c.RequestID, c.TransactionNo, c.Evidence, c.ConfirmLarge)
}
func (s WithdrawalService) ConfirmRemittance(ctx context.Context, c ReviewCommand) (BalanceWithdrawal, error) {
	c.Action = "remit"
	return s.Review(ctx, c)
}
func (s WithdrawalService) Reconcile(ctx context.Context) error {
	return billing.New(s.DB).AssertReconciled(ctx)
}
