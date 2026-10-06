// Package pointsevents persists rewards intentions with the successful business
// transaction. It never calls a provider or changes money balances.
package pointsevents

import (
	"encoding/json"
	"errors"
	"fmt"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type Event struct {
	Key         string `gorm:"column:event_key;primaryKey" json:"key"`
	UserID      int64  `json:"userId,string"`
	Event       string `json:"event"`
	RefID       string `json:"refId"`
	AmountCents int64  `json:"amountCents"`
	RefundCents int64  `json:"refundCents,omitempty"`
	PaidCents   int64  `json:"paidCents,omitempty"`
}

func Enqueue(tx *gorm.DB, e Event) error {
	if tx == nil || e.UserID <= 0 || e.Key == "" || len(e.Key) > 160 || e.Event == "" || e.RefID == "" || len(e.RefID) > 80 || e.AmountCents < 0 || e.RefundCents < 0 || e.PaidCents < 0 {
		return errors.New("invalid points event")
	}
	var enabled bool
	if err := tx.Table("system.points_settings").Select("enabled").Where("id=1").Scan(&enabled).Error; err != nil {
		return err
	}
	if !enabled {
		return nil
	}
	body, err := json.Marshal(e)
	if err != nil {
		return err
	}
	row := map[string]any{"event_key": e.Key, "user_id": e.UserID, "payload": string(body), "status": "pending"}
	if err = tx.Table("system.points_outbox").Clauses(clause.OnConflict{DoUpdates: clause.AssignmentColumns([]string{"event_key"})}).Create(row).Error; err != nil {
		return err
	}
	var stored string
	if err = tx.Table("system.points_outbox").Select("payload").Where("event_key=?", e.Key).Scan(&stored).Error; err != nil {
		return err
	}
	var prior Event
	if json.Unmarshal([]byte(stored), &prior) != nil || prior != e {
		return fmt.Errorf("points event identity conflict")
	}
	return nil
}
