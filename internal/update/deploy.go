package update

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func (m *Manager) refreshTarget(ctx context.Context, task *Task) (TargetConfig, VersionInfo, error) {
	target, err := m.config.target(task.Target)
	if err != nil {
		return TargetConfig{}, VersionInfo{}, err
	}
	info, err := m.versionInfo(ctx, task.Target, false)
	if err != nil {
		return TargetConfig{}, VersionInfo{}, err
	}
	task.TargetCommit = info.RemoteCommit
	task.TargetVersion = info.Remote
	task.Feature = firstFeature(info.Changes)
	_ = m.repo.Update(ctx, task.ID, map[string]any{
		"target_commit":  task.TargetCommit,
		"target_version": task.TargetVersion,
		"feature":        task.Feature,
	})
	return target, info, nil
}

func (m *Manager) runBackend(ctx context.Context, task *Task) {
	logValue := &taskLog{}
	m.progress(ctx, task, 3, logValue, "全局任务锁：当前任务已取得 Redis 唯一锁")
	checks, err := m.preflight(ctx, TargetBackend)
	for _, check := range checks {
		m.progress(ctx, task, 8, logValue, fmt.Sprintf("前置检查 [%s] %s：%s", check.Name, resultLabel(check.OK), check.Message))
	}
	if err != nil {
		m.fail(context.Background(), task, logValue, err, "not_required")
		return
	}
	target, info, err := m.refreshTarget(ctx, task)
	if err != nil {
		m.fail(context.Background(), task, logValue, err, "not_required")
		return
	}
	if !info.HasUpdate {
		m.progress(ctx, task, 95, logValue, "后端已是最新版本，无需替换")
		m.succeed(context.Background(), task, logValue)
		return
	}

	m.progress(ctx, task, 20, logValue, "导出后端候选提交，当前源码与服务保持不变")
	if output, diffErr := m.git(ctx, target.Workspace, "diff", "--name-only", task.BeforeCommit, info.RemoteCommit, "--", "*.sql"); diffErr == nil && strings.TrimSpace(output) != "" {
		m.progress(ctx, task, 30, logValue, "检测到数据库脚本变化；切换前须完成保留现有数据的离线增量迁移，更新不会自动改库")
	}
	newExecutable := filepath.Join(m.config.ResultDir, "staging", formatID(task.ID)+".new")
	if err = m.files.MkdirAll(filepath.Dir(newExecutable)); err != nil {
		m.fail(context.Background(), task, logValue, err, "not_required")
		return
	}
	rollbackBeforeSwitch := func(cause error) {
		cleanup, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_ = m.files.RemoveAll(newExecutable)
		rollback := "success"
		if resetErr := m.resetWorktree(cleanup, target, task.BeforeCommit); resetErr != nil {
			rollback = "failed: " + sanitizeOutput(resetErr.Error())
		}
		m.fail(context.Background(), task, logValue, cause, rollback)
	}

	_ = m.files.RemoveAll(newExecutable)
	job, prepareErr := m.prepareBuild(ctx, task, target, info)
	if prepareErr != nil {
		rollbackBeforeSwitch(prepareErr)
		return
	}
	m.progress(ctx, task, 42, logValue, "使用独立隔离账号构建后端")
	if output, buildErr := m.isolatedBuild(ctx, task, "backend"); buildErr != nil {
		rollbackBeforeSwitch(fmt.Errorf("后端隔离构建失败: %w (%s)", buildErr, output))
		return
	}
	if err = m.files.CopyFile(filepath.Join(job, "candidate-server"), newExecutable); err != nil {
		rollbackBeforeSwitch(err)
		return
	}
	if err = os.Chmod(newExecutable, 0755); err != nil {
		rollbackBeforeSwitch(err)
		return
	}

	// This executes as the existing unprivileged API identity. The candidate's
	// verification mode reads migration history only; it cannot run DBA migrations.
	m.progress(ctx, task, 60, logValue, "只读核验数据库迁移版本，旧服务保持运行")
	if _, verifyErr := m.runner.Run(ctx, target.Workspace, newExecutable, []string{"-f", target.ConfigFile, "-verify-migrations"}, nil); verifyErr != nil {
		rollbackBeforeSwitch(errors.New("数据库增量迁移未完成或校验失败，旧服务未停止；请完成保留数据的离线迁移后重试"))
		return
	}
	// The trusted helper is installed by deployment, never rebuilt by the API service.
	if _, helperErr := os.Stat(target.HelperPath); helperErr != nil {
		rollbackBeforeSwitch(fmt.Errorf("更新助手未安装: %w", helperErr))
		return
	}
	backup := filepath.Join(m.config.ResultDir, "backups", formatID(task.ID)+".previous")
	m.progress(ctx, task, 70, logValue, "更新产物已验证，准备由 systemd 更新助手执行原子切换")

	taskID := formatID(task.ID)
	resultFile := filepath.Join(m.config.ResultDir, taskID+".result.json")
	planFile := m.config.helperPlanPath()
	helperTimeout := m.config.TimeoutSeconds
	if deadline, ok := ctx.Deadline(); ok {
		helperTimeout = max(1, int(time.Until(deadline).Seconds()))
	}
	plan := HelperPlan{
		TaskID: taskID, ParentPID: m.pid, ParentIdentity: processIdentity(m.pid), NewExecutable: newExecutable,
		CurrentExecutable: target.Executable, BackupExecutable: backup,
		Workspace: target.Workspace, BeforeCommit: task.BeforeCommit, GitPath: m.config.GitPath,
		ServerDirectory: filepath.Dir(target.Executable), ConfigFile: target.ConfigFile,
		HealthURL: m.config.HealthURL, VersionURL: m.config.VersionURL, SmokeURL: m.config.SmokeURL,
		ExpectedCommit: info.RemoteCommit, ResultFile: resultFile, TimeoutSeconds: helperTimeout,
		MinFreeMB: m.config.MinFreeMB, SystemctlPath: m.config.SystemctlPath, SudoPath: m.config.SudoPath, ServiceName: m.config.ServerService,
	}
	if err = m.files.WriteJSON(planFile, plan); err != nil {
		rollbackBeforeSwitch(fmt.Errorf("写入更新计划失败: %w", err))
		return
	}
	if output, pullErr := m.pull(ctx, target); pullErr != nil {
		_ = m.files.RemoveAll(planFile)
		rollbackBeforeSwitch(fmt.Errorf("更新候选源码失败: %w (%s)", pullErr, output))
		return
	}
	m.progress(ctx, task, 85, logValue, "更新助手已就绪，即将安全切换后端")
	if err = m.startUnit(m.config.SudoPath, m.config.SystemctlPath, m.config.UpdateHelperService); err != nil {
		_ = m.files.RemoveAll(planFile)
		rollbackBeforeSwitch(fmt.Errorf("启动更新助手失败: %w", err))
		return
	}
	_ = m.repo.Update(context.Background(), task.ID, map[string]any{"progress": 90, "log": logValue.String() + "\n后端进程切换中"})
	time.Sleep(350 * time.Millisecond)
	m.exit(0)
}

func (m *Manager) runFrontend(ctx context.Context, task *Task) {
	logValue := &taskLog{}
	m.progress(ctx, task, 3, logValue, "全局任务锁：当前任务已取得 Redis 唯一锁")
	checks, err := m.preflight(ctx, TargetFrontend)
	for _, check := range checks {
		m.progress(ctx, task, 8, logValue, fmt.Sprintf("前置检查 [%s] %s：%s", check.Name, resultLabel(check.OK), check.Message))
	}
	if err != nil {
		m.fail(context.Background(), task, logValue, err, "not_required")
		return
	}
	target, info, err := m.refreshTarget(ctx, task)
	if err != nil {
		m.fail(context.Background(), task, logValue, err, "not_required")
		return
	}
	if !info.HasUpdate {
		m.progress(ctx, task, 95, logValue, "前端已是最新版本，无需构建")
		m.succeed(context.Background(), task, logValue)
		return
	}
	if err = m.frontendRelease(ctx, task, target, info, logValue); err != nil {
		m.fail(context.Background(), task, logValue, err, m.frontendRollbackStatus(task.ID))
		return
	}
	m.progress(ctx, task, 95, logValue, "前端原子切换与检查通过，旧版本和资源已保留")
	m.succeed(context.Background(), task, logValue)
}

func frontendInstallArgs() []string {
	return []string{"ci", "--ignore-scripts", "--no-audit", "--no-fund"}
}

func replaceStaticDirectory(files FileOps, source, destination string) error {
	if err := files.ReplaceDirectory(source, destination); err != nil {
		return err
	}
	return normalizeStaticTree(destination)
}

func (m *Manager) verifyHTTP(ctx context.Context, url, name string) error {
	status, _, err := m.httpGet(ctx, url)
	if err != nil || status != http.StatusOK {
		return fmt.Errorf("%s检查失败: status=%d err=%v", name, status, err)
	}
	return nil
}

func verifyFrontendVersion(path, expectedCommit string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("读取前端 version.json 失败: %w", err)
	}
	var version struct {
		Commit string `json:"commit"`
	}
	if err = json.Unmarshal(data, &version); err != nil {
		return fmt.Errorf("解析前端 version.json 失败: %w", err)
	}
	if strings.TrimSpace(version.Commit) != strings.TrimSpace(expectedCommit) {
		return errors.New("前端 version.json commit 与目标版本不一致")
	}
	return nil
}

func safeBuildValue(value string) string {
	return strings.NewReplacer(" ", "_", "\t", "_", "\n", "_").Replace(strings.TrimSpace(value))
}

func resultLabel(ok bool) string {
	if ok {
		return "通过"
	}
	return "失败"
}
