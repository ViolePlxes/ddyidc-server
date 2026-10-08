package update

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func firstFailed(checks []CheckResult) error {
	for _, check := range checks {
		if !check.OK {
			return errors.New(check.Name + ": " + check.Message)
		}
	}
	return nil
}

func (m *Manager) preflight(ctx context.Context, name string) ([]CheckResult, error) {
	target, err := m.config.target(name)
	if err != nil {
		return nil, err
	}
	checks := make([]CheckResult, 0, 10)
	run := func(label string, check func() (string, error)) {
		message, checkErr := check()
		if checkErr != nil {
			checks = append(checks, CheckResult{Name: label, OK: false, Message: sanitizeOutput(checkErr.Error())})
			return
		}
		checks = append(checks, CheckResult{Name: label, OK: true, Message: sanitizeOutput(message)})
	}
	run("受限构建基础设施", func() (string, error) {
		return "构建启动器及部署模板哈希一致", verifyBuildInfrastructure(ctx, m.config.SudoPath)
	})
	run("Git 工具", func() (string, error) {
		return m.runner.Run(ctx, target.Workspace, m.config.GitPath, []string{"--version"}, nil)
	})
	run("Git 工作区", func() (string, error) {
		inside, e := m.git(ctx, target.Workspace, "rev-parse", "--is-inside-work-tree")
		if e != nil || strings.TrimSpace(inside) != "true" {
			return "", errors.New("不是 Git 工作区")
		}
		branch, e := m.git(ctx, target.Workspace, "branch", "--show-current")
		if e != nil {
			return "", e
		}
		if strings.TrimSpace(branch) != target.Branch {
			return "", fmt.Errorf("当前分支 %s 与配置分支 %s 不一致", branch, target.Branch)
		}
		dirty, e := m.git(ctx, target.Workspace, "status", "--porcelain")
		if e != nil {
			return "", e
		}
		if strings.TrimSpace(dirty) != "" {
			return "", errors.New("工作区存在未提交改动，拒绝更新")
		}
		return "分支 " + target.Branch + "，工作区干净", nil
	})
	run("GitHub 连通", func() (string, error) {
		output, e := m.fetch(ctx, target)
		if e != nil {
			return "", e
		}
		if output == "" {
			output = "fetch 成功"
		}
		return output, nil
	})
	toolLabel, toolPath := "Node/npm", m.config.NPMPath
	toolArgs := toolVersionArgs(name)
	if name == TargetBackend {
		toolLabel, toolPath = "Go 工具链", m.config.GoPath
	}
	run(toolLabel, func() (string, error) {
		if name == TargetBackend {
			return m.runner.Run(ctx, "/", toolPath, toolArgs, []string{"GOTOOLCHAIN=local", "GOWORK=off"})
		}
		return m.runner.Run(ctx, target.Workspace, toolPath, toolArgs, nil)
	})
	run("磁盘空间", func() (string, error) {
		diskTarget := target.Workspace
		if name == TargetBackend {
			diskTarget = filepath.Dir(target.Executable)
		}
		free, e := m.freeSpace(diskTarget)
		if e != nil {
			return "", e
		}
		freeMB := free / 1024 / 1024
		if freeMB < m.config.MinFreeMB {
			return "", fmt.Errorf("可用 %dMB，至少需要 %dMB", freeMB, m.config.MinFreeMB)
		}
		return fmt.Sprintf("可用 %dMB", freeMB), nil
	})
	run("后端进程与端口", func() (string, error) {
		if _, e := os.Stat(m.config.Backend.Executable); e != nil {
			return "", fmt.Errorf("运行文件不可用: %w", e)
		}
		status, _, e := m.httpGet(ctx, m.config.HealthURL)
		if e != nil || status != http.StatusOK {
			return "", fmt.Errorf("8080 健康检查失败: status=%d err=%v", status, e)
		}
		return "运行文件存在，8080 正常", nil
	})
	run("MySQL", func() (string, error) {
		var one int
		if e := m.database.WithContext(ctx).Raw("SELECT 1").Scan(&one).Error; e != nil {
			return "", fmt.Errorf("数据库不可用: %w", e)
		}
		if one != 1 {
			return "", errors.New("数据库 SELECT 1 返回异常")
		}
		return "连接正常", nil
	})
	run("Redis", func() (string, error) {
		if e := m.redis.Ping(ctx).Err(); e != nil {
			return "", e
		}
		return "连接正常", nil
	})
	return checks, firstFailed(checks)
}

func toolVersionArgs(target string) []string {
	if target == TargetBackend {
		return []string{"version"}
	}
	return []string{"--version"}
}

func (m *Manager) httpGet(ctx context.Context, url string) (int, []byte, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return 0, nil, err
	}
	response, err := m.httpClient.Do(request)
	if err != nil {
		return 0, nil, err
	}
	defer response.Body.Close()
	data := make([]byte, 64*1024)
	n, readErr := response.Body.Read(data)
	if readErr != nil && n == 0 && readErr.Error() != "EOF" {
		return response.StatusCode, nil, readErr
	}
	return response.StatusCode, data[:n], nil
}

func defaultHTTPClient() *http.Client {
	return &http.Client{Timeout: 10 * time.Second}
}
