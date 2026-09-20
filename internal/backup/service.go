package backup

import (
	"context"
	"ddyidc/internal/shared/runtimepaths"
	"ddyidc/internal/shared/snowflake"
	"encoding/json"
	"errors"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

var ErrBusy = errors.New("已有备份或恢复任务正在进行中")

type Job struct {
	ID         string     `json:"id" gorm:"primaryKey;size:36"`
	Kind       string     `json:"kind" gorm:"size:16"`
	Status     string     `json:"status" gorm:"size:16;index"`
	SourceID   string     `json:"sourceId" gorm:"size:36"`
	ActorID    int64      `json:"actorId,string"`
	IP         string     `json:"-" gorm:"size:64"`
	LocalPath  string     `json:"-" gorm:"size:1024"`
	LocalBytes int64      `json:"localBytes"`
	OSSBytes   int64      `json:"ossBytes"`
	ObjectKey  string     `json:"objectKey" gorm:"size:512"`
	Bucket     string     `json:"bucket" gorm:"size:64"`
	Endpoint   string     `json:"endpoint" gorm:"size:128"`
	SHA256     string     `json:"sha256" gorm:"size:64"`
	Uploaded   bool       `json:"uploaded"`
	Expired    bool       `json:"expired"`
	DurationMS int64      `json:"durationMs"`
	Error      string     `json:"error" gorm:"size:512"`
	Warning    string     `json:"warning" gorm:"size:512"`
	CreatedAt  time.Time  `json:"createdAt"`
	UpdatedAt  time.Time  `json:"updatedAt"`
	FinishedAt *time.Time `json:"finishedAt"`
}

func (Job) TableName() string { return "system.backup_history" }

type Control struct {
	ID       int    `gorm:"primaryKey"`
	ActiveID string `gorm:"size:36"`
}

func (Control) TableName() string { return "system.backup_control" }

type Audit struct {
	ID        uint64 `gorm:"primaryKey;autoIncrement"`
	ActorID   int64
	Action    string `gorm:"size:32"`
	JobID     string `gorm:"size:36"`
	IP        string `gorm:"size:64"`
	CreatedAt time.Time
}

func (Audit) TableName() string { return "system.backup_audit" }

type Service struct {
	db   *gorm.DB
	key  []byte
	Root string
}

func New(db *gorm.DB, key []byte, root string) *Service {
	return &Service{db: db, key: key, Root: root}
}
func (s *Service) EnsureSchema() error {
	if e := s.db.AutoMigrate(&Job{}, &Control{}, &Audit{}); e != nil {
		return e
	}
	if e := s.db.Clauses(clause.OnConflict{DoNothing: true}).Create(&Control{ID: 1}).Error; e != nil {
		return e
	}
	for _, p := range []string{"backups", "backups/daily", "backups/weekly", "backups/monthly", "backups/jobs", "backups/downloads", "backups/restore", "backups/results"} {
		path := filepath.Join(s.Root, p)
		if e := (runtimepaths.Paths{Root: s.Root}).Within(path); e != nil {
			return e
		}
		if e := os.MkdirAll(path, 0700); e != nil {
			return e
		}
	}
	return nil
}
func (s *Service) SeedNavigation() error {
	return s.db.Transaction(func(tx *gorm.DB) error {
		result := tx.Exec("INSERT IGNORE INTO system.system_configs(id,`key`,value,description) VALUES(?,'backup.navigation_seeded','true','备份导航迁移')", snowflake.GenID())
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return nil
		}
		var group struct{ ID int64 }
		if e := tx.Table("system.navigation_nodes").Select("parent_id AS id").Where("scope='admin' AND href='/admin/updates'").Take(&group).Error; e != nil {
			if e = tx.Table("system.navigation_nodes").Select("id").Where("scope='admin' AND parent_id=0").Order("sort,id").Take(&group).Error; e != nil {
				return e
			}
		}
		var order int
		tx.Table("system.navigation_nodes").Select("COALESCE(MAX(sort),0)+1").Where("scope='admin' AND parent_id=?", group.ID).Scan(&order)
		if e := tx.Exec("INSERT INTO system.navigation_nodes(id,scope,parent_id,name,href,target,icon,gate,enabled,sort,created_at,updated_at) VALUES(?,'admin',?,'数据库备份','/admin/backups','self','CloudArrowDownIcon','',true,?,?,?)", snowflake.GenID(), group.ID, order, time.Now(), time.Now()).Error; e != nil {
			return e
		}
		return tx.Table("system.navigation_scopes").Where("scope='admin'").Update("revision", gorm.Expr("revision+1")).Error
	})
}
func (s *Service) Job(ctx context.Context, id string) (Job, error) {
	var j Job
	if _, e := uuid.Parse(id); e != nil {
		return j, errors.New("备份编号无效")
	}
	e := s.db.WithContext(ctx).First(&j, "id=?", id).Error
	return j, e
}
func (s *Service) History(ctx context.Context, page int) ([]Job, int64, error) {
	if page < 1 {
		page = 1
	}
	if page > 5 {
		page = 5
	}
	var n int64
	var items []Job
	db := s.db.WithContext(ctx)
	if e := db.Model(&Job{}).Count(&n).Error; e != nil {
		return nil, 0, e
	}
	if n > 100 {
		n = 100
	}
	e := db.Order("created_at DESC,id DESC").Offset((page - 1) * 20).Limit(20).Find(&items).Error
	return items, n, e
}
func (s *Service) enqueue(ctx context.Context, kind, source string, actor int64, ip string) (Job, error) {
	j := Job{ID: uuid.NewString(), Kind: kind, SourceID: source, Status: "queued", ActorID: actor, IP: ip}
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var c Control
		if e := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&c, 1).Error; e != nil {
			return e
		}
		if c.ActiveID != "" {
			return ErrBusy
		}
		if kind == "restore" {
			var src Job
			if e := tx.First(&src, "id=? AND kind<>? AND sha256<>''", source, "restore").Error; e != nil {
				return errors.New("请选择可用的业务备份")
			}
		}
		if e := tx.Create(&j).Error; e != nil {
			return e
		}
		if e := tx.Model(&c).Update("active_id", j.ID).Error; e != nil {
			return e
		}
		return tx.Create(&Audit{ActorID: actor, Action: kind, JobID: j.ID, IP: ip}).Error
	})
	return j, err
}
func (s *Service) Trigger(ctx context.Context, kind, source string, actor int64, ip string) (Job, error) {
	var j Job
	e := s.db.WithContext(ctx).Connection(func(tx *gorm.DB) error {
		var acquired int
		if err := tx.Raw("SELECT GET_LOCK('ddyidc.business-backup',0)").Scan(&acquired).Error; err != nil {
			return errors.New("备份锁不可用")
		}
		if acquired != 1 {
			return ErrBusy
		}
		defer tx.Exec("DO RELEASE_LOCK('ddyidc.business-backup')")
		var err error
		j, err = s.enqueue(ctx, kind, source, actor, ip)
		return err
	})
	if e != nil {
		return j, e
	}
	commandCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if e = exec.CommandContext(commandCtx, "/usr/bin/sudo", "-n", "/usr/bin/systemctl", "--no-block", "restart", "ddyidc-backup.service").Run(); e != nil {
		s.finish(&j, errors.New("启动备份任务失败，请检查备份服务安装与权限"), time.Now())
		return j, errors.New("备份服务未安装或启动失败")
	}
	return j, nil
}
func (s *Service) finish(j *Job, cause error, start time.Time) error {
	now := time.Now()
	j.FinishedAt = &now
	j.DurationMS = time.Since(start).Milliseconds()
	j.Status = "success"
	if cause != nil {
		j.Status = "failed"
		j.Error = cause.Error()
	}
	// Restore may have replaced these tables; reconstruct the final audit state.
	// Keep a durable, non-secret result if the database is unavailable.
	resultDir := filepath.Join(s.Root, "backups/results")
	if err := os.MkdirAll(resultDir, 0700); err != nil {
		return errors.New("无法写入备份结果记录")
	}
	raw, err := json.Marshal(j)
	if err != nil {
		return err
	}
	if err = os.WriteFile(filepath.Join(resultDir, j.ID+".json"), raw, 0600); err != nil {
		return errors.New("无法保存备份结果记录")
	}
	return s.db.Transaction(func(tx *gorm.DB) error {
		if e := tx.Clauses(clause.OnConflict{UpdateAll: true}).Create(j).Error; e != nil {
			return e
		}
		if e := tx.Model(&Control{}).Where("id=1").Update("active_id", "").Error; e != nil {
			return e
		}
		return tx.Create(&Audit{ActorID: j.ActorID, Action: j.Kind + "_" + j.Status, JobID: j.ID, IP: j.IP}).Error
	})
}
