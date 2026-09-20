package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"ddyidc/internal/update"
	"io"
)

const defaultMinFreeMB = 10 * 1024

func main() {
	planPath := flag.String("plan", "", "更新计划文件")
	flag.Parse()
	if strings.TrimSpace(*planPath) == "" {
		return
	}
	data, err := os.ReadFile(*planPath)
	if err != nil {
		return
	}
	var plan update.HelperPlan
	if json.Unmarshal(data, &plan) != nil || !validPlan(plan) {
		return
	}
	result := execute(plan)
	if writeResult(plan.ResultFile, result) == nil {
		_ = os.Remove(*planPath)
	}
}

func validPlan(plan update.HelperPlan) bool {
	paths := []string{plan.NewExecutable, plan.CurrentExecutable, plan.BackupExecutable, plan.Workspace, plan.GitPath, plan.ServerDirectory, plan.ConfigFile, plan.ResultFile, plan.SystemctlPath, plan.SudoPath}
	for _, path := range paths {
		if strings.TrimSpace(path) == "" || !filepath.IsAbs(path) {
			return false
		}
	}
	return plan.ParentPID > 0 && plan.TaskID != "" && plan.ExpectedCommit != "" && validServiceName(plan.ServiceName)
}

func validServiceName(value string) bool {
	value = strings.TrimSpace(value)
	if value == "" || strings.HasPrefix(value, "-") || !strings.HasSuffix(value, ".service") {
		return false
	}
	for _, character := range value {
		if (character >= 'a' && character <= 'z') || (character >= 'A' && character <= 'Z') || (character >= '0' && character <= '9') || strings.ContainsRune("_.@-", character) {
			continue
		}
		return false
	}
	return true
}

func execute(plan update.HelperPlan) update.HelperResult {
	result := update.HelperResult{TaskID: plan.TaskID, Status: update.StatusFailed, FinishedAt: time.Now()}
	logLines := []string{"更新助手已由 systemd 接管后端切换"}
	timeout := time.Duration(plan.TimeoutSeconds) * time.Second
	if timeout <= 0 || timeout > 10*time.Minute {
		timeout = 10 * time.Minute
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	minimumMB := plan.MinFreeMB
	if minimumMB == 0 {
		minimumMB = defaultMinFreeMB
	}
	free, err := diskFree(filepath.Dir(plan.CurrentExecutable))
	if err != nil {
		return failedResult(plan, logLines, "not_required", fmt.Errorf("检查目标卷空间失败: %w", err))
	}
	freeMB := free / 1024 / 1024
	if freeMB < minimumMB {
		return failedResult(plan, logLines, "not_required", fmt.Errorf("目标卷可用空间 %d MiB，至少需要 %d MiB", freeMB, minimumMB))
	}
	logLines = append(logLines, fmt.Sprintf("目标卷空间检查通过：%d MiB 可用", freeMB))

	if err = systemctl(ctx, plan.SudoPath, plan.SystemctlPath, "stop", plan.ServiceName); err != nil {
		return failedResult(plan, logLines, "not_required", fmt.Errorf("停止旧后端服务失败: %w", err))
	}
	logLines = append(logLines, "旧后端服务已停止")
	if err = atomicReplace(plan.NewExecutable, plan.CurrentExecutable, plan.BackupExecutable); err != nil {
		return rollback(plan, append(logLines, "原子替换运行文件失败"), err)
	}
	logLines = append(logLines, "新后端已原子切换，旧版本保留在数据目录")
	if err = systemctl(ctx, plan.SudoPath, plan.SystemctlPath, "start", plan.ServiceName); err != nil {
		return rollback(plan, logLines, fmt.Errorf("启动新后端服务失败: %w", err))
	}
	logLines = append(logLines, "新后端服务已启动")
	if err = waitHealthAndVersion(ctx, plan.HealthURL, plan.VersionURL, plan.SmokeURL, plan.ExpectedCommit); err != nil {
		return rollback(plan, logLines, err)
	}
	logLines = append(logLines, "健康、版本与关键接口后置检查通过")
	result.Status = update.StatusSuccess
	result.Log = strings.Join(logLines, "\n")
	result.FinishedAt = time.Now()
	return result
}

func failedResult(plan update.HelperPlan, logLines []string, rollbackStatus string, cause error) update.HelperResult {
	message := safeError(cause)
	logLines = append(logLines, "更新失败: "+message)
	return update.HelperResult{TaskID: plan.TaskID, Status: update.StatusFailed, Error: message, RollbackStatus: rollbackStatus, Log: strings.Join(logLines, "\n"), FinishedAt: time.Now()}
}

func rollback(plan update.HelperPlan, logLines []string, cause error) update.HelperResult {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	rollbackErrors := make([]string, 0, 4)
	if err := systemctl(ctx, plan.SudoPath, plan.SystemctlPath, "stop", plan.ServiceName); err != nil {
		rollbackErrors = append(rollbackErrors, "停止失败版本: "+safeError(err))
	}
	if err := restoreBackup(plan.CurrentExecutable, plan.BackupExecutable); err != nil {
		rollbackErrors = append(rollbackErrors, "恢复后端二进制: "+safeError(err))
	}
	command := exec.CommandContext(ctx, plan.GitPath, "reset", "--hard", plan.BeforeCommit)
	command.Dir = plan.Workspace
	if output, err := command.CombinedOutput(); err != nil {
		rollbackErrors = append(rollbackErrors, "恢复 Git: "+safeError(fmt.Errorf("%w (%s)", err, output)))
	}
	if len(rollbackErrors) == 0 {
		if err := systemctl(ctx, plan.SudoPath, plan.SystemctlPath, "start", plan.ServiceName); err != nil {
			rollbackErrors = append(rollbackErrors, "重启旧后端: "+safeError(err))
		} else if err = waitHealth(ctx, plan.HealthURL); err != nil {
			rollbackErrors = append(rollbackErrors, "旧后端健康检查: "+safeError(err))
		}
	}
	status := "success"
	if len(rollbackErrors) > 0 {
		status = "failed: " + strings.Join(rollbackErrors, "; ")
	}
	logLines = append(logLines, "更新失败: "+safeError(cause), "回滚状态: "+status)
	return update.HelperResult{TaskID: plan.TaskID, Status: update.StatusFailed, Error: safeError(cause), RollbackStatus: status, Log: strings.Join(logLines, "\n"), FinishedAt: time.Now()}
}

func diskFree(path string) (uint64, error) {
	var stat syscall.Statfs_t
	if err := syscall.Statfs(path, &stat); err != nil {
		return 0, err
	}
	return stat.Bavail * uint64(stat.Bsize), nil
}

func systemctl(ctx context.Context, sudoPath, executable, action, service string) error {
	if !validServiceName(service) || (action != "start" && action != "stop" && action != "restart") {
		return errors.New("无效的 systemd 操作")
	}
	command := exec.CommandContext(ctx, sudoPath, "-n", executable, action, service)
	output, err := command.CombinedOutput()
	if err != nil {
		return fmt.Errorf("systemctl %s %s: %w (%s)", action, service, err, strings.TrimSpace(string(output)))
	}
	return nil
}

// Only the deployment helper can write the program mount. Candidates and backups
// live on the data mount, which may be a different filesystem.
func atomicReplace(source, destination, backup string) error {
	if source == destination || backup == destination || backup == source {
		return errors.New("update paths must be distinct")
	}
	if _, err := os.Stat(source); err != nil {
		return err
	}
	if err := durableCopy(destination, backup); err != nil {
		return err
	}
	return durableCopy(source, destination)
}
func restoreBackup(destination, backup string) error {
	if err := durableCopy(backup, destination); err != nil {
		return err
	}
	return os.Remove(backup)
}
func durableCopy(source, destination string) error {
	input, err := os.Open(source)
	if err != nil {
		return err
	}
	defer input.Close()
	info, err := input.Stat()
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return errors.New("executable is not a regular file")
	}
	if err = os.MkdirAll(filepath.Dir(destination), 0750); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(destination), ".deploy-*")
	if err != nil {
		return err
	}
	defer os.Remove(temporary.Name())
	defer temporary.Close()
	if _, err = io.Copy(temporary, input); err != nil {
		return err
	}
	if err = temporary.Chmod(info.Mode().Perm()); err != nil {
		return err
	}
	if err = temporary.Sync(); err != nil {
		return err
	}
	if err = temporary.Close(); err != nil {
		return err
	}
	if err = os.Rename(temporary.Name(), destination); err != nil {
		return err
	}
	return syncDirectory(filepath.Dir(destination))
}

func syncDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}

func waitHealthAndVersion(ctx context.Context, healthURL, versionURL, smokeURL, expected string) error {
	if err := waitHealth(ctx, healthURL); err != nil {
		return err
	}
	if err := waitCommit(ctx, versionURL, expected); err != nil {
		return err
	}
	if err := waitStatus(ctx, smokeURL); err != nil {
		return fmt.Errorf("关键接口检查失败: %w", err)
	}
	return nil
}

func waitHealth(ctx context.Context, url string) error { return waitStatus(ctx, url) }

func waitStatus(ctx context.Context, url string) error {
	client := &http.Client{Timeout: 3 * time.Second}
	var lastErr error
	for ctx.Err() == nil {
		request, _ := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		response, err := client.Do(request)
		if err == nil {
			_ = response.Body.Close()
			if response.StatusCode == http.StatusOK {
				return nil
			}
			lastErr = fmt.Errorf("HTTP %d", response.StatusCode)
		} else {
			lastErr = err
		}
		time.Sleep(500 * time.Millisecond)
	}
	return fmt.Errorf("等待服务超时: %w", lastErr)
}

func waitCommit(ctx context.Context, url, expected string) error {
	client := &http.Client{Timeout: 3 * time.Second}
	var lastErr error
	for ctx.Err() == nil {
		request, _ := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		response, err := client.Do(request)
		if err == nil {
			var payload struct {
				Data struct {
					Commit string `json:"commit"`
				} `json:"data"`
				Commit string `json:"commit"`
			}
			decodeErr := json.NewDecoder(response.Body).Decode(&payload)
			_ = response.Body.Close()
			actual := payload.Commit
			if actual == "" {
				actual = payload.Data.Commit
			}
			if response.StatusCode == http.StatusOK && actual == expected {
				return nil
			}
			lastErr = fmt.Errorf("目标 commit=%s，实际=%s，HTTP=%d，解析错误=%v", expected, actual, response.StatusCode, decodeErr)
		} else {
			lastErr = err
		}
		time.Sleep(500 * time.Millisecond)
	}
	return fmt.Errorf("版本检查超时: %w", lastErr)
}

func writeResult(path string, result update.HelperResult) error {
	data, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return err
	}
	if err = os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	temporary := path + ".writing"
	if err = os.WriteFile(temporary, data, 0o600); err != nil {
		return err
	}
	return os.Rename(temporary, path)
}

func safeError(err error) string {
	if err == nil {
		return ""
	}
	value := err.Error()
	if len(value) > 4096 {
		value = value[len(value)-4096:]
	}
	return strings.TrimSpace(value)
}
