package update

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// A preparation failure may occur after its unprivileged fast-forward but before
// publication starts. Restore only this task's validated source plan so the
// version check cannot incorrectly report an uninstalled release as current.
func (m *Manager) restoreMigrationSource(ctx context.Context, result HelperResult, task *Task) (bool, error) {
	if result.Status != StatusFailed || result.RollbackStatus != "not_required" {
		return false, nil
	}
	raw, err := os.ReadFile(m.config.helperPlanPath())
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	var plan HelperPlan
	if err = json.Unmarshal(raw, &plan); err != nil {
		return false, err
	}
	if !plan.NeedsMigration || plan.TaskID != result.TaskID {
		return false, nil
	}
	if task == nil || plan.BeforeCommit != task.BeforeCommit || plan.Workspace != m.config.Backend.Workspace || plan.CurrentExecutable != m.config.Backend.Executable || plan.ResultFile != filepath.Join(m.config.ResultDir, result.TaskID+".result.json") {
		return true, errors.New("migration source recovery plan differs")
	}
	return true, m.resetWorktree(ctx, m.config.Backend, task.BeforeCommit)
}

// Candidate compilation does not advance the live worktree. Once ready, the
// existing durable helper plan covers the source/binary publication boundary.
func (m *Manager) recoverBackendPlan(ctx context.Context) error {
	raw, err := os.ReadFile(m.config.helperPlanPath())
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	var plan HelperPlan
	if err = json.Unmarshal(raw, &plan); err != nil {
		return err
	}
	if plan.ParentIdentity != "" && processIdentity(plan.ParentPID) == plan.ParentIdentity {
		return nil
	}
	services := []string{m.config.UpdateHelperService}
	if plan.NeedsMigration {
		services = append(services, "ddyidc-update-migration.service")
	}
	for _, service := range services {
		state, stateErr := m.runner.Run(ctx, "/", m.config.SystemctlPath, []string{"show", service, "-p", "ActiveState", "--value"}, nil)
		if stateErr != nil {
			return stateErr
		}
		if strings.TrimSpace(state) == "active" || strings.TrimSpace(state) == "activating" || strings.TrimSpace(state) == "deactivating" {
			return nil
		}
	}
	if _, err = os.Stat(plan.ResultFile); err == nil {
		return nil
	}
	// A backup means the independent helper entered publication. Its durable
	// result owns that outcome; never claim rollback from a Git reset alone.
	if _, err = os.Stat(plan.BackupExecutable); err == nil {
		return nil
	}
	id, err := strconv.ParseInt(plan.TaskID, 10, 64)
	if err != nil || id <= 0 {
		return errors.New("unsafe backend recovery ID")
	}
	if plan.Workspace != m.config.Backend.Workspace || plan.CurrentExecutable != m.config.Backend.Executable || plan.NewExecutable != filepath.Join(m.config.ResultDir, "staging", plan.TaskID+".new") || plan.ResultFile != filepath.Join(m.config.ResultDir, plan.TaskID+".result.json") {
		return errors.New("unsafe backend recovery paths")
	}
	task, err := m.repo.Get(ctx, id)
	if err != nil {
		return err
	}
	if task == nil {
		return errors.New("missing backend recovery task")
	}
	if task.Status == StatusRunning {
		if err = m.resetWorktree(ctx, m.config.Backend, plan.BeforeCommit); err != nil {
			return err
		}
		if err = m.repo.Finish(ctx, id, StatusFailed, "更新助手启动前进程中断，旧版本保持运行", "success", "已从持久计划恢复源码；未执行数据库恢复或迁移"); err != nil {
			return err
		}
	}
	if err = m.locker.Release(ctx, plan.TaskID); err != nil {
		return err
	}
	if err = os.Remove(m.config.helperPlanPath()); err != nil {
		return err
	}
	return nil
}
