package billing

import (
	"context"
	"ddyidc/internal/shared/distsync"
	"fmt"
	"log"
	"time"
)

type ReconciliationMismatch struct {
	Scope         string
	CustomerID    int64
	Balance       Money
	LedgerBalance Money
	FrozenBalance Money
	LedgerFrozen  Money
}

func (s *BalanceService) Reconcile(ctx context.Context) ([]ReconciliationMismatch, error) {
	rows := []ReconciliationMismatch{}
	err := s.db.WithContext(ctx).Raw(`SELECT a.customer_id,a.balance,COALESCE(t.net,0) ledger_balance,a.frozen_balance,COALESCE(f.frozen,0) ledger_frozen
 FROM billing.accounts a LEFT JOIN (SELECT customer_id,SUM(amount) net FROM billing.transactions GROUP BY customer_id) t ON t.customer_id=a.customer_id
 LEFT JOIN (SELECT f.customer_id,SUM(-f.amount) frozen FROM billing.transactions f WHERE f.type IN ('freeze','withdrawal_hold')
 AND NOT EXISTS (SELECT 1 FROM billing.transactions u WHERE (u.type='unfreeze' AND u.related_type='freeze' AND u.related_id=f.id) OR (f.type='withdrawal_hold' AND u.type='withdrawal_release' AND u.related_type='balance_withdrawal' AND u.related_id=f.related_id))
 GROUP BY f.customer_id) f ON f.customer_id=a.customer_id
 WHERE a.balance<>COALESCE(t.net,0) OR a.frozen_balance<>COALESCE(f.frozen,0)`).Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	var orphan []ReconciliationMismatch
	err = s.db.WithContext(ctx).Raw("SELECT t.customer_id,0 balance,SUM(t.amount) ledger_balance,0 frozen_balance,0 ledger_frozen FROM billing.transactions t LEFT JOIN billing.accounts a ON a.customer_id=t.customer_id WHERE a.id IS NULL GROUP BY t.customer_id").Scan(&orphan).Error
	if err != nil {
		return nil, err
	}
	rows = append(rows, orphan...)
	var walletRows []ReconciliationMismatch
	err = s.db.WithContext(ctx).Raw(`SELECT 'marketplace_wallet' scope,w.user_id customer_id,w.balance,
 COALESCE(f.opening,0)+COALESCE(f.delta,0) ledger_balance,w.frozen_balance,
 COALESCE(f.opening_frozen,0)+COALESCE(f.frozen_delta,0) ledger_frozen
 FROM system.marketplace_wallet w LEFT JOIN (
 SELECT user_id,MAX(CASE WHEN account_version=1 THEN balance_before END) opening,
 MAX(CASE WHEN account_version=1 THEN frozen_before END) opening_frozen,
 SUM(balance_after-balance_before) delta,SUM(frozen_after-frozen_before) frozen_delta,
 MAX(account_version) last_version,COUNT(*) entries
 FROM system.marketplace_flows WHERE account_version>0 GROUP BY user_id
 ) f ON f.user_id=w.user_id WHERE w.version>0 AND
 (w.balance<>COALESCE(f.opening,0)+COALESCE(f.delta,0)
 OR w.frozen_balance<>COALESCE(f.opening_frozen,0)+COALESCE(f.frozen_delta,0)
 OR w.version<>COALESCE(f.last_version,0) OR w.version<>COALESCE(f.entries,0))`).Scan(&walletRows).Error
	return append(rows, walletRows...), err
}

func (s *BalanceService) StartReconciliation(ctx context.Context) {
	go func() {
		run := func() {
			distsync.Task(ctx, "balance_reconcile", time.Minute, func(ctx context.Context) error {
				rows, err := s.Reconcile(ctx)
				if err != nil {
					log.Printf("billing reconciliation failed: %v", err)
					return err
				}
				for _, row := range rows {
					log.Printf("BILLING_ALERT reconciliation scope=%s customer=%d balance=%s ledger=%s frozen=%s ledger_frozen=%s", row.Scope, row.CustomerID, row.Balance.String(), row.LedgerBalance.String(), row.FrozenBalance.String(), row.LedgerFrozen.String())
				}
				return nil
			})
		}
		run()
		ticker := time.NewTicker(24 * time.Hour)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				run()
			}
		}
	}()
}

func (s *BalanceService) CheckManualFrequency(ctx context.Context, customerID int64) error {
	var count int64
	if err := s.db.WithContext(ctx).Model(&Transaction{}).Where("customer_id=? AND channel='admin_manual' AND created_at>=?", customerID, time.Now().Add(-time.Minute)).Count(&count).Error; err != nil {
		return err
	}
	if count >= 5 {
		log.Printf("BILLING_ALERT high frequency customer=%d operations=%d window=60s", customerID, count)
	}
	return nil
}

func (s *BalanceService) AssertReconciled(ctx context.Context) error {
	rows, err := s.Reconcile(ctx)
	if err != nil {
		return err
	}
	if len(rows) != 0 {
		return fmt.Errorf("billing reconciliation: %d mismatched accounts", len(rows))
	}
	return nil
}
