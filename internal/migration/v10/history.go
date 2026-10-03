package v10

import (
	"context"
	"crypto/sha256"
	balance "ddyidc/internal/billing/balance"
	"ddyidc/internal/shared/snowflake"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"gorm.io/gorm"
	"strconv"
	"strings"
	"time"
)

//go:embed history.sql
var historySQL string

type HistoryReport struct {
	Counts               map[string]int64 `json:"counts"`
	FieldMismatches      int64            `json:"field_mismatches"`
	MissingOwners        int64            `json:"missing_owners"`
	MissingTicketParents int64            `json:"missing_ticket_parents"`
	BalanceYuan          string           `json:"balance_yuan"`
	LedgerRows           int64            `json:"live_ledger_rows"`
}
type historyRecord struct {
	ID             int64
	SourceID       int64
	Kind           string
	CustomerID     int64
	SourceOrderID  int64
	SourceTicketID int64
	Amount         string
	BalanceAfter   string
	CreatedAt      time.Time
	Payload        string
	PayloadSHA256  string
	SourceSHA256   string
	ReadOnly       bool
}

var historyQueries = map[string]string{
	"balance": "SELECT id,client_id,type,amount,credit,order_id,host_id,create_time,client_notes,notes,admin_id,rel_id,is_unfreeze FROM v10_source_latest.idcsmart_client_credit ORDER BY id",
	"payment": "SELECT id,client_id,order_id,amount,gateway,gateway_name,transaction_number,create_time,host_id,notes FROM v10_source_latest.idcsmart_transaction ORDER BY id",
	"ticket":  "SELECT t.id,t.client_id,t.ticket_num,t.title,t.content,t.attachment,t.create_time,t.update_time,t.last_reply_time,t.status,t.ticket_type_id,COALESCE(s.name,'历史状态') status_name,COALESCE(y.name,'历史类型') type_name FROM v10_history_source.idcsmart_addon_idcsmart_ticket t LEFT JOIN v10_history_source.idcsmart_addon_idcsmart_ticket_status s ON s.id=t.status LEFT JOIN v10_history_source.idcsmart_addon_idcsmart_ticket_type y ON y.id=t.ticket_type_id ORDER BY t.id",
	"reply":   "SELECT r.id,t.client_id,r.ticket_id,r.type,r.rel_id,r.content,r.attachment,r.create_time,r.update_time,r.quote_reply_id FROM v10_history_source.idcsmart_addon_idcsmart_ticket_reply r JOIN v10_history_source.idcsmart_addon_idcsmart_ticket t ON t.id=r.ticket_id ORDER BY r.id",
}

func historyNumber(row map[string]any, key string) int64 {
	value := fmt.Sprint(row[key])
	n, _ := strconv.ParseInt(value, 10, 64)
	return n
}
func historyPayload(row map[string]any) (string, error) {
	for k, v := range row {
		if b, ok := v.([]byte); ok {
			row[k] = string(b)
		}
	}
	data, err := json.Marshal(row)
	return string(data), err
}
func historyHash(payload string) string {
	sum := sha256.Sum256([]byte(payload))
	return hex.EncodeToString(sum[:])
}
func historySources(source *gorm.DB) (map[string][]map[string]any, error) {
	result := map[string][]map[string]any{}
	for kind, query := range historyQueries {
		var rows []map[string]any
		if err := source.Raw(query).Scan(&rows).Error; err != nil {
			return nil, errors.New("history source query failed")
		}
		result[kind] = rows
	}
	var rawReplies int64
	if err := source.Table("v10_history_source.idcsmart_addon_idcsmart_ticket_reply").Count(&rawReplies).Error; err != nil || rawReplies != int64(len(result["reply"])) {
		return nil, errors.New("source ticket reply has missing parent")
	}
	return result, nil
}

// SupplementHistory is an isolated offline archive import. It NEVER calls the
// balance service, payment engine, live ticket handler, callbacks or providers.
func SupplementHistory(ctx context.Context, source, target *gorm.DB, sourceSocket, targetSocket, digest string) (*HistoryReport, error) {
	if sourceSocket == targetSocket || !isolated(sourceSocket) || !isolated(targetSocket) || len(digest) != 64 {
		return nil, errors.New("isolated history databases required")
	}
	for _, database := range []*gorm.DB{source, target} {
		var isolatedMode int
		if err := database.Raw("SELECT @@skip_networking").Scan(&isolatedMode).Error; err != nil || isolatedMode != 1 {
			return nil, errors.New("network-enabled history target refused")
		}
	}
	for _, statement := range strings.Split(historySQL, ";") {
		if strings.TrimSpace(statement) != "" {
			if err := target.Exec(statement).Error; err != nil {
				return nil, errors.New("archive schema creation failed")
			}
		}
	}
	records, err := historySources(source)
	if err != nil {
		return nil, err
	}
	var owners []struct {
		SourceID   int64
		CustomerID int64
	}
	if err := target.Table("system.v10_customer_source s").Select("s.source_id,s.customer_id").Joins("JOIN customer.customers c ON c.id=s.customer_id").Where("s.source_sha256=?", digest).Scan(&owners).Error; err != nil {
		return nil, err
	}
	ownerMap := map[int64]int64{}
	for _, owner := range owners {
		ownerMap[owner.SourceID] = owner.CustomerID
	}
	var complete int64
	if err := target.Table("system.v10_history_batches").Where("source_sha256=?", digest).Count(&complete).Error; err != nil {
		return nil, err
	}
	if complete == 0 {
		err = target.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			var coreComplete int64
			if err := tx.Table("system.v10_import_batches").Where("source_sha256=? AND state='complete'", digest).Count(&coreComplete).Error; err != nil || coreComplete != 1 {
				return errors.New("matching completed core import required")
			}
			if err := tx.Table("system.v10_history_batches").Create(map[string]any{"source_sha256": digest, "completed_at": time.Now().UTC()}).Error; err != nil {
				return err
			}
			for kind, rows := range records {
				for _, row := range rows {
					payload, e := historyPayload(row)
					if e != nil {
						return e
					}
					sourceID := historyNumber(row, "id")
					owner, ok := ownerMap[historyNumber(row, "client_id")]
					if !ok {
						return errors.New("history owner missing or excluded; no records discarded")
					}
					amount, credit := "0.00", "0.00"
					if kind == "balance" || kind == "payment" {
						amount = fmt.Sprint(row["amount"])
						if _, e := balance.ParseMoney(amount); e != nil {
							return errors.New("invalid history amount")
						}
					}
					if kind == "balance" {
						credit = fmt.Sprint(row["credit"])
						if _, e := balance.ParseMoney(credit); e != nil {
							return errors.New("invalid history ending balance")
						}
					}
					record := historyRecord{ID: snowflake.GenID(), SourceID: sourceID, Kind: kind, CustomerID: owner, SourceOrderID: historyNumber(row, "order_id"), SourceTicketID: historyNumber(row, "ticket_id"), Amount: amount, BalanceAfter: credit, CreatedAt: unix(historyNumber(row, "create_time")), Payload: payload, PayloadSHA256: historyHash(payload), SourceSHA256: digest, ReadOnly: true}
					if err := tx.Table("system.v10_history_records").Create(&record).Error; err != nil {
						return err
					}
				}
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	return ReconcileHistory(ctx, source, target, digest)
}
func ReconcileHistory(ctx context.Context, source, target *gorm.DB, digest string) (*HistoryReport, error) {
	records, err := historySources(source)
	if err != nil {
		return nil, err
	}
	report := &HistoryReport{Counts: map[string]int64{}}
	for kind, sourceRows := range records {
		var rows []historyRecord
		if err := target.WithContext(ctx).Table("system.v10_history_records").Where("source_sha256=? AND kind=?", digest, kind).Find(&rows).Error; err != nil {
			return nil, err
		}
		report.Counts[kind] = int64(len(rows))
		if len(rows) != len(sourceRows) {
			return nil, errors.New("history source/target count mismatch")
		}
		byID := map[int64]historyRecord{}
		for _, row := range rows {
			byID[row.SourceID] = row
		}
		for _, sourceRow := range sourceRows {
			payload, e := historyPayload(sourceRow)
			if e != nil {
				return nil, e
			}
			row, ok := byID[historyNumber(sourceRow, "id")]
			var stored map[string]any
			if !ok || json.Unmarshal([]byte(row.Payload), &stored) != nil {
				report.FieldMismatches++
				continue
			}
			canonical, _ := json.Marshal(stored)
			if historyHash(string(canonical)) != historyHash(payload) || row.PayloadSHA256 != historyHash(payload) || !row.ReadOnly {
				report.FieldMismatches++
			}
			if kind == "balance" || kind == "payment" {
				a, e := balance.ParseMoney(row.Amount)
				b, f := balance.ParseMoney(fmt.Sprint(sourceRow["amount"]))
				if e != nil || f != nil || a != b {
					report.FieldMismatches++
				}
			}
			if kind == "balance" {
				a, e := balance.ParseMoney(row.BalanceAfter)
				b, f := balance.ParseMoney(fmt.Sprint(sourceRow["credit"]))
				if e != nil || f != nil || a != b {
					report.FieldMismatches++
				}
			}
		}
	}
	if err := target.Raw("SELECT COUNT(*) FROM system.v10_history_records r LEFT JOIN customer.customers c ON c.id=r.customer_id LEFT JOIN system.v10_customer_source s ON s.customer_id=r.customer_id WHERE c.id IS NULL OR s.customer_id IS NULL OR CAST(JSON_UNQUOTE(JSON_EXTRACT(r.payload,'$.client_id')) AS UNSIGNED)<>s.source_id").Scan(&report.MissingOwners).Error; err != nil {
		return nil, err
	}
	if err := target.Raw("SELECT COUNT(*) FROM system.v10_history_records r LEFT JOIN system.v10_history_records t ON t.kind='ticket' AND t.source_id=r.source_ticket_id AND t.customer_id=r.customer_id AND t.source_sha256=r.source_sha256 WHERE r.kind='reply' AND t.id IS NULL").Scan(&report.MissingTicketParents).Error; err != nil {
		return nil, err
	}
	if err := target.Raw("SELECT COALESCE(SUM(balance),0) FROM billing.accounts").Scan(&report.BalanceYuan).Error; err != nil {
		return nil, err
	}
	if err := target.Table("billing.transactions").Count(&report.LedgerRows).Error; err != nil {
		return nil, err
	}
	if report.FieldMismatches != 0 || report.MissingOwners != 0 || report.MissingTicketParents != 0 {
		return report, errors.New("history reconciliation failed")
	}
	return report, nil
}
