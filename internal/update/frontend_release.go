package update

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type frontendJournal struct {
	TaskID        int64  `json:"taskId"`
	OwnerPID      int    `json:"ownerPid"`
	OwnerIdentity string `json:"ownerIdentity"`
	Candidate     string `json:"candidate"`
	Commit        string `json:"commit"`
	BeforeCommit  string `json:"beforeCommit"`
	Phase         string `json:"phase"`
}

func (m *Manager) frontendRelease(ctx context.Context, task *Task, target TargetConfig, info VersionInfo, logValue *taskLog) error {
	// A legacy revision may not yet ignore .releases. Keep rollback/retry clean
	// independently of the version of its tracked .gitignore.
	if err := m.ignoreFrontendCandidates(ctx, target); err != nil {
		return err
	}
	candidate := filepath.Join(target.Workspace, ".releases", formatID(task.ID))
	j := frontendJournal{TaskID: task.ID, OwnerPID: m.pid, OwnerIdentity: processIdentity(m.pid), Candidate: candidate, Commit: info.RemoteCommit, BeforeCommit: task.BeforeCommit, Phase: "building"}
	journal := filepath.Join(m.config.ResultDir, formatID(task.ID)+".frontend.json")
	if err := m.files.WriteJSON(journal, j); err != nil {
		return err
	}
	rollback := func(cause error) error {
		cleanup, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if stopErr := m.stopBuild(cleanup, task.ID); stopErr != nil {
			return errors.Join(cause, stopErr)
		}
		restoreErr := m.restoreFrontend(cleanup, target, j)
		if restoreErr != nil {
			return errors.Join(cause, fmt.Errorf("rollback failed: %w", restoreErr))
		}
		j.Phase = "rolled_back"
		if e := m.files.WriteJSON(journal, j); e != nil {
			return errors.Join(cause, e)
		}
		return cause
	}
	job, err := m.prepareBuild(ctx, task, target, info)
	if err != nil {
		return rollback(err)
	}
	m.progress(ctx, task, 35, logValue, "使用无业务凭据的隔离账号安装锁定依赖")
	if out, e := m.isolatedBuild(ctx, task, "install"); e != nil {
		return rollback(fmt.Errorf("isolated install: %w (%s)", e, out))
	}
	m.progress(ctx, task, 55, logValue, "在独立候选目录构建，线上资源保持可用")
	if out, e := m.isolatedBuild(ctx, task, "frontend"); e != nil {
		return rollback(fmt.Errorf("isolated build: %w (%s)", e, out))
	}
	built := filepath.Join(job, "source", "dist")
	if err = validateReleaseTree(built, true); err != nil {
		return rollback(err)
	}
	if err = verifyFrontendVersion(filepath.Join(built, "version.json"), info.RemoteCommit); err != nil {
		return rollback(err)
	}
	if err = m.files.CopyDirectory(built, candidate); err != nil {
		return rollback(err)
	}
	live := filepath.Join(target.Workspace, "dist")
	// Old hashed resources remain available to browsers opened before the switch.
	oldAssets := filepath.Join(live, "assets")
	if _, e := os.Stat(oldAssets); e == nil && !reclamationConfigured() {
		if err = retainStaticAssets(oldAssets, filepath.Join(candidate, "assets")); err != nil {
			return rollback(err)
		}
	}
	if err = validateReleaseTree(candidate, true); err != nil {
		return rollback(err)
	}
	if err = normalizeStaticTree(candidate); err != nil {
		return rollback(err)
	}
	j.Phase = "prepared"
	if err = m.files.WriteJSON(journal, j); err != nil {
		return rollback(err)
	}
	if out, e := m.pull(ctx, target); e != nil {
		return rollback(fmt.Errorf("pull: %w (%s)", e, out))
	}
	m.progress(ctx, task, 78, logValue, "候选版本校验通过，原子切换静态资源")
	if err = exchangeDirectories(candidate, live); err != nil {
		return rollback(err)
	}
	if err = syncDirectory(target.Workspace); err != nil {
		return rollback(err)
	}
	j.Phase = "switched"
	if err = m.files.WriteJSON(journal, j); err != nil {
		return rollback(err)
	}
	for _, check := range []struct{ url, name string }{{target.StaticURL, "前端首页"}, {m.config.HealthURL, "后端健康"}, {m.config.SmokeURL, "关键接口"}} {
		if err = m.verifyHTTP(ctx, check.url, check.name); err != nil {
			return rollback(err)
		}
	}
	j.Phase = "committed"
	if err = m.files.WriteJSON(journal, j); err != nil {
		return rollback(err)
	}
	return nil
}

func (m *Manager) ignoreFrontendCandidates(ctx context.Context, target TargetConfig) error {
	path, err := m.git(ctx, target.Workspace, "rev-parse", "--git-path", "info/exclude")
	if err != nil {
		return err
	}
	path = strings.TrimSpace(path)
	if !filepath.IsAbs(path) {
		path = filepath.Join(target.Workspace, path)
	}
	raw, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	for _, line := range strings.Split(string(raw), "\n") {
		if strings.TrimSpace(line) == "/.releases/" {
			return nil
		}
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer file.Close()
	if _, err = file.WriteString("\n/.releases/\n"); err != nil {
		return err
	}
	return file.Sync()
}

func (m *Manager) frontendRollbackStatus(taskID int64) string {
	raw, err := os.ReadFile(filepath.Join(m.config.ResultDir, formatID(taskID)+".frontend.json"))
	if os.IsNotExist(err) {
		return "not_required"
	}
	if err != nil {
		return "failed"
	}
	var j frontendJournal
	if json.Unmarshal(raw, &j) != nil {
		return "failed"
	}
	if j.Phase == "rolled_back" {
		return "success"
	}
	return "failed"
}

func retainStaticAssets(source, destination string) error {
	return filepath.WalkDir(source, func(path string, entry os.DirEntry, e error) error {
		if e != nil {
			return e
		}
		relative, _ := filepath.Rel(source, path)
		target := filepath.Join(destination, relative)
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("unsafe retained asset: %s", relative)
		}
		if entry.IsDir() {
			return os.MkdirAll(target, 0755)
		}
		if _, e := os.Stat(target); e == nil {
			return nil
		}
		return (OSFileOps{}).CopyFile(path, target)
	})
}

func (m *Manager) restoreFrontend(ctx context.Context, target TargetConfig, j frontendJournal) error {
	live := filepath.Join(target.Workspace, "dist")
	if verifyFrontendVersion(filepath.Join(live, "version.json"), j.Commit) == nil {
		if _, err := os.Stat(filepath.Join(live, "assets")); err == nil && !reclamationConfigured() {
			if err = retainStaticAssets(filepath.Join(live, "assets"), filepath.Join(j.Candidate, "assets")); err != nil {
				return err
			}
		}
		if err := exchangeDirectories(j.Candidate, live); err != nil {
			return err
		}
	}
	return m.resetWorktree(ctx, target, j.BeforeCommit)
}

func (m *Manager) recoverFrontend(ctx context.Context) error {
	paths, err := filepath.Glob(filepath.Join(m.config.ResultDir, "*.frontend.json"))
	if err != nil {
		return err
	}
	for _, path := range paths {
		raw, e := os.ReadFile(path)
		if e != nil {
			return e
		}
		var j frontendJournal
		if e = json.Unmarshal(raw, &j); e != nil {
			return e
		}
		task, e := m.repo.Get(ctx, j.TaskID)
		if e != nil {
			return e
		}
		if task == nil {
			continue
		}
		if j.OwnerPID > 0 && j.OwnerIdentity != "" && processIdentity(j.OwnerPID) == j.OwnerIdentity {
			continue
		}
		if task.Status != StatusRunning {
			if e = m.locker.Release(ctx, formatID(j.TaskID)); e != nil {
				return e
			}
			if e = m.archiveFrontendJournal(j.TaskID); e != nil {
				return e
			}
			continue
		}
		if j.Phase == "rolled_back" {
			if e = m.repo.Finish(ctx, j.TaskID, StatusFailed, "发布失败，旧版本已恢复", "success", "从持久日志恢复回滚结果"); e != nil {
				return e
			}
		} else if j.Phase == "committed" {
			if e = m.repo.Finish(ctx, j.TaskID, StatusSuccess, "", "not_required", "从持久日志确认更新已完成"); e != nil {
				return e
			}
		} else {
			expected := filepath.Join(m.config.Frontend.Workspace, ".releases", formatID(j.TaskID))
			if j.Candidate != expected {
				return errors.New("unsafe frontend recovery path")
			}
			if e = m.stopBuild(ctx, j.TaskID); e != nil {
				return e
			}
			if e = m.restoreFrontend(ctx, m.config.Frontend, j); e != nil {
				return e
			}
			j.Phase = "rolled_back"
			if e = m.files.WriteJSON(path, j); e != nil {
				return e
			}
			if e = m.repo.Finish(ctx, j.TaskID, StatusFailed, "更新进程中断，已恢复旧版本", "success", "持久日志恢复完成；未恢复或改动业务数据库"); e != nil {
				return e
			}
		}
		if e = m.locker.Release(ctx, formatID(j.TaskID)); e != nil {
			return e
		}
		if e = m.archiveFrontendJournal(j.TaskID); e != nil {
			return e
		}
	}
	return nil
}

func (m *Manager) archiveFrontendJournal(taskID int64) error {
	filename := filepath.Join(m.config.ResultDir, formatID(taskID)+".frontend.json")
	if _, err := os.Stat(filename); os.IsNotExist(err) {
		return nil
	}
	return archiveHelperResult(filename, time.Now())
}

func (m *Manager) retireFrontendJournal(taskID int64) error {
	filename := filepath.Join(m.config.ResultDir, formatID(taskID)+".frontend.json")
	raw, err := os.ReadFile(filename)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	var journal frontendJournal
	if err = json.Unmarshal(raw, &journal); err != nil {
		return err
	}
	journal.OwnerPID = 0
	journal.OwnerIdentity = ""
	return m.files.WriteJSON(filename, journal)
}

func processIdentity(pid int) string {
	stat, e := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if e != nil {
		return ""
	}
	closing := strings.LastIndex(string(stat), ")")
	if closing < 0 {
		return ""
	}
	fields := strings.Fields(string(stat)[closing+1:])
	if len(fields) < 20 {
		return ""
	}
	boot, e := os.ReadFile("/proc/sys/kernel/random/boot_id")
	if e != nil {
		return ""
	}
	return strings.TrimSpace(string(boot)) + ":" + fields[19]
}
