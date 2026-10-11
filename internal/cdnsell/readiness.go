package cdnsell

import (
	"context"
	"ddyidc/internal/shared/snowflake"
	"ddyidc/pkg/clientip"
	"encoding/json"
	"errors"
	"gorm.io/gorm"
	"net/http"
	"strings"
)

type Readiness struct {
	AutoSuspendEnabled bool   `json:"autoSuspendEnabled"`
	AutoRestoreEnabled bool   `json:"autoRestoreEnabled"`
	OveragePrice       Yuan   `json:"overagePrice"`
	Enabled            bool   `json:"enabled"`
	Phase              string `json:"phase"`
	CloudWritesEnabled bool   `json:"cloudWritesEnabled"`
	Ready              bool   `json:"ready"`
	SetupMessage       string `json:"setupMessage"`
	InventoryMode      string `json:"inventoryMode"`
}

func (s *Service) Readiness(ctx context.Context) (*Readiness, error) {
	enabled, e := s.Enabled(ctx)
	if e != nil {
		return nil, e
	}
	writes, e := s.P2CloudWritesEnabled(ctx)
	if e != nil {
		return nil, e
	}
	var count int64
	if e = s.db.WithContext(ctx).Table(accountsTable).Where("status=1 AND ak_encrypted<>'' AND sk_encrypted<>'' AND JSON_EXTRACT(last_test_result,'$.success')=true").Count(&count).Error; e != nil {
		return nil, e
	}
	q := &Readiness{}
	if e = s.db.WithContext(ctx).Table(settingsTable).Select("auto_suspend_enabled,auto_restore_enabled,overage_price").Where("id=1").Scan(q).Error; e != nil {
		return nil, e
	}
	q.Enabled, q.Phase, q.CloudWritesEnabled, q.InventoryMode = enabled, "P2", writes, "manual"
	switch {
	case !enabled:
		q.SetupMessage = "CDN服务暂未开放"
	case count == 0:
		q.SetupMessage = "配置账号并完成连接测试后可用"
	case !writes:
		q.SetupMessage = "域名操作需管理员开启服务操作"
	default:
		q.Ready = true
	}
	return q, nil
}
func (h *Handler) cdnSettings(w http.ResponseWriter, r *http.Request) {
	id, yes := h.claims(w, r, "admin")
	if !yes {
		return
	}
	if r.Method == http.MethodGet {
		value, e := h.service.Readiness(r.Context())
		result(w, value, e)
		return
	}
	var count int64
	if h.service.db.WithContext(r.Context()).Table("system.admins").Where("id=? AND status=1 AND is_super=1", id).Count(&count).Error != nil || count != 1 {
		message(w, 403, "仅超级管理员可修改CDN设置")
		return
	}
	var input struct {
		AutoSuspendEnabled *bool  `json:"autoSuspendEnabled"`
		AutoRestoreEnabled *bool  `json:"autoRestoreEnabled"`
		OveragePrice       *Yuan  `json:"overagePrice"`
		Enabled            bool   `json:"enabled"`
		CloudWritesEnabled bool   `json:"cloudWritesEnabled"`
		Notes              string `json:"notes"`
	}
	if !decode(w, r, &input) {
		return
	}
	if input.OveragePrice != nil && (*input.OveragePrice < 0 || *input.OveragePrice > 5000000) {
		message(w, 400, "超额单价必须为0至50000元/GiB")
		return
	}
	input.Notes = strings.TrimSpace(input.Notes)
	if len(input.Notes) < 2 || len(input.Notes) > 500 {
		message(w, 400, "请填写2–500字操作说明")
		return
	}
	err := h.service.db.WithContext(r.Context()).Transaction(func(tx *gorm.DB) error {
		if input.CloudWritesEnabled {
			var n int64
			if e := tx.Table(accountsTable).Where("status=1 AND JSON_EXTRACT(last_test_result,'$.success')=true").Count(&n).Error; e != nil {
				return e
			}
			if n == 0 {
				return errors.New("请先配置账号并通过连接测试")
			}
		}
		changes := map[string]any{"enabled": input.Enabled}
		if input.AutoSuspendEnabled != nil {
			changes["auto_suspend_enabled"] = *input.AutoSuspendEnabled
		}
		if input.AutoRestoreEnabled != nil {
			changes["auto_restore_enabled"] = *input.AutoRestoreEnabled
		}
		if input.OveragePrice != nil {
			changes["overage_price"] = *input.OveragePrice
		}
		if e := tx.Table(settingsTable).Where("id=1").Updates(changes).Error; e != nil {
			return e
		}
		if e := tx.Table("system.cdnsell_p2_runtime").Where("id=1").Updates(map[string]any{"cloud_writes_enabled": input.CloudWritesEnabled, "enabled_by": id, "enabled_at": h.service.now()}).Error; e != nil {
			return e
		}
		payload, _ := json.Marshal(map[string]any{"enabled": input.Enabled, "cloudWritesEnabled": input.CloudWritesEnabled, "autoSuspendEnabled": input.AutoSuspendEnabled, "autoRestoreEnabled": input.AutoRestoreEnabled, "overagePrice": input.OveragePrice, "notes": input.Notes, "operatorIp": clientip.FromRequest(r)})
		return tx.Exec("INSERT INTO system.cdnsell_admin_audits(id,admin_id,action,payload,created_at) VALUES(?,?,'settings',?,?)", snowflake.GenID(), id, string(payload), h.service.now()).Error
	})
	result(w, map[string]bool{"updated": err == nil}, err)
}
