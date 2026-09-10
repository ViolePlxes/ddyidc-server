package main

import (
	"context"
	"encoding/json"
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
	"golang.org/x/sys/windows"
)

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
	_ = writeResult(plan.ResultFile, result)
}

func validPlan(plan update.HelperPlan) bool {
	paths := []string{plan.NewExecutable, plan.CurrentExecutable, plan.BackupExecutable, plan.Workspace, plan.GitPath, plan.ServerDirectory, plan.ConfigFile, plan.ResultFile}
	for _, path := range paths {
		if strings.TrimSpace(path) == "" || !filepath.IsAbs(path) {
			return false
		}
	}
	return plan.ParentPID > 0 && plan.TaskID != "" && plan.ExpectedCommit != ""
}

func execute(plan update.HelperPlan) update.HelperResult {
	result := update.HelperResult{TaskID: plan.TaskID, Status: update.StatusFailed, FinishedAt: time.Now()}
	logLines := []string{"更新助手已接管后端切换"}
	timeout := time.Duration(plan.TimeoutSeconds) * time.Second
	if timeout <= 0 || timeout > 10*time.Minute {
		timeout = 10 * time.Minute
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	if err := stopProcess(plan.ParentPID); err != nil {
		result.Error = safeError(fmt.Errorf("停止旧后端失败: %w", err))
		result.RollbackStatus = "not_required"
		result.Log = strings.Join(append(logLines, result.Error), "\n")
		return result
	}
	logLines = append(logLines, "旧后端进程已停止")
	if err := replaceExecutable(ctx, plan.NewExecutable, plan.CurrentExecutable); err != nil {
		return rollback(ctx, plan, nil, append(logLines, "替换运行文件失败"), err)
	}
	logLines = append(logLines, "新后端运行文件已就位")
	command, err := startServer(plan.CurrentExecutable, plan.ConfigFile, plan.ServerDirectory)
	if err != nil {
		return rollback(ctx, plan, nil, logLines, fmt.Errorf("启动新后端失败: %w", err))
	}
	logLines = append(logLines, "新后端进程已启动")
	if err = waitHealthAndVersion(ctx, plan.HealthURL, plan.VersionURL, plan.SmokeURL, plan.ExpectedCommit); err != nil {
		return rollback(ctx, plan, command, logLines, err)
	}
	if command.Process != nil {
		_ = command.Process.Release()
	}
	logLines = append(logLines, "健康、版本与关键接口后置检查通过")
	result.Status = update.StatusSuccess
	result.Log = strings.Join(logLines, "\n")
	result.FinishedAt = time.Now()
	return result
}

func rollback(ctx context.Context, plan update.HelperPlan, running *exec.Cmd, logLines []string, cause error) update.HelperResult {
	rollbackCtx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	if running != nil && running.Process != nil {
		_ = running.Process.Kill()
		_, _ = running.Process.Wait()
	}
	rollbackErrors := []string{}
	if err := copyFile(plan.BackupExecutable, plan.CurrentExecutable); err != nil {
		rollbackErrors = append(rollbackErrors, "恢复 exe: "+safeError(err))
	}
	command := exec.CommandContext(rollbackCtx, plan.GitPath, "reset", "--hard", plan.BeforeCommit)
	command.Dir = plan.Workspace
	if output, err := command.CombinedOutput(); err != nil {
		rollbackErrors = append(rollbackErrors, "恢复 Git: "+safeError(fmt.Errorf("%w (%s)", err, output)))
	}
	if len(rollbackErrors) == 0 {
		oldServer, err := startServer(plan.CurrentExecutable, plan.ConfigFile, plan.ServerDirectory)
		if err != nil {
			rollbackErrors = append(rollbackErrors, "重启旧后端: "+safeError(err))
		} else if err = waitHealth(rollbackCtx, plan.HealthURL); err != nil {
			if oldServer.Process != nil {
				_ = oldServer.Process.Kill()
			}
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

func stopProcess(pid int) error {
	process, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	if err = process.Kill(); err != nil {
		return err
	}
	_, _ = process.Wait()
	return nil
}

func replaceExecutable(ctx context.Context, source, destination string) error {
	var lastErr error
	for ctx.Err() == nil {
		if err := os.Remove(destination); err != nil && !os.IsNotExist(err) {
			lastErr = err
			time.Sleep(150 * time.Millisecond)
			continue
		}
		if err := os.Rename(source, destination); err == nil {
			return nil
		} else {
			lastErr = err
		}
		time.Sleep(150 * time.Millisecond)
	}
	return fmt.Errorf("替换超时: %w", lastErr)
}

func startServer(executable, configFile, directory string) (*exec.Cmd, error) {
	command := exec.Command(executable, "-f", configFile)
	command.Dir = directory
	command.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.CREATE_NEW_PROCESS_GROUP | windows.DETACHED_PROCESS, HideWindow: true}
	if err := command.Start(); err != nil {
		return nil, err
	}
	return command, nil
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

func waitHealth(ctx context.Context, url string) error {
	return waitStatus(ctx, url)
}

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

func copyFile(source, destination string) error {
	data, err := os.ReadFile(source)
	if err != nil {
		return err
	}
	temporary := destination + ".restoring"
	if err = os.WriteFile(temporary, data, 0o755); err != nil {
		return err
	}
	_ = os.Remove(destination)
	return os.Rename(temporary, destination)
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
	_ = os.Remove(path)
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
