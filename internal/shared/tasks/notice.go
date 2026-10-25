package tasks

import (
	"ddyidc/internal/shared/snowflake"
	"encoding/json"
	"errors"
	"fmt"
	"gorm.io/gorm"
	"reflect"
)

type NoticePayload struct {
	CustomerID int64             `json:"customerId"`
	BusinessID int64             `json:"businessId"`
	Scene      string            `json:"scene"`
	Params     map[string]string `json:"params"`
}

// EnqueueNotice uses the existing durable queue inside the caller's business
// transaction. It must never be called with an untrusted client-supplied scene.
func EnqueueNotice(tx *gorm.DB, p NoticePayload) error {
	if tx == nil || p.CustomerID <= 0 || p.BusinessID <= 0 {
		return errors.New("invalid notification identity")
	}
	switch p.Scene {
	case "domain_order_success", "domain_order_failed", "marketplace_trade_success", "marketplace_transfer_complete", "marketplace_refund", "marketplace_withdraw_result", "marketplace_listing_passed":
	default:
		return errors.New("unregistered durable notification scene")
	}
	raw, err := json.Marshal(p)
	if err != nil {
		return err
	}
	if len(raw) > 8192 {
		return errors.New("notification payload too large")
	}
	key := fmt.Sprintf("notice:%s:%d:%d", p.Scene, p.BusinessID, p.CustomerID)
	var old Task
	err = tx.Where("task_key=?", key).Take(&old).Error
	if err == nil {
		var previous NoticePayload
		if old.Kind != "customer_notice" || json.Unmarshal([]byte(old.Payload), &previous) != nil || !reflect.DeepEqual(previous, p) {
			return errors.New("notification identity conflict")
		}
		return nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return err
	}
	return tx.Create(&Task{ID: snowflake.GenID(), TaskKey: key, Kind: "customer_notice", Payload: string(raw), Status: "pending"}).Error
}
