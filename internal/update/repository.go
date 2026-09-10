package update

import (
	"context"
	"errors"
	"time"

	"gorm.io/gorm"
)

type Repository struct{ db *gorm.DB }

func NewRepository(database *gorm.DB) *Repository { return &Repository{db: database} }

func (r *Repository) VerifySchema(ctx context.Context) error {
	var count int64
	if err := r.db.WithContext(ctx).Raw("SELECT COUNT(*) FROM information_schema.columns WHERE table_schema = 'system' AND table_name = 'update_tasks' AND column_name IN ('id', 'status', 'branch', 'target_commit', 'rollback_status')").Scan(&count).Error; err != nil {
		return err
	}
	if count != 5 {
		return errors.New("缺少 system.update_tasks 表或必要字段")
	}
	return nil
}

func (r *Repository) Create(ctx context.Context, task *Task) error {
	return r.db.WithContext(ctx).Table(task.TableName()).Create(task).Error
}

func (r *Repository) Update(ctx context.Context, id int64, values map[string]any) error {
	return r.db.WithContext(ctx).Table((Task{}).TableName()).Where("id = ?", id).Updates(values).Error
}

func (r *Repository) Get(ctx context.Context, id int64) (*Task, error) {
	var task Task
	err := r.db.WithContext(ctx).Table(task.TableName()).Where("id = ?", id).First(&task).Error
	if err == gorm.ErrRecordNotFound {
		return nil, nil
	}
	return &task, err
}

func (r *Repository) Running(ctx context.Context) (*Task, error) {
	var task Task
	err := r.db.WithContext(ctx).Table(task.TableName()).Where("status = ?", StatusRunning).Order("created_at DESC").First(&task).Error
	if err == gorm.ErrRecordNotFound {
		return nil, nil
	}
	return &task, err
}

func (r *Repository) List(ctx context.Context, page, pageSize int) ([]Task, int64, error) {
	if page < 1 {
		page = 1
	}
	if pageSize < 1 || pageSize > 100 {
		pageSize = 20
	}
	var total int64
	query := r.db.WithContext(ctx).Table((Task{}).TableName())
	if err := query.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var tasks []Task
	err := query.Order("created_at DESC").Offset((page - 1) * pageSize).Limit(pageSize).Find(&tasks).Error
	return tasks, total, err
}

func (r *Repository) Finish(ctx context.Context, id int64, status, errorText, rollbackStatus, logText string) error {
	now := time.Now()
	return r.Update(ctx, id, map[string]any{"status": status, "progress": 100, "error": errorText, "rollback_status": rollbackStatus, "log": logText, "finished_at": &now})
}
