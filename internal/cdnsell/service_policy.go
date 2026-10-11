package cdnsell

import (
	"context"
	"ddyidc/internal/shared/snowflake"
	"errors"
	"fmt"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"strings"
)

// Automatic recovery owns only pauses created by this policy. Unknown cloud
// outcomes retain their task marker and require reconciliation, never resubmit.
func (s *Service) RunServicePolicy(ctx context.Context) error {
	var settings struct{ Enabled, AutoSuspendEnabled, AutoRestoreEnabled bool }
	if err := s.db.WithContext(ctx).Table(settingsTable).Where("id=1").Take(&settings).Error; err != nil {
		return err
	}
	if !settings.Enabled || (!settings.AutoSuspendEnabled && !settings.AutoRestoreEnabled) {
		return nil
	}
	writes, err := s.P2CloudWritesEnabled(ctx)
	if err != nil || !writes {
		return err
	}
	var ids []int64
	if err = s.db.WithContext(ctx).Table(domainsTable).Where("deleted=0 AND upstream_status='active' AND service_no<>''").Pluck("id", &ids).Error; err != nil {
		return err
	}
	var failures []error
	for _, id := range ids {
		if err = s.planServicePolicy(ctx, id, settings.AutoSuspendEnabled, settings.AutoRestoreEnabled); err != nil {
			failures = append(failures, err)
		}
	}
	return errors.Join(failures...)
}
func (s *Service) hasServiceCredit(tx *gorm.DB, user int64) (bool, error) {
	var remaining int64
	now := s.now()
	if err := tx.Table(batchesTable).Where("user_id=? AND status='running' AND effective_time<=? AND expire_time>?", user, now, now).Select("COALESCE(SUM(remaining_bytes),0)").Scan(&remaining).Error; err != nil {
		return false, err
	}
	var debt int64
	if err := tx.Table("system.cdnsell_overage_bills").Where("user_id=? AND status IN ('unpaid','recalculated') AND amount>0", user).Count(&debt).Error; err != nil {
		return false, err
	}
	return remaining > 0 && debt == 0, nil
}
func (s *Service) planServicePolicy(ctx context.Context, id int64, suspend, restore bool) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var d DomainRecord
		if err := tx.Table(domainsTable).Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=?", id).Take(&d).Error; err != nil {
			return err
		}
		if d.Deleted || d.ManualDisabled || d.AutoTaskID != 0 || d.ServiceNo == "" || d.UpstreamStatus != "active" {
			return nil
		}
		credit, err := s.hasServiceCredit(tx, d.UserID)
		if err != nil {
			return err
		}
		kind := ""
		if !credit && suspend && d.Enabled && !d.AutoSuspended {
			kind = "auto_domain_disable"
		}
		if credit && restore && d.AutoSuspended && !d.Enabled {
			kind = "auto_domain_enable"
		}
		if kind == "" {
			return nil
		}
		var count int64
		if err = tx.Table(tasksTable).Where("biz_type='domain' AND biz_id=? AND status IN ('pending','running','failed','need_manual')", id).Count(&count).Error; err != nil {
			return err
		}
		if count > 0 {
			return nil
		}
		taskID := snowflake.GenID()
		task := DomainTaskRecord{ID: taskID, TaskNo: fmt.Sprintf("CT%d", taskID), TaskType: kind, BizType: "domain", BizID: id, Status: "pending", MaxAttempts: 1, CreatedAt: s.now()}
		if err = tx.Table(tasksTable).Create(&task).Error; err != nil {
			return err
		}
		return tx.Table(domainsTable).Where("id=?", id).Update("auto_task_id", taskID).Error
	})
}
func (s *Service) executeAutomaticState(ctx context.Context, task DomainTaskRecord) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var d DomainRecord
		if err := tx.Table(domainsTable).Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=?", task.BizID).Take(&d).Error; err != nil {
			return err
		}
		if d.AutoTaskID != task.ID {
			return nil
		}
		var settings struct{ Enabled, AutoSuspendEnabled, AutoRestoreEnabled bool }
		if err := tx.Table(settingsTable).Where("id=1").Take(&settings).Error; err != nil {
			return err
		}
		credit, err := s.hasServiceCredit(tx, d.UserID)
		if err != nil {
			return err
		}
		enable := task.TaskType == "auto_domain_enable"
		allowed := settings.Enabled && !d.Deleted && !d.ManualDisabled && d.ServiceNo != "" && d.UpstreamStatus == "active"
		if enable {
			allowed = allowed && settings.AutoRestoreEnabled && credit && d.AutoSuspended
		} else {
			allowed = allowed && settings.AutoSuspendEnabled && !credit && d.Enabled && !d.AutoSuspended
		}
		if !allowed {
			return tx.Table(domainsTable).Where("id=?", d.ID).Update("auto_task_id", 0).Error
		}
		_, account, err := s.accountSecret(ctx, d.AccountID)
		if err != nil {
			return err
		}
		if err = s.waitCloudGap(ctx); err != nil {
			return err
		}
		// A cloud-side manual pause must never acquire automatic ownership.
		if !enable {
			info, readErr := s.cloud.GetDomain(ctx, account, d.ServiceNo)
			if readErr != nil {
				return readErr
			}
			status, _ := info["status"].(string)
			paused, stateErr := externallyPaused(status)
			if stateErr != nil {
				return stateErr
			}
			if paused {
				return tx.Table(domainsTable).Where("id=? AND auto_task_id=?", d.ID, task.ID).Updates(map[string]any{"enabled": false, "manual_disabled": true, "auto_suspended": false, "auto_task_id": 0}).Error
			}
			if err = s.waitCloudGap(ctx); err != nil {
				return err
			}
		}
		started := s.now()
		err = s.cloud.SetDomainEnabled(ctx, account, d.ServiceNo, enable)
		s.writeAPILog(ctx, d.AccountID, "SetDomainEnabled", map[string]any{"domainId": d.ID, "enabled": enable, "reason": "traffic_policy"}, map[string]any{}, started, err)
		if err != nil {
			return err
		}
		return tx.Table(domainsTable).Where("id=? AND auto_task_id=?", d.ID, task.ID).Updates(map[string]any{"enabled": enable, "auto_suspended": !enable, "auto_task_id": 0}).Error
	})
}

func externallyPaused(status string) (bool, error) {
	switch strings.ToLower(status) {
	case "offline":
		return true, nil
	case "online", "active":
		return false, nil
	default:
		return false, errors.New("域名当前状态未确认，自动停复未执行")
	}
}
