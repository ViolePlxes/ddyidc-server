package navigation

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"ddyidc/internal/shared/snowflake"
	"gorm.io/gorm"
)

// Category is the sidebar "大类" a group of navigation sections belongs to. It is
// editor-owned data: administrators add, rename, reorder and delete it like any
// other navigation record.
type Category struct {
	ID        int64     `json:"id,string" gorm:"primaryKey;autoIncrement:false"`
	Name      string    `json:"name"`
	Sort      int       `json:"sort"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

func (Category) TableName() string { return "system.navigation_categories" }

type CategoryInput struct {
	ID   string   `json:"id"`
	Name string   `json:"name"`
	IDs  []string `json:"ids"`
}

// categorySeed creates the table and its default rows on first use. An install
// whose migration for this table was skipped (or an older binary) must still be
// able to open the editor.
const categorySeed = `CREATE TABLE IF NOT EXISTS system.navigation_categories (
 id BIGINT PRIMARY KEY,
 name VARCHAR(32) NOT NULL,
 sort INT NOT NULL DEFAULT 0,
 created_at DATETIME(3) NOT NULL,
 updated_at DATETIME(3) NOT NULL,
 UNIQUE KEY navigation_category_name (name)
) ENGINE=InnoDB`

var defaultCategories = []string{"业务管理", "服务管理", "支持与设置", "账户与权益", "运营管理", "销售管理", "更多服务"}

func (s *Service) ensureCategories() error {
	if err := s.db.Exec(categorySeed).Error; err != nil {
		return err
	}
	now := time.Now().UTC()
	for index, name := range defaultCategories {
		row := Category{ID: int64(960000001 + index), Name: name, Sort: index, CreatedAt: now, UpdatedAt: now}
		if err := s.db.Where("name = ?", name).FirstOrCreate(&row, Category{Name: name}).Error; err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) Categories(ctx context.Context) ([]Category, error) {
	var out []Category
	err := s.db.WithContext(ctx).Order("sort ASC,id ASC").Find(&out).Error
	if err == nil {
		return out, nil
	}
	// Missing table or column: repair once and retry, so the editor never dead-ends.
	if seedErr := s.ensureCategories(); seedErr != nil {
		return nil, fmt.Errorf("%w (repair: %v)", err, seedErr)
	}
	out = nil
	return out, s.db.WithContext(ctx).Order("sort ASC,id ASC").Find(&out).Error
}

// categoryNames returns the current names so node writes can reject unknown
// categories instead of creating orphans.
func categoryNames(tx *gorm.DB) (map[string]bool, error) {
	var rows []Category
	if err := tx.Find(&rows).Error; err != nil {
		return nil, err
	}
	out := make(map[string]bool, len(rows))
	for _, row := range rows {
		out[row.Name] = true
	}
	return out, nil
}

func (s *Service) SaveCategory(ctx context.Context, id string, in CategoryInput) error {
	name := strings.TrimSpace(in.Name)
	if name == "" || utf8.RuneCountInString(name) > 32 {
		return errors.New("大类名称需为 1-32 个字符")
	}
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var duplicate int64
		if err := tx.Model(&Category{}).Where("name = ? AND id <> ?", name, id).Count(&duplicate).Error; err != nil {
			return err
		}
		if duplicate > 0 {
			return errors.New("已存在同名大类")
		}
		if id == "" {
			var max int
			if err := tx.Model(&Category{}).Select("COALESCE(MAX(sort), -1)").Scan(&max).Error; err != nil {
				return err
			}
			now := time.Now().UTC()
			return tx.Create(&Category{ID: snowflake.GenID(), Name: name, Sort: max + 1, CreatedAt: now, UpdatedAt: now}).Error
		}
		var existing Category
		if err := tx.Where("id = ?", id).Take(&existing).Error; err != nil {
			return errors.New("大类不存在")
		}
		if existing.Name == name {
			return nil
		}
		// Renaming rewrites the sections that point at the old name in the same
		// transaction, so the sidebar never shows an orphan category.
		if err := tx.Model(&Node{}).Where("category = ?", existing.Name).Update("category", name).Error; err != nil {
			return err
		}
		return tx.Model(&Category{}).Where("id = ?", id).Updates(map[string]any{"name": name, "updated_at": time.Now().UTC()}).Error
	})
}

func (s *Service) RemoveCategory(ctx context.Context, id string) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var existing Category
		if err := tx.Where("id = ?", id).Take(&existing).Error; err != nil {
			return errors.New("大类不存在")
		}
		var used int64
		if err := tx.Model(&Node{}).Where("category = ?", existing.Name).Count(&used).Error; err != nil {
			return err
		}
		if used > 0 {
			return fmt.Errorf("该大类下还有 %d 个分类，请先移动到其他大类", used)
		}
		return tx.Where("id = ?", id).Delete(&Category{}).Error
	})
}

func (s *Service) SortCategories(ctx context.Context, ids []string) error {
	if len(ids) == 0 {
		return errors.New("排序数据为空")
	}
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var rows []Category
		if err := tx.Find(&rows).Error; err != nil {
			return err
		}
		if len(rows) != len(ids) {
			return errors.New("大类数量已变化，请刷新后重试")
		}
		wanted := map[string]bool{}
		for _, row := range rows {
			wanted[fmt.Sprint(row.ID)] = true
		}
		for _, id := range ids {
			if !wanted[id] {
				return errors.New("排序数据不完整，请刷新后重试")
			}
			delete(wanted, id)
		}
		if len(wanted) != 0 {
			return errors.New("排序数据不完整，请刷新后重试")
		}
		now := time.Now().UTC()
		for index, id := range ids {
			if err := tx.Model(&Category{}).Where("id = ?", id).Updates(map[string]any{"sort": index, "updated_at": now}).Error; err != nil {
				return err
			}
		}
		return nil
	})
}
