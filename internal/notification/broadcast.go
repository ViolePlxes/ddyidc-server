package notification

import (
	"context"
	"crypto/sha256"
	"ddyidc/internal/shared/snowflake"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"strings"
	"sync"
	"time"
)

type BroadcastInput struct {
	RequestID    string  `json:"requestId"`
	Title        string  `json:"title"`
	Summary      string  `json:"summary"`
	Content      string  `json:"content"`
	All          bool    `json:"all"`
	Recipients   []int64 `json:"recipients"`
	Reason       string  `json:"reason"`
	PreviewToken string  `json:"previewToken"`
	Confirm      bool    `json:"confirm"`
}
type Broadcast struct {
	ID                 int64      `json:"id,string" gorm:"primaryKey"`
	RequestID          string     `json:"requestId"`
	AdminID            int64      `json:"adminId,string"`
	Reason             string     `json:"reason"`
	OperatorIP         string     `json:"-"`
	Title              string     `json:"title"`
	Summary            string     `json:"summary"`
	Content            string     `json:"content"`
	AudienceDefinition string     `json:"audienceDefinition"`
	PayloadHash        string     `json:"-"`
	Status             string     `json:"status"`
	Total              int64      `json:"total"`
	Succeeded          int64      `json:"succeeded"`
	Failed             int64      `json:"failed"`
	Skipped            int64      `json:"skipped"`
	CreatedAt          time.Time  `json:"createdAt"`
	FinishedAt         *time.Time `json:"finishedAt"`
}

func (Broadcast) TableName() string { return "system.notification_broadcasts" }

type Target struct {
	BroadcastID   int64 `gorm:"primaryKey"`
	RecipientID   int64 `gorm:"primaryKey"`
	Status        string
	Attempts      int
	LastErrorCode string
	UpdatedAt     time.Time
}

func (Target) TableName() string { return "system.notification_broadcast_targets" }
func (s *Service) Allowed(ctx context.Context, id int64, action string) bool {
	var a struct {
		IsSuper bool
		Status  int
	}
	if s.DB.WithContext(ctx).Table("system.admins").Select("is_super,status").Where("id=?", id).Take(&a).Error != nil || a.Status != 1 {
		return false
	}
	if a.IsSuper {
		return true
	}
	col := map[string]string{"view": "can_view", "create": "can_create", "retry": "can_retry", "cancel": "can_cancel"}[action]
	if col == "" {
		return false
	}
	var grants int64
	if s.DB.WithContext(ctx).Table("system.permissions p").Joins("JOIN system.role_permissions rp ON rp.permission_id=p.id").Joins("JOIN system.roles r ON r.id=rp.role_id AND r.status=1").Joins("JOIN system.admin_roles ar ON ar.role_id=r.id").Where("ar.admin_id=? AND p.code=?", id, "notification:"+action).Count(&grants).Error != nil || grants == 0 {
		return false
	}
	// Optional per-admin caps can only narrow an existing role grant, never grant authority.
	var capRow struct{ Allowed bool }
	err := s.DB.WithContext(ctx).Table("system.notification_permissions").Select(col+" AS allowed").Where("admin_id=?", id).Take(&capRow).Error
	if err == gorm.ErrRecordNotFound {
		return true
	}
	return err == nil && capRow.Allowed
}
func broadcastHash(in BroadcastInput) string {
	in.RequestID = ""
	in.PreviewToken = ""
	in.Confirm = false
	raw, _ := json.Marshal(in)
	v := sha256.Sum256(raw)
	return hex.EncodeToString(v[:])
}
func validateBroadcast(in BroadcastInput) error {
	e := Event{Key: "broadcast", RecipientID: 1, Type: "marketing", Scene: "announcement", Title: in.Title, Summary: in.Summary, Content: in.Content}
	if e.Validate() != nil || strings.TrimSpace(in.Content) == "" || len(in.Recipients) > 10000 || (!in.All && len(in.Recipients) == 0) || (in.All && len(in.Recipients) > 0) {
		return ErrInput
	}
	return nil
}
func audience(tx *gorm.DB, in BroadcastInput) *gorm.DB {
	q := tx.Table("customer.customers").Where("status=1 AND deleted_at IS NULL")
	if !in.All {
		q = q.Where("id IN ?", in.Recipients)
	}
	return q
}
func (s *Service) Preview(ctx context.Context, admin int64, in BroadcastInput) (map[string]any, error) {
	if err := validateBroadcast(in); err != nil {
		return nil, err
	}
	var total int64
	if err := audience(s.DB.WithContext(ctx), in).Count(&total).Error; err != nil {
		return nil, err
	} // HMAC token binds actor + content hash; number is a preview, final audience is atomically fixed on submit.
	hash := broadcastHash(in)
	rev := uint64(0)
	for _, b := range []byte(hash[:8]) {
		rev = rev*256 + uint64(b)
	}
	return map[string]any{"total": total, "previewToken": s.token(admin, rev, "broadcast"), "payloadHash": hash}, nil
}
func (s *Service) CreateBroadcast(ctx context.Context, admin int64, ip string, in BroadcastInput) (Broadcast, error) {
	var out Broadcast
	if err := validateBroadcast(in); err != nil {
		return out, err
	}
	if !in.Confirm || len(in.RequestID) > 96 || strings.TrimSpace(in.RequestID) == "" || strings.TrimSpace(in.Reason) == "" || len(in.Reason) > 500 {
		return out, ErrInput
	}
	hash := broadcastHash(in)
	rev := uint64(0)
	for _, b := range []byte(hash[:8]) {
		rev = rev*256 + uint64(b)
	}
	tokenRev, e := s.parseToken(admin, in.PreviewToken, "broadcast")
	if e != nil || rev != tokenRev {
		return out, ErrInput
	}
	err := s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Take(&out, "request_id=?", in.RequestID).Error; err == nil {
			if out.PayloadHash != hash || out.AdminID != admin {
				return ErrConflict
			}
			return nil
		} else if err != gorm.ErrRecordNotFound {
			return err
		}
		raw, _ := json.Marshal(map[string]any{"all": in.All, "recipients": in.Recipients})
		out = Broadcast{ID: snowflake.GenID(), RequestID: in.RequestID, AdminID: admin, Reason: in.Reason, OperatorIP: ip, Title: in.Title, Summary: in.Summary, Content: in.Content, AudienceDefinition: string(raw), PayloadHash: hash, Status: "pending", CreatedAt: time.Now().UTC()}
		if err := tx.Create(&out).Error; err != nil {
			return err
		}
		query := audience(tx, in).Select("? AS broadcast_id,id AS recipient_id,? AS status,UTC_TIMESTAMP(6) AS updated_at", out.ID, "pending")
		r := tx.Exec("INSERT INTO system.notification_broadcast_targets(broadcast_id,recipient_id,status,updated_at) " + query.ToSQL(func(q *gorm.DB) *gorm.DB { return q.Find(&[]Target{}) }))
		if r.Error != nil {
			return r.Error
		}
		out.Total = r.RowsAffected
		if out.Total == 0 {
			return ErrInput
		}
		return tx.Model(&out).Update("total", out.Total).Error
	})
	if err != nil {
		var existing Broadcast
		if e := s.DB.WithContext(ctx).Take(&existing, "request_id=?", in.RequestID).Error; e == nil {
			if existing.PayloadHash == hash && existing.AdminID == admin {
				return existing, nil
			}
			return out, ErrConflict
		}
	}
	if err == nil {
		s.Wake()
	}
	return out, err
}
func (s *Service) processBroadcasts(ctx context.Context) error {
	var enabled string
	if s.DB.WithContext(ctx).Table("system.system_configs").Select("value").Where("`key`=?", "notification.broadcast.enabled").Scan(&enabled).Error != nil || (enabled != "true" && enabled != "1") {
		return nil
	}
	var jobs []Broadcast
	load := func(cursor int64) error {
		return s.DB.WithContext(ctx).Where("status IN ? AND id>?", []string{"pending", "sending"}, cursor).Order("id").Limit(1).Find(&jobs).Error
	}
	if err := load(s.broadcastCursor); err != nil {
		return err
	}
	if len(jobs) == 0 && s.broadcastCursor > 0 {
		s.broadcastCursor = 0
		if err := load(0); err != nil {
			return err
		}
	}
	for _, job := range jobs {
		s.broadcastCursor = job.ID
		var targets []Target
		if err := s.DB.WithContext(ctx).Where("broadcast_id=? AND status='pending'", job.ID).Limit(32).Find(&targets).Error; err != nil {
			return err
		}
		var wg sync.WaitGroup
		slots := make(chan struct{}, 8)
		for _, target := range targets {
			target := target
			slots <- struct{}{}
			wg.Add(1)
			go func() {
				defer wg.Done()
				defer func() { <-slots }()
				err := s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
					var locked Broadcast
					if err := tx.Clauses(clause.Locking{Strength: "SHARE"}).Take(&locked, "id=?", job.ID).Error; err != nil {
						return err
					}
					if locked.Status == "cancelled" {
						return nil
					}
					var t Target
					if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Take(&t, "broadcast_id=? AND recipient_id=?", target.BroadcastID, target.RecipientID).Error; err != nil {
						return err
					}
					if t.Status != "pending" {
						return nil
					}
					var n int64
					if err := tx.Table("customer.customers").Where("id=? AND status=1 AND deleted_at IS NULL", t.RecipientID).Count(&n).Error; err != nil {
						return err
					}
					status := "skipped"
					if n == 1 {
						err := s.deliverTx(tx, Event{Key: fmt.Sprintf("broadcast:%d", job.ID), RecipientID: t.RecipientID, Type: "marketing", Scene: "announcement", Title: job.Title, Summary: job.Summary, Content: job.Content, OccurredAt: job.CreatedAt})
						if err != nil {
							return err
						}
						status = "succeeded"
					}
					return tx.Model(&t).Updates(map[string]any{"status": status, "updated_at": time.Now().UTC()}).Error
				})
				if err != nil {
					_ = s.DB.WithContext(ctx).Model(&Target{}).Where("broadcast_id=? AND recipient_id=? AND status='pending'", job.ID, target.RecipientID).Updates(map[string]any{"status": "failed", "attempts": gorm.Expr("attempts+1"), "last_error_code": "enqueue_failed"}).Error
				}
			}()
		}
		wg.Wait()
		var counts []struct {
			Status string
			N      int64
		}
		if err := s.DB.WithContext(ctx).Model(&Target{}).Select("status,COUNT(*) n").Where("broadcast_id=?", job.ID).Group("status").Scan(&counts).Error; err != nil {
			return err
		}
		updates := map[string]any{"succeeded": 0, "failed": 0, "skipped": 0}
		pending := int64(0)
		failed := int64(0)
		for _, v := range counts {
			if v.Status == "pending" {
				pending = v.N
			} else {
				updates[v.Status] = v.N
				if v.Status == "failed" {
					failed = v.N
				}
			}
		}
		status := "sending"
		if pending == 0 {
			status = "completed"
			if failed > 0 {
				status = "partial_failed"
			}
			updates["finished_at"] = time.Now().UTC()
		}
		updates["status"] = status
		if err := s.DB.WithContext(ctx).Model(&Broadcast{}).Where("id=? AND status<>'cancelled'", job.ID).Updates(updates).Error; err != nil {
			return err
		}
		if len(targets) > 0 {
			s.Wake()
		}
	}
	return nil
}
