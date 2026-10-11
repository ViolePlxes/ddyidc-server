package cdnsell

import (
	"context"
	"gorm.io/gorm"
)

// Leases mark an external request in flight; a lost acknowledgement must not
// trigger another write. Operators reconcile the cloud result instead.
func (s *Service) recoverExpiredTasks(ctx context.Context) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		q := tx.Table(tasksTable).Where("status='running' AND biz_id>0 AND (lease_until IS NULL OR lease_until<=?)", s.now())
		if e := q.Updates(map[string]any{"status": "need_manual", "lease_until": nil, "error": "任务中断，供应商结果待核查，请先同步域名信息"}).Error; e != nil {
			return e
		}
		if e := tx.Table(configTasksTable).Where("status='running' AND (lease_until IS NULL OR lease_until<=?)", s.now()).Updates(map[string]any{"status": "terminated", "lease_until": nil, "error": "配置下发中断，须回读核对，未重复下发"}).Error; e != nil {
			return e
		}
		return tx.Table(refreshTasksTable).Where("status='running' AND lease_until IS NOT NULL AND lease_until<=?", s.now()).Updates(map[string]any{"status": "failed", "lease_until": nil, "error": "提交结果待核查，未自动重复提交", "finished_at": s.now()}).Error
	})
}
