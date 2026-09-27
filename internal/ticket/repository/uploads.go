package ticket

import (
	"context"
	"ddyidc/internal/shared/filebarrier"
	"encoding/json"
	"errors"
	"time"

	dom "ddyidc/internal/ticket/domain"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type Upload struct {
	ID          string
	OwnerType   string
	OwnerID     int64
	Name        string
	Size        int64
	ContentType string
	SHA256      string `gorm:"column:sha256"`
	State       string
	TicketID    *int64
	CreatedAt   time.Time
	ExpiresAt   time.Time
}

func (Upload) TableName() string { return "ticket.uploads" }

func (r *Repository) WithFileSnapshot(ctx context.Context, fn func(context.Context) error) error {
	return filebarrier.With(ctx, r.db, fn)
}

func (r *Repository) ReserveUpload(ctx context.Context, u *Upload) error {
	if u.OwnerID <= 0 || (u.OwnerType != dom.ActorClient && u.OwnerType != dom.ActorAdmin) || u.Size <= 0 || u.Size > 10<<20 {
		return errors.New("附件参数无效")
	}
	return r.Transaction(ctx, func(tx *gorm.DB) error {
		if err := tx.Exec("INSERT IGNORE INTO ticket.upload_owners(owner_type,owner_id) VALUES(?,?)", u.OwnerType, u.OwnerID).Error; err != nil {
			return err
		}
		var owner struct{ OwnerID int64 }
		if err := tx.Table("ticket.upload_owners").Clauses(clause.Locking{Strength: "UPDATE"}).Where("owner_type=? AND owner_id=?", u.OwnerType, u.OwnerID).Take(&owner).Error; err != nil {
			return err
		}
		var daily struct {
			Count int64
			Bytes int64
		}
		if err := tx.Model(&Upload{}).Select("COUNT(*) AS count, COALESCE(SUM(size),0) AS bytes").Where("owner_type=? AND owner_id=? AND created_at>=?", u.OwnerType, u.OwnerID, time.Now().Add(-24*time.Hour)).Scan(&daily).Error; err != nil {
			return err
		}
		var pending int64
		if err := tx.Model(&Upload{}).Where("owner_type=? AND owner_id=? AND state IN ('reserved','ready','deleting')", u.OwnerType, u.OwnerID).Count(&pending).Error; err != nil {
			return err
		}
		if daily.Count >= 50 || daily.Bytes+u.Size > 200<<20 || pending >= 20 {
			return errors.New("附件上传配额已用完，请稍后重试")
		}
		u.State = "reserved"
		u.CreatedAt = time.Now()
		u.ExpiresAt = u.CreatedAt.Add(30 * time.Minute)
		return tx.Create(u).Error
	})
}
func (r *Repository) CompleteUpload(ctx context.Context, id, hash string, size int64) error {
	res := r.db.WithContext(ctx).Model(&Upload{}).Where("id=? AND state='reserved' AND expires_at>? AND size=?", id, time.Now(), size).Updates(map[string]any{"state": "ready", "sha256": hash, "expires_at": time.Now().Add(24 * time.Hour)})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected != 1 {
		return errors.New("附件上传已失效")
	}
	return nil
}
func (r *Repository) DiscardUpload(ctx context.Context, id string) (bool, error) {
	res := r.db.WithContext(ctx).Model(&Upload{}).Where("id=? AND state IN ('reserved','ready')", id).Updates(map[string]any{"state": "deleting", "expires_at": time.Now()})
	return res.RowsAffected == 1, res.Error
}
func (r *Repository) FinishDelete(ctx context.Context, id string) error {
	return r.db.WithContext(ctx).Model(&Upload{}).Where("id=? AND state='deleting'", id).Update("state", "deleted").Error
}

// Bind within the ticket/reply transaction. Trust only the opaque id supplied
// by the client; filename, size and MIME always come from the owned registry.
func (r *Repository) bindUploads(tx *gorm.DB, raw json.RawMessage, ownerType string, ownerID, ticketID int64) (json.RawMessage, error) {
	var items []dom.Attachment
	if err := json.Unmarshal(raw, &items); err != nil {
		return nil, errors.New("附件信息无效")
	}
	if len(items) > 5 {
		return nil, errors.New("每次最多上传5个附件")
	}
	seen := map[string]bool{}
	for i, item := range items {
		if seen[item.ID] {
			return nil, errors.New("附件不能重复绑定")
		}
		seen[item.ID] = true
		var u Upload
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=? AND owner_type=? AND owner_id=? AND state='ready' AND expires_at>?", item.ID, ownerType, ownerID, time.Now()).Take(&u).Error; err != nil {
			return nil, errors.New("附件不属于当前账号、已绑定或已过期")
		}
		if err := tx.Model(&u).Updates(map[string]any{"state": "bound", "ticket_id": ticketID}).Error; err != nil {
			return nil, err
		}
		items[i] = dom.Attachment{ID: u.ID, Name: u.Name, Size: u.Size, ContentType: u.ContentType}
	}
	if items == nil {
		items = []dom.Attachment{}
	}
	return json.Marshal(items)
}

func (r *Repository) ExpiredUploads(ctx context.Context) ([]Upload, error) {
	var out []Upload
	err := r.Transaction(ctx, func(tx *gorm.DB) error {
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE", Options: "SKIP LOCKED"}).Where("state='deleting' OR (state IN ('reserved','ready') AND expires_at<=?)", time.Now()).Order("expires_at,id").Limit(100).Find(&out).Error; err != nil {
			return err
		}
		for _, u := range out {
			if err := tx.Model(&Upload{}).Where("id=?", u.ID).Update("state", "deleting").Error; err != nil {
				return err
			}
		}
		return nil
	})
	return out, err
}
