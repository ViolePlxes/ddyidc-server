package backup

import (
	"context"
	balance "ddyidc/internal/billing/balance"
	"encoding/json"
	"errors"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func readPrivateJSON(path string, to any) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Size() > 128<<20 {
		return errors.New("维护记录格式或大小无效")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, to)
}

// ReleaseRestore is a privileged, explicit operations action, never called by
// Worker. It attests human external reconciliation; it does not pretend that
// a local SQL comparison proves a payment-provider or supplier transaction.
func (s *Service) ReleaseRestore(ctx context.Context, id, confirmation, operator, reason string) error {
	if s.privateRoot == "" {
		return errors.New("维护目录未初始化")
	}
	if _, err := uuid.Parse(id); err != nil {
		return errors.New("恢复任务ID无效")
	}
	if confirmation != "已完成外部对账 "+id || strings.TrimSpace(operator) == "" || strings.TrimSpace(reason) == "" || len(operator) > 100 || len(reason) > 500 {
		return errors.New("放行须提供完整确认文字、操作人和原因")
	}
	base := filepath.Join(s.privateRoot, "restore", id)
	raw, err := os.ReadFile(filepath.Join(base, "phase"))
	if err != nil {
		return errors.New("缺少恢复阶段记录")
	}
	phase := string(raw)
	if phase == "released" {
		return nil
	}
	switch phase {
	case "awaiting_reconciliation", "rolled_back", "validated", "drained", "runtime_accounts_fenced", "rollback_ready", "release_authorized":
	default:
		return errors.New("恢复阶段不允许放行，先完成数据修复或回滚")
	}
	var review RecoveryReview
	if err := readPrivateJSON(filepath.Join(base, "reconciliation.json"), &review); err != nil {
		return err
	}
	if !review.ExternalChecked || strings.TrimSpace(review.Notes) == "" {
		return errors.New("未完成支付、退款、采购、实例和续费外部事实核对")
	}
	reviewed := map[string]RecoveryDifference{}
	for _, d := range review.Differences {
		if (d.Resolution != "resolved" && d.Resolution != "accepted") || strings.TrimSpace(d.Notes) == "" {
			return errors.New("仍有未处理恢复差异")
		}
		key := d.Kind + ":" + d.ID
		if _, exists := reviewed[key]; exists {
			return errors.New("核对清单存在重复记录")
		}
		reviewed[key] = d
	}
	var before []RecoveryFact
	beforeErr := readPrivateJSON(filepath.Join(base, "before-facts.json"), &before)
	if beforeErr != nil && !os.IsNotExist(beforeErr) {
		return beforeErr
	}
	if beforeErr == nil {
		current, err := s.recoveryFacts(ctx)
		if err != nil {
			return err
		}
		for _, d := range differences(before, current) {
			r, ok := reviewed[d.Kind+":"+d.ID]
			if !ok || !sameFact(r.Before, d.Before) || !sameFact(r.Restored, d.Restored) {
				return errors.New("出现未在核对清单中确认的差异，禁止放行")
			}
		}
	} else if phase == "awaiting_reconciliation" || phase == "rolled_back" {
		return errors.New("缺少恢复前事实，禁止放行")
	}
	mismatches, err := balance.New(s.db).Reconcile(ctx)
	if err != nil || len(mismatches) > 0 {
		return errors.New("余额或冻结金额对账未通过，禁止放行")
	}
	var accounts []RuntimeAccount
	if err := readPrivateJSON(filepath.Join(base, "runtime-accounts.json"), &accounts); err != nil {
		return err
	}
	if len(accounts) == 0 {
		return errors.New("缺少账户恢复计划")
	}
	for _, a := range accounts {
		if a.User != "ddyidc_runtime" || !accountHost.MatchString(a.Host) {
			return errors.New("账户恢复计划无效")
		}
	}
	var barrier struct{ Mode, Reason string }
	if err := s.db.WithContext(ctx).Raw("SELECT mode,reason FROM system.operation_barrier WHERE id=1").Scan(&barrier).Error; err != nil {
		return errors.New("无法核对维护屏障归属")
	}
	if barrier.Mode != "running" && !(barrier.Mode == "restore" && barrier.Reason == "restore:"+id) {
		return errors.New("维护屏障属于其他恢复，禁止解锁账户")
	}
	audit, _ := json.Marshal(map[string]any{"jobId": id, "operator": operator, "reason": reason, "uid": os.Geteuid(), "authorizedAt": time.Now().UTC(), "externalChecked": true})
	if err := writeDurable(filepath.Join(base, "release-audit.json"), audit); err != nil {
		return err
	}
	if err := writeDurable(filepath.Join(base, "phase"), []byte("release_authorized")); err != nil {
		return err
	}
	for _, a := range accounts {
		if a.AccountLocked == "N" {
			if err := s.db.WithContext(ctx).Exec("ALTER USER " + accountSQL(a) + " ACCOUNT UNLOCK").Error; err != nil {
				return errors.New("部分账户放行失败，请凭维护日志重试")
			}
		}
	}
	// Accounts may be unlocked first: all application work remains blocked by
	// the durable barrier until this explicit, audited release completes.
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		result := tx.Exec("UPDATE system.operation_barrier SET mode='running',reason='',generation=generation+1 WHERE id=1 AND mode='restore' AND reason=?", "restore:"+id)
		if result.Error != nil {
			return errors.New("账户已解锁但维护屏障放行失败，凭维护日志重试")
		}
		if result.RowsAffected != 1 {
			var mode string
			if err := tx.Raw("SELECT mode FROM system.operation_barrier WHERE id=1").Scan(&mode).Error; err != nil || mode != "running" {
				return errors.New("维护屏障属于其他恢复，禁止放行")
			}
		}
		return tx.Model(&Job{}).Where("id=? AND kind='restore' AND status='awaiting_review'", id).Updates(map[string]any{"status": "released", "warning": "运维已核对并放行；应用服务须由运维确认启动"}).Error
	})
	if err != nil {
		return err
	}
	return writeDurable(filepath.Join(base, "phase"), []byte("released"))
}

func sameFact(a, b *RecoveryFact) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}
