package backup

import (
	"context"
	"ddyidc/internal/shared/filebarrier"
	"ddyidc/internal/shared/snowflake"
	"ddyidc/internal/shared/workgate"
	"ddyidc/pkg/cache"
	"encoding/json"
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
	return filebarrier.With(ctx, s.db, func(ctx context.Context) error {
		if err := safeCommand(ctx, "/opt/ddyidc/deploy/backup.sh", "--business", destination); err != nil {
			return err
		}
		return copyStorage(ctx, s.Root, filepath.Join(destination, "storage"))
	})
}
func (s *Service) Worker(ctx context.Context) error {
	if s.privateRoot == "" || len(s.signingKey) != 32 {
		return errors.New("维护目录与独立签名密钥未初始化")
	}
	lock, e := os.OpenFile(filepath.Join(s.privateRoot, ".backup.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if e != nil {
		return e
	}
	defer lock.Close()
	if e = unix.Flock(int(lock.Fd()), unix.LOCK_EX|unix.LOCK_NB); e != nil {
		return ErrBusy
	}
	defer unix.Flock(int(lock.Fd()), unix.LOCK_UN)
	if e = s.pendingRecovery(); e != nil {
		return e
	}
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
	metadata, e := s.snapshotMetadata(ctx)
	if e != nil {
		return e
	}
	payload := filepath.Join(s.privateRoot, "jobs", j.ID)
	if e = os.MkdirAll(payload, 0700); e != nil {
		return errors.New("无法创建备份工作目录")
	}
	defer os.RemoveAll(payload)
	if e = s.businessPayload(ctx, payload); e != nil {
		return e
	}
	archive := filepath.Join(s.Root, "backups/daily", "business-"+j.ID+".tar.gz")
	finished, e := s.snapshotMetadata(ctx)
	if e != nil {
		return e
	}
	metadata.FinishedAt = finished.StartedAt
	if metadata.ApplicationSHA256 != finished.ApplicationSHA256 {
		return errors.New("备份期间应用产物发生变化，请在升级完成后重试")
	}
	if len(metadata.Schemas) != len(finished.Schemas) {
		return errors.New("备份期间数据库版本变化")
	}
	for n, v := range metadata.Schemas {
		if v != finished.Schemas[n] {
			return errors.New("备份期间数据库版本变化")
		}
	}
	if finished.IDHighWater > metadata.IDHighWater {
		metadata.IDHighWater = finished.IDHighWater
	}
	privateArchive := filepath.Join(payload, "business.tar.gz")
	if e = packMetadata(payload, privateArchive, s.key, s.signingKey, metadata); e != nil {
		return errors.New("业务备份打包失败（检查附件特殊文件及磁盘空间）")
	}
	if e = s.publishArchive(privateArchive, filepath.Join("backups/daily", filepath.Base(archive))); e != nil {
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
func (s *Service) retainLocal(archive string) error {
	root, e := os.OpenRoot(s.Root)
	if e != nil {
		return e
	}
	defer root.Close()
	archiveRel, e := filepath.Rel(s.Root, archive)
	if e != nil {
		return e
	}
	now := time.Now()
	for _, entry := range []struct {
		name string
		days int
		copy bool
	}{{"daily", 7, false}, {"weekly", 28, now.Weekday() == time.Sunday}, {"monthly", 370, now.Day() == 1}} {
		dir := filepath.Join("backups", entry.name)
		if entry.copy {
			target := filepath.Join(dir, filepath.Base(archive))
			if e := root.Link(archiveRel, target); e != nil && !os.IsExist(e) {
				return e
			}
		}
		directory, e := root.Open(dir)
		if e != nil {
			return e
		}
		files, e := directory.ReadDir(-1)
		directory.Close()
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
				if e = root.Remove(filepath.Join(dir, f.Name())); e != nil {
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
	base := filepath.Join(s.privateRoot, "restore", j.ID)
	payload := filepath.Join(base, "incoming")
	rollback := filepath.Join(base, "rollback")
	if e = os.MkdirAll(payload, 0700); e != nil {
		return errors.New("创建恢复目录失败")
	}
	var savedMetadata *SnapshotMetadata
	if e = unpackSigned(archive, payload, s.key, s.signingKey, &savedMetadata); e != nil {
		os.RemoveAll(base)
		return e
	}
	currentMetadata, e := s.snapshotMetadata(ctx)
	if e != nil {
		return e
	}
	if e = metadataCompatible(savedMetadata, currentMetadata); e != nil {
		return e
	}
	// Durable journal lives outside MySQL so interrupted restores remain diagnosable.
	phase := func(value string) error { return writeDurable(filepath.Join(base, "phase"), []byte(value)) }
	if e = phase("validated"); e != nil {
		return errors.New("无法持久保存恢复日志，未修改业务数据")
	}
	accounts, e := runtimeAccounts(ctx, s.db, s.runtimeAccount)
	if e != nil {
		return e
	}
	rawAccounts, e := json.Marshal(accounts)
	if e != nil {
		return e
	}
	if e = writeDurable(filepath.Join(base, "runtime-accounts.json"), rawAccounts); e != nil {
		return errors.New("账户隔离计划未持久保存")
	}
	if e = writeReview(base, nil, nil); e != nil {
		return errors.New("恢复核对清单未持久保存")
	}
	pool, e := s.db.DB()
	if e != nil {
		return e
	}
	drainCtx, drainCancel := context.WithTimeout(ctx, 5*time.Minute)
	e = (&workgate.Gate{DB: pool}).Pause(drainCtx, "restore:"+j.ID)
	drainCancel()
	if e != nil {
		return errors.New("全节点工作排空失败，未导入；请检查维护屏障状态")
	}
	if e = phase("drained"); e != nil {
		return errors.New("工作已暂停但阶段日志写入失败，禁止导入")
	}
	if e = fenceAccounts(ctx, s.db, accounts); e != nil {
		return e
	}
	if e = phase("runtime_accounts_fenced"); e != nil {
		return errors.New("账户已隔离但日志写入失败，禁止导入")
	}
	// Keep accounts locked after every outcome until explicit fact reconciliation.
	*restart = false
	if e = safeCommand(ctx, "/usr/bin/systemctl", "stop", "ddyidc.service"); e != nil {
		return e
	}
	*restart = false
	frozenMetadata, e := s.snapshotMetadata(ctx)
	if e != nil {
		return errors.New("账户隔离后读取ID高水位失败")
	}
	highWater := frozenMetadata.IDHighWater
	if savedMetadata.IDHighWater > highWater {
		highWater = savedMetadata.IDHighWater
	}
	beforeFacts, e := s.recoveryFacts(ctx)
	if e != nil {
		return e
	}
	factsJSON, e := json.Marshal(beforeFacts)
	if e != nil {
		return e
	}
	if e = writeDurable(filepath.Join(base, "before-facts.json"), factsJSON); e != nil {
		return errors.New("恢复前核对事实未持久保存")
	}
	if e = os.MkdirAll(rollback, 0700); e != nil {
		return errors.New("无法创建恢复前回滚目录")
	}
	if e = s.businessPayload(ctx, rollback); e != nil {
		return errors.New("恢复前本地备份失败，已取消恢复")
	}
	if e = phase("rollback_ready"); e != nil {
		return errors.New("回滚准备日志写入失败，应用账户保持隔离")
	}
	live := filepath.Join(s.Root, "storage")
	old := filepath.Join(base, "storage-before")
	swapped := false
	rollbackData := func(cause error) error {
		recovery, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
		defer cancel()
		if e := importSQL(recovery, filepath.Join(rollback, "mysql.sql")); e != nil {
			*restart = false
			_ = phase("rollback_failed_service_stopped")
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
		rolledFacts, err := s.recoveryFacts(recovery)
		if err != nil {
			return errors.New("回滚后事实读取失败，账户保持隔离")
		}
		if err = writeReview(base, beforeFacts, rolledFacts); err != nil {
			return errors.New("回滚后核对清单失败，账户保持隔离")
		}
		if err := phase("rolled_back"); err != nil {
			return errors.New("数据已回滚但日志写入失败，账户保持隔离")
		}
		return fmt.Errorf("%s；已回滚到恢复前数据", cause.Error())
	}
	if e = phase("importing_mysql"); e != nil {
		return errors.New("导入阶段日志写入失败，禁止导入")
	}
	if e = importSQL(ctx, filepath.Join(payload, "mysql.sql")); e != nil {
		return rollbackData(e)
	}
	if e = s.db.WithContext(ctx).Exec("UPDATE system.id_sequences SET high_water=GREATEST(high_water,?) WHERE name='global'", highWater).Error; e != nil {
		return rollbackData(errors.New("ID高水位恢复保护失败"))
	}
	if pool, err := s.db.DB(); err != nil {
		return rollbackData(errors.New("ID登记库恢复保护失败"))
	} else if e = snowflake.Prepare(ctx, pool); e != nil {
		return rollbackData(errors.New("随机ID登记库恢复保护失败"))
	}
	if e = s.db.WithContext(ctx).Exec("UPDATE system.durable_tasks SET status='unknown',lease_token='',lease_until=NULL,safe_error='数据库恢复后等待外部事实核对' WHERE status IN ('pending','running')").Error; e != nil {
		return rollbackData(errors.New("恢复后任务隔离失败"))
	}
	restoredFacts, e := s.recoveryFacts(ctx)
	if e != nil {
		return rollbackData(e)
	}
	if e = writeReview(base, beforeFacts, restoredFacts); e != nil {
		return rollbackData(errors.New("恢复差异清单保存失败"))
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
	if e = phase("awaiting_reconciliation"); e != nil {
		return errors.New("导入完成但日志写入失败，应用账户保持隔离")
	}
	j.Warning = "数据导入完成，应用账户保持隔离，须核对外部交易后人工放行；维护记录 /var/lib/ddyidc-maintenance/restore/" + j.ID
	return nil
}
