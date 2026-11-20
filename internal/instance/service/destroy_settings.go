package instance

import (
	"context"
	"ddyidc/internal/shared/snowflake"
	"encoding/json"
	"errors"
	"gorm.io/gorm"
	"strings"
	"time"
)

// destroySettingsKey stores the customer destroy-application switches in
// system_configs so enabling them needs no schema change.
const destroySettingsKey = "instance.destroy.settings"

// DestroySettings keeps the destroy request behaviour configurable. Email
// verification defaults to on: that is the behaviour every existing install
// already has, so a missing or unreadable row must never weaken it.
type DestroySettings struct {
	RequireEmailVerification bool `json:"requireEmailVerification"`
}

func defaultDestroySettings() DestroySettings {
	return DestroySettings{RequireEmailVerification: true}
}

func LoadDestroySettings(db *gorm.DB) (DestroySettings, error) {
	out := defaultDestroySettings()
	var row struct{ Value string }
	err := db.Table("system.system_configs").Select("value").Where("`key`=?", destroySettingsKey).Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return out, nil
	}
	if err != nil {
		return out, err
	}
	if strings.TrimSpace(row.Value) == "" {
		return out, nil
	}
	if err := json.Unmarshal([]byte(row.Value), &out); err != nil {
		// A corrupt row keeps verification enabled and reports the fault.
		return defaultDestroySettings(), err
	}
	return out, nil
}

// DestroySettings reads the current switches for the console and admin views.
func (s *Service) DestroySettings(ctx context.Context) (DestroySettings, error) {
	if s.db == nil {
		return defaultDestroySettings(), ErrUnsupported
	}
	return LoadDestroySettings(s.db.WithContext(ctx))
}

// SaveDestroySettings upserts the switches and records who changed them.
func (s *Service) SaveDestroySettings(ctx context.Context, in DestroySettings, adminID int64, operatorIP string) (DestroySettings, error) {
	if s.db == nil {
		return DestroySettings{}, ErrUnsupported
	}
	payload, err := json.Marshal(in)
	if err != nil {
		return DestroySettings{}, err
	}
	detail, _ := json.Marshal(map[string]any{"requireEmailVerification": in.RequireEmailVerification})
	now := time.Now().UTC()
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec("INSERT INTO system.system_configs(id,`key`,value,description) VALUES(?,?,?,?) ON DUPLICATE KEY UPDATE value=VALUES(value)", snowflake.GenID(), destroySettingsKey, string(payload), "实例销毁申请设置").Error; err != nil {
			return err
		}
		return tx.Exec("INSERT INTO system.audit_logs(id,operator_type,operator_id,action,target_type,target_id,ip,detail,created_at,updated_at) VALUES(?,1,?,?,?,0,?,?,?,?)", snowflake.GenID(), adminID, "instance.destroy_settings", "instance", operatorIP, string(detail), now, now).Error
	})
	if err != nil {
		return DestroySettings{}, err
	}
	return in, nil
}
