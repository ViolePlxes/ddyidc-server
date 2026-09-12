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

	m.progress(ctx, task, 20, logValue, "拉取后端 main 分支更新")
	if output, pullErr := m.pull(ctx, target); pullErr != nil {
		m.fail(context.Background(), task, logValue, fmt.Errorf("拉取代码失败: %w (%s)", pullErr, output), "not_required")
		return
	}
	if output, diffErr := m.git(ctx, target.Workspace, "diff", "--name-only", task.BeforeCommit, info.RemoteCommit, "--", "*.sql"); diffErr == nil && strings.TrimSpace(output) != "" {
		m.progress(ctx, task, 30, logValue, "检测到数据库脚本变化；更新不会自动执行迁移，请在更新完成后人工评审并迁移")
	}
	newExecutable := target.Executable + ".new"
	rollbackBeforeSwitch := func(cause error) {
		_ = m.files.RemoveAll(newExecutable)
		rollback := "success"
		if resetErr := m.resetWorktree(context.Background(), target, task.BeforeCommit); resetErr != nil {
			rollback = "failed: " + sanitizeOutput(resetErr.Error())
		}
		m.fail(context.Background(), task, logValue, cause, rollback)
	}

	_ = m.files.RemoveAll(newExecutable)
	buildTime := time.Now().UTC().Format(time.RFC3339)
	ldflags := fmt.Sprintf("-X main.version=%s -X main.commit=%s -X main.branch=%s -X main.buildTime=%s", safeBuildValue(info.Remote), safeBuildValue(info.RemoteCommit), safeBuildValue(target.Branch), safeBuildValue(buildTime))
	m.progress(ctx, task, 42, logValue, "构建后端新版本")
	buildEnv := []string{"CGO_ENABLED=0", "GOOS=linux", "GOARCH=amd64", "GOPROXY=https://goproxy.cn,direct"}
	if output, buildErr := m.runner.Run(ctx, target.Workspace, m.config.GoPath, []string{"build", "-trimpath", "-ldflags", ldflags, "-o", newExecutable, "."}, buildEnv); buildErr != nil {
		rollbackBeforeSwitch(fmt.Errorf("后端构建失败: %w (%s)", buildErr, output))
		return
	}
	m.progress(ctx, task, 58, logValue, "构建外部更新助手")
	if output, helperErr := m.runner.Run(ctx, target.Workspace, m.config.GoPath, []string{"build", "-trimpath", "-o", target.HelperPath, "./cmd/updatehelper"}, buildEnv); helperErr != nil {
		rollbackBeforeSwitch(fmt.Errorf("更新助手构建失败: %w (%s)", helperErr, output))
		return
	}
	backup := target.Executable + "~"
	m.progress(ctx, task, 70, logValue, "更新产物已验证，准备由 systemd 更新助手执行原子切换")

	taskID := formatID(task.ID)
	resultFile := filepath.Join(m.config.ResultDir, taskID+".result.json")
	planFile := m.config.helperPlanPath()
	helperTimeout := m.config.TimeoutSeconds
	if deadline, ok := ctx.Deadline(); ok {
		helperTimeout = max(1, int(time.Until(deadline).Seconds()))
	}
	plan := HelperPlan{
		TaskID: taskID, ParentPID: m.pid, NewExecutable: newExecutable,
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
	dist := filepath.Join(target.Workspace, "dist")
	backup := filepath.Join(target.Workspace, "dist.old")
	stage := filepath.Join(m.config.ResultDir, formatID(task.ID)+".dist.backup")
	m.progress(ctx, task, 18, logValue, "备份当前前端产物")
	if err = m.files.CopyDirectory(dist, stage); err != nil {
		m.fail(context.Background(), task, logValue, fmt.Errorf("备份前端产物失败: %w", err), "not_required")
		return
	}
	if err = normalizeStaticTree(stage); err != nil {
		_ = m.files.RemoveAll(stage)
		m.fail(context.Background(), task, logValue, fmt.Errorf("规范前端备份权限失败: %w", err), "not_required")
		return
	}
	rolledBack := false
	rollback := func(cause error) {
		if rolledBack {
			return
		}
		rolledBack = true
		status := "success"
		if resetErr := m.resetWorktree(context.Background(), target, task.BeforeCommit); resetErr != nil {
			status = "failed: git " + sanitizeOutput(resetErr.Error())
		}
		if restoreErr := replaceStaticDirectory(m.files, stage, dist); restoreErr != nil {
			status = "failed: dist " + sanitizeOutput(restoreErr.Error())
		}
		m.fail(context.Background(), task, logValue, cause, status)
	}

	m.progress(ctx, task, 28, logValue, "拉取前端 main 分支更新")
	if output, pullErr := m.pull(ctx, target); pullErr != nil {
		rollback(fmt.Errorf("拉取前端失败: %w (%s)", pullErr, output))
		return
	}
	m.progress(ctx, task, 40, logValue, "执行 npm ci 安装锁定依赖")
	if output, installErr := m.runner.Run(ctx, target.Workspace, m.config.NPMPath, frontendInstallArgs(), nil); installErr != nil {
		rollback(fmt.Errorf("前端依赖安装失败: %w (%s)", installErr, output))
		return
	}
	m.progress(ctx, task, 58, logValue, "构建前端新版本")
	env := []string{"DDYIDC_VERSION=" + info.Remote, "DDYIDC_COMMIT=" + info.RemoteCommit, "DDYIDC_BRANCH=" + target.Branch, "DDYIDC_BUILD_TIME=" + time.Now().UTC().Format(time.RFC3339)}
	if output, buildErr := m.runner.Run(ctx, target.Workspace, m.config.NPMPath, []string{"run", "build"}, env); buildErr != nil {
		rollback(fmt.Errorf("前端构建失败: %w (%s)", buildErr, output))
		return
	}
	if err = normalizeStaticTree(dist); err != nil {
		rollback(fmt.Errorf("规范前端产物权限失败: %w", err))
		return
	}
	m.progress(ctx, task, 78, logValue, "执行前端版本与页面后置检查")
	if err = verifyFrontendVersion(filepath.Join(dist, "version.json"), info.RemoteCommit); err != nil {
		rollback(err)
		return
	}
	if err = m.verifyHTTP(ctx, target.StaticURL, "前端首页"); err != nil {
		rollback(err)
		return
	}
	if err = m.verifyHTTP(ctx, m.config.HealthURL, "后端及 DB/Redis 健康状态"); err != nil {
		rollback(err)
		return
	}
	if err = m.verifyHTTP(ctx, m.config.SmokeURL, "关键接口"); err != nil {
		rollback(err)
		return
	}
	_ = m.files.RemoveAll(backup)
	if err = replaceStaticDirectory(m.files, stage, backup); err != nil {
		rollback(fmt.Errorf("保留 dist.old 失败: %w", err))
		return
	}
	m.progress(ctx, task, 95, logValue, "前端更新后检查通过，旧产物已保留为 dist.old")
	m.succeed(context.Background(), task, logValue)
}

func frontendInstallArgs() []string {
	return []string{"ci", "--no-audit", "--no-fund"}
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
