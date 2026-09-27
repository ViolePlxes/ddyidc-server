package backup

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
)

type RecoveryFact struct {
	Kind      string `json:"kind"`
	ID        string `json:"id"`
	State     string `json:"state"`
	Reference string `json:"reference"`
	Amount    string `json:"amount"`
}
type RecoveryDifference struct {
	Kind       string        `json:"kind"`
	ID         string        `json:"id"`
	Before     *RecoveryFact `json:"before"`
	Restored   *RecoveryFact `json:"restored"`
	Resolution string        `json:"resolution"`
	Notes      string        `json:"notes"`
}
type RecoveryReview struct {
	ExternalChecked bool                 `json:"externalChecked"`
	Notes           string               `json:"notes"`
	Differences     []RecoveryDifference `json:"differences"`
}

func (s *Service) recoveryFacts(ctx context.Context) ([]RecoveryFact, error) {
	queries := []string{
		"SELECT 'wallet' AS kind,CAST(customer_id AS CHAR) AS id,CAST(version AS CHAR) AS state,CAST(frozen_balance AS CHAR) AS reference,CAST(balance AS CHAR) AS amount FROM billing.accounts",
		"SELECT 'ledger' AS kind,CAST(id AS CHAR) AS id,type AS state,tx_no AS reference,CAST(amount AS CHAR) AS amount FROM billing.transactions",
		"SELECT 'receipt' AS kind,CAST(payment_id AS CHAR) AS id,'received' AS state,transaction_id AS reference,CAST(amount_fen AS CHAR) AS amount FROM payment.receipts",
		"SELECT 'refund' AS kind,CAST(payment_id AS CHAR) AS id,status AS state,request_no AS reference,CAST(amount AS CHAR) AS amount FROM payment.original_refunds",
		"SELECT 'procurement' AS kind,CAST(id AS CHAR) AS id,status AS state,CONCAT(upstream_order_id,':',COALESCE(upstream_host_id,'')) AS reference,CAST(cost AS CHAR) AS amount FROM `order`.unit_procurements",
		"SELECT 'legacy_procurement' AS kind,CAST(id AS CHAR) AS id,status AS state,CONCAT(upstream_order_id,':',COALESCE(upstream_host_id,'')) AS reference,CAST(cost AS CHAR) AS amount FROM `order`.upstream_orders",
		"SELECT 'renewal' AS kind,CAST(id AS CHAR) AS id,state,invoice_id AS reference,CAST(amount AS CHAR) AS amount FROM instance.instance_renewals",
		"SELECT 'instance' AS kind,CAST(id AS CHAR) AS id,CAST(status AS CHAR) AS state,CONCAT(COALESCE(upstream_provider_id,0),':',upstream_instance_id,':',customer_id,':',ownership_version) AS reference,COALESCE(CAST(expires_at AS CHAR),'') AS amount FROM instance.instances WHERE deleted_at IS NULL",
		"SELECT 'order' AS kind,CAST(id AS CHAR) AS id,CAST(status AS CHAR) AS state,order_no AS reference,CAST(pay_amount AS CHAR) AS amount FROM `order`.orders",
	}
	facts := []RecoveryFact{}
	for _, query := range queries {
		var rows []RecoveryFact
		if err := s.db.WithContext(ctx).Raw(query + " LIMIT 100001").Scan(&rows).Error; err != nil {
			return nil, errors.New("读取恢复核对事实失败")
		}
		if len(rows) > 100000 {
			return nil, errors.New("核对事实超出自动清单容量，需离线恢复")
		}
		facts = append(facts, rows...)
	}
	return facts, nil
}

func differences(before, restored []RecoveryFact) []RecoveryDifference {
	old := map[string]RecoveryFact{}
	for _, f := range before {
		old[f.Kind+":"+f.ID] = f
	}
	out := []RecoveryDifference{}
	for _, f := range restored {
		key := f.Kind + ":" + f.ID
		previous, ok := old[key]
		if !ok {
			copy := f
			out = append(out, RecoveryDifference{Kind: f.Kind, ID: f.ID, Restored: &copy, Resolution: "pending"})
		} else if previous != f {
			copy := f
			out = append(out, RecoveryDifference{Kind: f.Kind, ID: f.ID, Before: &previous, Restored: &copy, Resolution: "pending"})
		}
		delete(old, key)
	}
	for _, f := range old {
		copy := f
		out = append(out, RecoveryDifference{Kind: f.Kind, ID: f.ID, Before: &copy, Resolution: "pending"})
	}
	return out
}

func writeReview(base string, before, restored []RecoveryFact) error {
	review := RecoveryReview{Differences: differences(before, restored)}
	raw, err := json.MarshalIndent(review, "", "  ")
	if err != nil {
		return err
	}
	return writeDurable(filepath.Join(base, "reconciliation.json"), raw)
}
