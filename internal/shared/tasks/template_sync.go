package tasks

import (
	"ddyidc/internal/shared/snowflake"
	"encoding/json"
	"gorm.io/gorm"
	"strconv"
)

// TemplateSyncPayload asks the durable worker to refresh registrar audit state.
// CustomerID 0 means every non-final template (periodic sweep).
type TemplateSyncPayload struct {
	CustomerID int64 `json:"customerId"`
}

func EnqueueTemplateSync(tx *gorm.DB, customerID int64) error {
	raw, err := json.Marshal(TemplateSyncPayload{CustomerID: customerID})
	if err != nil {
		return err
	}
	id := snowflake.GenID()
	return tx.Create(&Task{ID: id, TaskKey: "domain-template-sync:" + strconv.FormatInt(id, 10), Kind: "domain_template_sync", Payload: string(raw), Status: "pending"}).Error
}
