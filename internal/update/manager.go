package update

import (
	"context"
	"ddyidc/internal/shared/distsync"
	"ddyidc/internal/shared/workgate"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"ddyidc/internal/shared/snowflake"
	"github.com/redis/go-redis/v9"
	"gorm.io/gorm"
)

var ErrUpdateRunning = errors.New("已有更新任务正在执行")

type Manager struct {
	config     Config
	database   *gorm.DB
	redis      *redis.Client
	repo       *Repository
	runner     CommandRunner
	files      FileOps
	locker     Locker
	httpClient *http.Client
	freeSpace  func(string) (uint64, error)
	exit       func(int)
	startUnit  func(string, string, string) error
	pid        int
	mu         sync.Mutex
}

func NewManager(database *gorm.DB, redisClient *redis.Client, config Config) (*Manager, error) {
	config = config.normalized()
	if err := config.validate(); err != nil {
		return nil, err
	}
	manager := &Manager{config: config, database: database, redis: redisClient, repo: NewRepository(database), runner: OSRunner{}, files: OSFileOps{}, locker: NewRedisLocker(redisClient), httpClient: defaultHTTPClient(), freeSpace: diskFree, exit: os.Exit, startUnit: startSystemdUnit, pid: os.Getpid()}
	if err := manager.repo.VerifySchema(context.Background()); err != nil {
		return nil, fmt.Errorf("更新任务表未就绪，请先人工执行 migrations/001_create_update_tasks.sql: %w", err)
	}
	if err := manager.files.MkdirAll(config.ResultDir); err != nil {
		return nil, fmt.Errorf("创建更新结果目录: %w", err)
	}
	recoveryCtx, recoveryCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer recoveryCancel()
	if err := manager.recoverFrontend(recoveryCtx); err != nil {
		return nil, fmt.Errorf("recover interrupted frontend: %w", err)
	}
	if err := manager.recoverBackendPlan(recoveryCtx); err != nil {
		return nil, fmt.Errorf("recover interrupted backend: %w", err)
	}
	return manager, nil
}

func (m *Manager) Status(ctx context.Context, fetchRemote bool) (StatusView, error) {
	backend, err := m.versionInfo(ctx, TargetBackend, fetchRemote)
	if err != nil {
		return StatusView{}, err
	}
	frontend, err := m.versionInfo(ctx, TargetFrontend, fetchRemote)
	if err != nil {
		return StatusView{}, err
	}
	status := StatusView{Backend: backend, Frontend: frontend}
	if running, runningErr := m.repo.Running(ctx); runningErr != nil {
		return StatusView{}, runningErr
	} else if running != nil {
		view := viewTask(running)
		status.Running = &view
	}
	return status, nil
}

func (m *Manager) Task(ctx context.Context, id int64) (*TaskView, error) {
	task, err := m.repo.Get(ctx, id)
	if err != nil || task == nil {
		return nil, err
	}
	view := viewTask(task)
	return &view, nil
}

func (m *Manager) Tasks(ctx context.Context, page, pageSize int) ([]TaskView, int64, error) {
	tasks, total, err := m.repo.List(ctx, page, pageSize)
	if err != nil {
		return nil, 0, err
	}
	views := make([]TaskView, 0, len(tasks))
	for index := range tasks {
		views = append(views, viewTask(&tasks[index]))
	}
	return views, total, nil
}

func (m *Manager) Trigger(ctx context.Context, targetName string, adminID int64) (string, error) {
	if _, err := m.config.target(targetName); err != nil {
		return "", err
	}
 if reclamationConfigured() {
  target,_:=m.config.target(targetName)
  free,spaceErr:=m.freeSpace(target.Workspace)
  if spaceErr!=nil{return "",spaceErr}
  if free/1024/1024<m.config.MinFreeMB {
   var last struct{Status string;FinishedAt *time.Time}
   if err:=m.database.WithContext(ctx).Table("system.reclamation_state").Where("id=1").Take(&last).Error;err!=nil{return "",fmt.Errorf("自动回收状态未就绪")}
   if last.Status=="success"&&last.FinishedAt!=nil&&time.Since(*last.FinishedAt)<5*time.Minute{return "",fmt.Errorf("自动回收后空间仍不足，请核对必要数据容量或扩容")}
   if err:=m.requestReclamation(ctx);err!=nil{return "",fmt.Errorf("空间不足且自动回收调度失败")}
   return "",fmt.Errorf("空间不足，已安排系统自动回收，请稍后重试更新")
  }
 }
	m.mu.Lock()
	defer m.mu.Unlock()
	taskID := snowflake.GenID()
	token := formatID(taskID)
	acquired, err := m.locker.Acquire(ctx, token, m.config.timeout()+2*time.Minute)
	if err != nil {
		return "", fmt.Errorf("获取更新锁: %w", err)
	}
	if !acquired {
        if m.redis!=nil { if owner,e:=m.redis.Get(ctx,updateLockKey).Result();e==nil&&strings.HasPrefix(owner,"reclamation:"){return "",fmt.Errorf("生产资源正在自动回收，请稍后更新")} }
		return "", ErrUpdateRunning
	}
	if running, runningErr := m.repo.Running(ctx); runningErr != nil {
		_ = m.locker.Release(ctx, token)
		return "", runningErr
	} else if running != nil {
		if time.Since(running.StartedAt) <= m.config.timeout()+2*time.Minute {
			_ = m.locker.Release(ctx, token)
			return "", ErrUpdateRunning
		}
		_ = m.repo.Finish(ctx, running.ID, StatusFailed, "任务超时后服务恢复，已清理遗留状态", "unknown", running.Log+"\n遗留任务已标记失败")
	}
	info, err := m.versionInfo(ctx, targetName, false)
	if err != nil {
		_ = m.locker.Release(ctx, token)
		return "", err
	}
	task := &Task{ID: taskID, Target: targetName, Status: StatusRunning, Progress: 1, TriggerBy: adminID, Branch: info.Branch, BeforeCommit: info.CurrentCommit, TargetCommit: info.RemoteCommit, BeforeVersion: info.Current, TargetVersion: info.Remote, Feature: firstFeature(info.Changes), Log: "任务已创建", CreatedAt: time.Now(), StartedAt: time.Now()}
	if err = m.repo.Create(ctx, task); err != nil {
		_ = m.locker.Release(ctx, token)
		return "", err
	}
	go m.runTask(task)
	return token, nil
}

func firstFeature(changes []Change) string {
	if len(changes) == 0 {
		return ""
	}
	return changes[0].Feature
}

func (m *Manager) runTask(task *Task) {
 defer func(){ ctx,cancel:=context.WithTimeout(context.Background(),10*time.Second);defer cancel(); _=m.requestReclamation(ctx) }()
	ctx, cancel := context.WithTimeout(context.Background(), m.config.timeout())
	defer cancel()
	err := workgate.Run(ctx, func(ctx context.Context) error {
		if task.Target == TargetBackend {
			m.runBackend(ctx, task)
			return nil
		}
		m.runFrontend(ctx, task)
		return nil
	})
	if err != nil {
		m.fail(context.Background(), task, &taskLog{}, err, "not_required")
	}
}

func (m *Manager) progress(ctx context.Context, task *Task, progress int, logValue *taskLog, message string) {
	logValue.add(message)
	_ = m.repo.Update(ctx, task.ID, map[string]any{"progress": progress, "log": logValue.String()})
}

func (m *Manager) fail(ctx context.Context, task *Task, logValue *taskLog, err error, rollback string) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	logValue.add("失败: " + err.Error())
	finishErr := m.repo.Finish(ctx, task.ID, StatusFailed, sanitizeOutput(err.Error()), rollback, logValue.String())
	releaseErr := m.locker.Release(ctx, formatID(task.ID))
	if finishErr == nil && releaseErr == nil {
		_ = m.archiveFrontendJournal(task.ID)
	} else {
		_ = m.retireFrontendJournal(task.ID)
	}
}

func (m *Manager) succeed(ctx context.Context, task *Task, logValue *taskLog) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	logValue.add("更新完成")
	finishErr := m.repo.Finish(ctx, task.ID, StatusSuccess, "", "", logValue.String())
	releaseErr := m.locker.Release(ctx, formatID(task.ID))
	if finishErr == nil && releaseErr == nil {
		_ = m.archiveFrontendJournal(task.ID)
	} else {
		_ = m.retireFrontendJournal(task.ID)
	}
}

func (m *Manager) StartResultWatcher(ctx context.Context) {
	go func() {
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			if distsync.IsLeader(ctx) {
				_ = distsync.Work(ctx, func(ctx context.Context) error {
					bounded, cancel := context.WithTimeout(ctx, 30*time.Second)
					defer cancel()
					m.processHelperResults(bounded)
					return m.recoverFrontend(bounded)
				})
			}
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
}

func (m *Manager) processHelperResults(ctx context.Context) {
	paths, err := filepath.Glob(filepath.Join(m.config.ResultDir, "*.result.json"))
	if err != nil {
		return
	}
	for _, path := range paths {
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			continue
		}
		var result HelperResult
		if json.Unmarshal(data, &result) != nil {
			continue
		}
		id, parseErr := strconv.ParseInt(result.TaskID, 10, 64)
		if parseErr != nil {
			continue
		}
		existing, _ := m.repo.Get(ctx, id)
		if err = m.repo.Finish(ctx, id, result.Status, sanitizeOutput(result.Error), result.RollbackStatus, mergeTaskLogs(existing, result.Log)); err != nil {
			continue
		}
		if err = m.locker.Release(ctx, result.TaskID); err != nil {
			continue
		}
		_ = archiveHelperResult(path, result.FinishedAt)
		_ = os.Remove(m.config.helperPlanPath())
		_ = m.requestReclamation(ctx)
	}
}

func archiveHelperResult(path string, finishedAt time.Time) error {
	if finishedAt.IsZero() {
		finishedAt = time.Now()
	}
	directory := filepath.Join(filepath.Dir(path), "archive", finishedAt.UTC().Format("2006-01"))
	if err := os.MkdirAll(directory, 0o750); err != nil {
		return err
	}
	destination := filepath.Join(directory, filepath.Base(path))
	if err := os.Rename(path, destination); err != nil {
		return err
	}
	return os.Chmod(destination, 0o600)
}

func mergeTaskLogs(existing *Task, helperLog string) string {
	parts := make([]string, 0, 2)
	if existing != nil && strings.TrimSpace(existing.Log) != "" {
		parts = append(parts, sanitizeOutput(existing.Log))
	}
	if strings.TrimSpace(helperLog) != "" {
		parts = append(parts, sanitizeOutput(helperLog))
	}
	return strings.Join(parts, "\n")
}
