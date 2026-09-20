package backup

import (
	"context"
	"ddyidc/pkg/cache"
	"errors"
	"fmt"
	"golang.org/x/sys/unix"
	"gorm.io/gorm/clause"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

func safeCommand(ctx context.Context, exe string, args ...string) error {
	cmd := exec.CommandContext(ctx, exe, args...)
	// Never log command output: SQL, credentials and customer data can appear there.
	if e := cmd.Run(); e != nil {
		return fmt.Errorf("%s 执行失败，请检查服务权限、磁盘和数据库状态", filepath.Base(exe))
	}
	return nil
}
func (s *Service) businessPayload(ctx context.Context, destination string) error {
	return safeCommand(ctx, "/opt/ddyidc/deploy/backup.sh", "--business", destination)
}
func (s *Service) Worker(ctx context.Context) error {
	lock, e := os.OpenFile(filepath.Join(s.Root, "backups/.backup.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if e != nil {
		return e
	}
	defer lock.Close()
	if e = unix.Flock(int(lock.Fd()), unix.LOCK_EX|unix.LOCK_NB); e != nil {
		return ErrBusy
	}
	defer unix.Flock(int(lock.Fd()), unix.LOCK_UN)
	// MySQL connection-level lock also excludes workers on a second application host.
	sqlDB, e := s.db.DB()
	if e != nil {
		return e
	}
	conn, e := sqlDB.Conn(ctx)
	if e != nil {
		return e
	}
	defer conn.Close()
	var acquired int
	if e = conn.QueryRowContext(ctx, "SELECT GET_LOCK('ddyidc.business-backup',0)").Scan(&acquired); e != nil || acquired != 1 {
		return ErrBusy
	}
	defer conn.ExecContext(context.Background(), "DO RELEASE_LOCK('ddyidc.business-backup')")
	var control Control
	if e = s.db.First(&control, 1).Error; e != nil {
		return e
	}
	var job Job
	if control.ActiveID != "" {
		job, e = s.Job(ctx, control.ActiveID)
		if e != nil {
			return e
		}
		if job.Status == "running" {
			s.finish(&job, errors.New("上次任务中断，未自动重做恢复；请检查本地恢复记录后重新发起"), job.CreatedAt)
			return nil
		}
	} else {
		job, e = s.enqueue(ctx, "scheduled", "", 0, "")
		if e != nil {
			return e
		}
	}
	start := time.Now()
	job.Status = "running"
	if e = s.db.Save(&job).Error; e != nil {
		return e
	}
	restart := false
	if job.Kind == "restore" {
		e = s.restore(ctx, &job, &restart)
	} else {
		e = s.backup(ctx, &job)
	}
	if finishErr := s.finish(&job, e, start); finishErr != nil {
		return errors.New("任务结果数据库写入失败，请检查本地 backups/results 记录；恢复服务未自动启动")
	}
	if restart {
		restartCtx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
		defer cancel()
		if startErr := safeCommand(restartCtx, "/usr/bin/systemctl", "start", "ddyidc.service"); startErr != nil {
			e = errors.New("数据处理完成但服务启动失败，请检查服务日志")
			s.finish(&job, e, start)
		}
	}
	if e != nil {
		return e
	}
	return nil
}
func (s *Service) backup(ctx context.Context, j *Job) error {
	var e error
	payload := filepath.Join(s.Root, "backups/jobs", j.ID)
	if e = os.MkdirAll(payload, 0700); e != nil {
		return errors.New("无法创建备份工作目录")
	}
	defer os.RemoveAll(payload)
	if e = s.businessPayload(ctx, payload); e != nil {
		return e
	}
	archive := filepath.Join(s.Root, "backups/daily", "business-"+j.ID+".tar.gz")
	if e = pack(payload, archive, s.key); e != nil {
		return errors.New("业务备份打包失败（检查附件特殊文件及磁盘空间）")
	}
	if e = readableByService(archive); e != nil {
		return e
	}
	j.LocalPath = archive
	info, e := os.Stat(archive)
	if e != nil {
		return e
	}
	j.LocalBytes = info.Size()
	j.SHA256, e = fileSHA(archive)
	if e != nil {
		return e
	}
	// Persist local success even when the subsequent OSS request fails.
	if e = s.db.Save(j).Error; e != nil {
		return errors.New("保存备份记录失败")
	}
	if e = s.retainLocal(archive); e != nil {
		j.Warning = "本地备份保留清理失败"
	}
	c, e := s.Config(ctx)
	if e != nil {
		return e
	}
	if c.Enabled {
		if e = s.upload(ctx, c, j); e != nil {
			return e
		}
		if e = s.pruneOSS(ctx, c); e != nil {
			j.Warning = "备份上传成功，但保留清理失败；下次备份会重试"
		}
	}
	return nil
}
func readableByService(path string) error {
	u, e := user.Lookup("ddyidc")
	if e != nil {
		return errors.New("缺少 ddyidc 服务账号")
	}
	uid, _ := strconv.Atoi(u.Uid)
	gid, _ := strconv.Atoi(u.Gid)
	if e = os.Chown(path, uid, gid); e != nil {
		return errors.New("设置备份读取权限失败")
	}
	return os.Chmod(path, 0600)
}
func (s *Service) retainLocal(archive string) error {
	now := time.Now()
	for _, entry := range []struct {
		name string
		days int
		copy bool
	}{{"daily", 7, false}, {"weekly", 28, now.Weekday() == time.Sunday}, {"monthly", 370, now.Day() == 1}} {
		dir := filepath.Join(s.Root, "backups", entry.name)
		if entry.copy {
			target := filepath.Join(dir, filepath.Base(archive))
			if e := os.Link(archive, target); e != nil && !os.IsExist(e) {
				return e
			}
		}
		files, e := os.ReadDir(dir)
		if e != nil {
			return e
		}
		for _, f := range files {
			if f.IsDir() || !strings.HasPrefix(f.Name(), "business-") || !strings.HasSuffix(f.Name(), ".tar.gz") {
				continue
			}
			info, e := f.Info()
			if e != nil {
				return e
			}
			if info.ModTime().Before(now.AddDate(0, 0, -entry.days)) {
				if e = os.Remove(filepath.Join(dir, f.Name())); e != nil {
					return e
				}
			}
		}
	}
	return nil
}
func importSQL(ctx context.Context, path string) error {
	f, e := os.Open(path)
	if e != nil {
		return errors.New("恢复 SQL 不存在")
	}
	defer f.Close()
	cmd := exec.CommandContext(ctx, "/usr/bin/mysql", "--protocol=socket", "--user=root", "--binary-mode=1", "--local-infile=0")
	cmd.Stdin = f
	if e = cmd.Run(); e != nil {
		return errors.New("数据库导入失败")
	}
	return nil
}
func (s *Service) restore(ctx context.Context, j *Job, restart *bool) (result error) {
	source, e := s.Job(ctx, j.SourceID)
	if e != nil {
		return errors.New("原备份不存在")
	}
	archive, e := s.Archive(ctx, source)
	if e != nil {
		return e
	}
	if filepath.Dir(archive) == filepath.Join(s.Root, "backups/downloads") {
		defer os.Remove(archive)
	}
	base := filepath.Join(s.Root, "backups/restore", j.ID)
	payload := filepath.Join(base, "incoming")
	rollback := filepath.Join(base, "rollback")
	if e = os.MkdirAll(payload, 0700); e != nil {
		return errors.New("创建恢复目录失败")
	}
	if e = unpack(archive, payload, s.key); e != nil {
		os.RemoveAll(base)
		return e
	}
	// Durable journal lives outside MySQL so interrupted restores remain diagnosable.
	phase := func(value string) { _ = os.WriteFile(filepath.Join(base, "phase"), []byte(value), 0600) }
	phase("validated")
	if e = safeCommand(ctx, "/usr/bin/systemctl", "stop", "ddyidc.service"); e != nil {
		return e
	}
	*restart = true
	if e = os.MkdirAll(rollback, 0700); e != nil {
		return errors.New("无法创建恢复前回滚目录")
	}
	if e = s.businessPayload(ctx, rollback); e != nil {
		return errors.New("恢复前本地备份失败，已取消恢复")
	}
	phase("rollback_ready")
	live := filepath.Join(s.Root, "storage")
	old := filepath.Join(base, "storage-before")
	swapped := false
	rollbackData := func(cause error) error {
		recovery, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
		defer cancel()
		if e := importSQL(recovery, filepath.Join(rollback, "mysql.sql")); e != nil {
			*restart = false
			phase("rollback_failed_service_stopped")
			return errors.New("恢复及数据库回滚失败，服务保持停止；请使用本地回滚副本恢复")
		}
		if swapped {
			if e := os.RemoveAll(live); e != nil {
				*restart = false
				return errors.New("附件回滚失败，服务保持停止")
			}
			if e := os.Rename(old, live); e != nil {
				*restart = false
				return errors.New("附件回滚失败，服务保持停止")
			}
		}
		phase("rolled_back")
		return fmt.Errorf("%s；已回滚到恢复前数据", cause.Error())
	}
	phase("importing_mysql")
	if e = importSQL(ctx, filepath.Join(payload, "mysql.sql")); e != nil {
		return rollbackData(e)
	}
	if e = os.Rename(live, old); e != nil {
		return rollbackData(errors.New("附件切换失败"))
	}
	swapped = true
	if e = os.Rename(filepath.Join(payload, "storage"), live); e != nil {
		return rollbackData(errors.New("附件切换失败"))
	}
	u, e := user.Lookup("ddyidc")
	if e != nil {
		return rollbackData(errors.New("服务用户不存在"))
	}
	uid, _ := strconv.Atoi(u.Uid)
	gid, _ := strconv.Atoi(u.Gid)
	if e = filepath.Walk(live, func(p string, info os.FileInfo, e error) error {
		if e != nil {
			return e
		}
		return os.Chown(p, uid, gid)
	}); e != nil {
		return rollbackData(errors.New("附件权限恢复失败"))
	}
	// The restored dump contains its own in-progress backup row. Close historical
	// active tasks, then finish() recreates this restore's audit row before restart.
	if e = s.db.Model(&Job{}).Where("status IN ?", []string{"queued", "running"}).Updates(map[string]any{"status": "interrupted", "error": "由数据库恢复替换的历史任务"}).Error; e != nil {
		return rollbackData(errors.New("恢复状态校验失败"))
	}
	if e = s.db.Clauses(clause.OnConflict{UpdateAll: true}).Create(&source).Error; e != nil {
		return rollbackData(errors.New("恢复备份来源记录失败"))
	}
	if e = s.db.Exec("UPDATE system.backup_control SET active_id='' WHERE id=1").Error; e != nil {
		return rollbackData(errors.New("恢复控制状态失败"))
	}
	if cache.RDB != nil {
		for _, pattern := range []string{"session:user:*", "session:admin:*", "session:sales:*"} {
			var cursor uint64
			for {
				keys, next, err := cache.RDB.Scan(ctx, cursor, pattern, 100).Result()
				if err != nil {
					*restart = false
					return errors.New("数据已恢复但会话清理失败，服务保持停止")
				}
				if len(keys) > 0 {
					if err = cache.RDB.Del(ctx, keys...).Err(); err != nil {
						*restart = false
						return errors.New("会话清理失败，服务保持停止")
					}
				}
				cursor = next
				if cursor == 0 {
					break
				}
			}
		}
	}
	phase("completed")
	j.Warning = "恢复前回滚副本保留在本地 backups/restore/" + j.ID
	return nil
}
