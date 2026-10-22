package notification

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"ddyidc/internal/shared/snowflake"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	mysqlerr "github.com/go-sql-driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"log"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

type Service struct {
	broadcastCursor int64
	nextCleanup     time.Time
	unavailable     atomic.Bool
	Admit           func(context.Context, func(context.Context) error) error
	DB              *gorm.DB
	Hub             *Hub
	Publisher       Publisher
	key             []byte
	wake            chan struct{}
}

func New(db *gorm.DB, key []byte) *Service {
	h := NewHub()
	s := &Service{DB: db, Hub: h, Publisher: h, key: key, wake: make(chan struct{}, 1)}
	setWake(s.Wake)
	wakeMu.Lock()
	disconnectFn = h.DisconnectAll
	wakeMu.Unlock()
	return s
}
func (s *Service) transaction(ctx context.Context, fn func(*gorm.DB) error) error {
	run := func(c context.Context) error {
		var err error
		for attempt := 0; attempt < 8; attempt++ {
			err = s.DB.WithContext(c).Transaction(fn)
			var dbErr *mysqlerr.MySQLError
			if !errors.As(err, &dbErr) || (dbErr.Number != 1213 && dbErr.Number != 1205) {
				return err
			}
			select {
			case <-c.Done():
				return c.Err()
			case <-time.After(time.Duration(1<<attempt) * time.Millisecond):
			}
		}
		return err
	}
	if s.Admit != nil {
		return s.Admit(ctx, run)
	}
	return run(ctx)
}
func (s *Service) Wake() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}
func (s *Service) Enabled(ctx context.Context) bool {
	if s.unavailable.Load() {
		return false
	}
	var v string
	return s.DB.WithContext(ctx).Table("system.system_configs").Select("value").Where("`key`=?", "notification.inapp.enabled").Scan(&v).Error == nil && (v == "true" || v == "1")
}
func lockState(tx *gorm.DB, id int64) (*State, error) {
	v := State{RecipientID: id, UpdatedAt: time.Now().UTC()}
	if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&v).Error; err != nil {
		return nil, err
	}
	err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Take(&v, "recipient_id=?", id).Error
	return &v, err
}
func bump(tx *gorm.DB, st *State, kind string, id *int64) error {
	markQueued(tx)
	st.Revision++
	st.UpdatedAt = time.Now().UTC()
	if err := tx.Save(st).Error; err != nil {
		return err
	}
	return tx.Create(&Change{RecipientID: st.RecipientID, Revision: st.Revision, Kind: kind, MessageID: id, PushStatus: "pending", CreatedAt: st.UpdatedAt}).Error
}
func (s *Service) Deliver(ctx context.Context, e Event) error {
	if err := e.Validate(); err != nil {
		return err
	}
	return s.transaction(ctx, func(tx *gorm.DB) error { return s.deliverTx(tx, e) })
}
func (s *Service) deliverTx(tx *gorm.DB, e Event) error {
	if e.OccurredAt.IsZero() {
		e.OccurredAt = time.Now().UTC()
	}
	st, err := lockState(tx, e.RecipientID)
	if err != nil {
		return err
	}
	var n int64
	if err = tx.Model(&Message{}).Where("recipient_id=? AND source_event_key=?", e.RecipientID, e.Key).Count(&n).Error; err != nil || n > 0 {
		return err
	}
	m := Message{ID: snowflake.GenID(), RecipientID: e.RecipientID, Type: e.Type, Scene: e.Scene, Title: e.Title, Summary: e.Summary, Content: e.Content, BusinessType: e.BusinessType, BusinessID: e.BusinessID, SourceEventKey: e.Key, SourceOccurredAt: e.OccurredAt, CreatedAt: time.Now().UTC(), CreatedRevision: st.Revision + 1}
	alreadyRead, err := ticketAlreadyRead(tx, e)
	if err != nil {
		return err
	}
	if alreadyRead {
		now := time.Now().UTC()
		m.ReadAt = &now
	}
	if err = tx.Create(&m).Error; err != nil {
		return err
	}
	return bump(tx, st, "created", &m.ID)
}

// Run is one singleton durable worker. It never polls an inbox per connection.
// A bounded global recovery tick covers committed events whose post-commit hint
// was lost (process death, legacy jobs). Idle SSE heartbeats execute no SQL.
func (s *Service) Run(ctx context.Context) {
	defer s.Hub.DisconnectAll()
	pool, err := s.DB.DB()
	if err != nil {
		s.unavailable.Store(true)
		return
	}
	conn, err := pool.Conn(ctx)
	if err != nil {
		s.unavailable.Store(true)
		return
	}
	defer conn.Close()
	var locked int
	if err = conn.QueryRowContext(ctx, "SELECT GET_LOCK('ddyidc.notification.singleton',0)").Scan(&locked); err != nil || locked != 1 {
		s.unavailable.Store(true)
		log.Print("notification_singleton_unavailable")
		return
	}
	defer conn.ExecContext(context.Background(), "SELECT RELEASE_LOCK('ddyidc.notification.singleton')")
	s.Wake()
	tick := time.NewTicker(30 * time.Second)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-s.wake:
		case <-tick.C:
		}
		if !s.Enabled(ctx) {
			continue
		}
		var owns int
		if err := conn.QueryRowContext(ctx, "SELECT IS_USED_LOCK('ddyidc.notification.singleton') = CONNECTION_ID()").Scan(&owns); err != nil || owns != 1 {
			s.unavailable.Store(true)
			return
		}
		for i := 0; i < 100; i++ {
			worked, err := s.step(ctx)
			if err != nil {
				if worked {
					s.Wake()
				}
				break
			}
			if !worked {
				break
			}
			if i == 99 {
				s.Wake()
			}
		}
		run := func(c context.Context) error {
			if err := s.flush(c); err != nil {
				return err
			}
			if err := s.processBroadcasts(c); err != nil {
				return err
			}
			return s.cleanup(c)
		}
		if s.Admit != nil {
			_ = s.Admit(ctx, run)
		} else {
			_ = run(ctx)
		}
	}
}
func (s *Service) step(ctx context.Context) (bool, error) {
	var row Outbox
	found := false
	err := s.transaction(ctx, func(tx *gorm.DB) error {
		err := tx.Clauses(clause.Locking{Strength: "UPDATE", Options: "SKIP LOCKED"}).Where("status='pending' AND next_attempt_at<=?", time.Now().UTC()).Order("created_at,id").Take(&row).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		found = true
		var e Event
		if err = json.Unmarshal([]byte(row.Payload), &e); err != nil {
			return err
		} // delivery is in this same transaction
		if e.Scene == "renewal_quote_paused" || e.Scene == "product_lifecycle" {
			var host struct {
				CustomerID       int64
				OwnershipVersion int64
			}
			err := tx.Table("instance.instances").Select("customer_id,ownership_version").Where("id=? AND deleted_at IS NULL", e.BusinessID).Take(&host).Error
			if errors.Is(err, gorm.ErrRecordNotFound) || (err == nil && (host.CustomerID != e.RecipientID || host.OwnershipVersion != e.OwnershipVersion)) {
				return tx.Model(&row).Update("status", "skipped").Error
			}
			if err != nil {
				return err
			}
		}
		if e.Scene == "expiry" {
			var host struct {
				CustomerID int64
				ExpiresAt  *time.Time
				Status     int
			}
			err := tx.Table("instance.instances").Select("customer_id,expires_at,status").Where("id=? AND deleted_at IS NULL", e.BusinessID).Take(&host).Error
			if errors.Is(err, gorm.ErrRecordNotFound) || (err == nil && (host.CustomerID != e.RecipientID || host.ExpiresAt == nil || !host.ExpiresAt.After(time.Now().UTC()) || e.Key != fmt.Sprintf("expiry:%s:%d:before_expiry", e.BusinessID, host.ExpiresAt.Unix()) || host.Status < 1 || host.Status > 4)) {
				return tx.Model(&row).Update("status", "skipped").Error
			}
			if err != nil {
				return err
			}
			days, enabled, err := expiryPolicy(tx)
			if err != nil {
				return err
			}
			if !enabled || host.ExpiresAt.After(time.Now().UTC().Add(time.Duration(days)*24*time.Hour)) {
				return tx.Model(&row).Update("status", "skipped").Error
			}
		}
		if err := s.deliverTx(tx, e); err != nil {
			return err
		}
		return tx.Model(&row).Update("status", "succeeded").Error
	})
	if err != nil && found {
		attempt := row.Attempts + 1
		status := "pending"
		if attempt >= 10 {
			status = "dead"
		}
		_ = s.transaction(ctx, func(tx *gorm.DB) error {
			return tx.Model(&Outbox{}).Where("id=? AND status='pending'", row.ID).Updates(map[string]any{"attempts": attempt, "status": status, "next_attempt_at": time.Now().UTC().Add(time.Duration(1<<min(attempt, 8)) * time.Second), "last_error_code": "delivery_failed"}).Error
		})
	}
	return found, err
}
func (s *Service) flush(ctx context.Context) error {
	var changes []Change
	if err := s.DB.WithContext(ctx).Where("push_status='pending'").Order("created_at").Limit(1000).Find(&changes).Error; err != nil {
		return err
	}
	if len(changes) == 0 {
		return nil
	}
	recipients := map[int64]bool{}
	pairs := make([][]any, 0, len(changes))
	for _, v := range changes {
		recipients[v.RecipientID] = true
		pairs = append(pairs, []any{v.RecipientID, v.Revision})
	}
	// Coalesce hints per recipient, then acknowledge this exact durable batch in one
	// commit. A transport error leaves the entire batch pending; repeats are safe.
	for recipient := range recipients {
		if err := s.Publisher.Publish(ctx, recipient); err != nil {
			return err
		}
	}
	if err := s.DB.WithContext(ctx).Model(&Change{}).Where("push_status='pending' AND (recipient_id,revision) IN ?", pairs).Update("push_status", "published").Error; err != nil {
		return err
	}
	if len(changes) == 1000 {
		s.Wake()
	}
	return nil
}

type Sync struct {
	Enabled               bool         `json:"enabled"`
	Revision              uint64       `json:"revision,string"`
	UnreadCount           int64        `json:"unreadCount"`
	Items                 []Message    `json:"items"`
	Changes               []ChangeView `json:"changes"`
	NextCursor            uint64       `json:"nextCursor,string"`
	HasMore               bool         `json:"hasMore"`
	ResetRequired         bool         `json:"resetRequired"`
	SnapshotToken         string       `json:"snapshotToken"`
	MarketingToastEnabled bool         `json:"marketingToastEnabled"`
}
type ChangeView struct {
	Revision  uint64 `json:"revision,string"`
	Kind      string `json:"kind"`
	MessageID *int64 `json:"messageId,string,omitempty"`
}

func (s *Service) Sync(ctx context.Context, id int64, cursor *uint64) (Sync, error) {
	out := Sync{Enabled: s.Enabled(ctx), Items: []Message{}, Changes: []ChangeView{}, MarketingToastEnabled: true}
	if !out.Enabled {
		return out, nil
	}
	err := s.transaction(ctx, func(tx *gorm.DB) error {
		var st State
		err := tx.Take(&st, "recipient_id=?", id).Error
		if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		out.Revision = st.Revision
		out.NextCursor = st.Revision
		if err = tx.Model(&Message{}).Where("recipient_id=? AND deleted_at IS NULL AND read_at IS NULL", id).Count(&out.UnreadCount).Error; err != nil {
			return err
		}
		var pref Preference
		if err := tx.Take(&pref, "recipient_id=?", id).Error; err == nil {
			out.MarketingToastEnabled = pref.MarketingToastEnabled
		} else if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		if cursor != nil {
			if *cursor > st.Revision {
				out.ResetRequired = true
			} else {
				var rows []Change
				if err = tx.Where("recipient_id=? AND revision>?", id, *cursor).Order("revision").Limit(51).Find(&rows).Error; err != nil {
					return err
				}
				if len(rows) > 0 && rows[0].Revision != *cursor+1 {
					out.ResetRequired = true
				}
				if len(rows) == 0 && *cursor < st.Revision {
					out.ResetRequired = true
				}
				if len(rows) > 50 {
					rows = rows[:50]
					out.HasMore = true
				}
				for _, v := range rows {
					out.Changes = append(out.Changes, ChangeView{v.Revision, v.Kind, v.MessageID})
					if v.Kind == "created" && v.MessageID != nil {
						var m Message
						if err := tx.Select(summaryColumns).Where("recipient_id=? AND id=? AND deleted_at IS NULL", id, *v.MessageID).Take(&m).Error; err == nil {
							out.Items = append(out.Items, m)
						} else if !errors.Is(err, gorm.ErrRecordNotFound) {
							return err
						}
					}
				}
				if len(rows) > 0 {
					out.NextCursor = rows[len(rows)-1].Revision
				}
			}
		}
		out.SnapshotToken = s.token(id, st.Revision)
		return nil
	})
	return out, err
}
func (s *Service) token(id int64, rev uint64, purpose ...string) string {
	scope := "inbox"
	if len(purpose) > 0 {
		scope = purpose[0]
	}
	v := fmt.Sprintf("%d:%d:%d", id, rev, time.Now().Add(15*time.Minute).Unix())
	h := hmac.New(sha256.New, s.key)
	h.Write([]byte("notification-v1:" + scope + ":" + v))
	return base64.RawURLEncoding.EncodeToString([]byte(v)) + "." + base64.RawURLEncoding.EncodeToString(h.Sum(nil))
}
func (s *Service) parseToken(id int64, token string, purpose ...string) (uint64, error) {
	scope := "inbox"
	if len(purpose) > 0 {
		scope = purpose[0]
	}
	parts := strings.Split(token, ".")
	if len(parts) != 2 {
		return 0, ErrInput
	}
	raw, e := base64.RawURLEncoding.DecodeString(parts[0])
	if e != nil {
		return 0, ErrInput
	}
	sig, e := base64.RawURLEncoding.DecodeString(parts[1])
	if e != nil {
		return 0, ErrInput
	}
	h := hmac.New(sha256.New, s.key)
	h.Write(append([]byte("notification-v1:"+scope+":"), raw...))
	if !hmac.Equal(sig, h.Sum(nil)) {
		return 0, ErrInput
	}
	v := strings.Split(string(raw), ":")
	if len(v) != 3 || v[0] != strconv.FormatInt(id, 10) {
		return 0, ErrInput
	}
	rev, e := strconv.ParseUint(v[1], 10, 64)
	exp, e2 := strconv.ParseInt(v[2], 10, 64)
	if e != nil || e2 != nil || exp < time.Now().Unix() {
		return 0, ErrInput
	}
	return rev, nil
}
func (s *Service) Read(ctx context.Context, id int64, ids []int64, token string) (int64, error) {
	var cutoff uint64
	var err error
	if token != "" {
		cutoff, err = s.parseToken(id, token)
		if err != nil {
			return 0, err
		}
	} else if len(ids) == 0 || len(ids) > 100 {
		return 0, ErrInput
	}
	changed := int64(0)
	err = s.transaction(ctx, func(tx *gorm.DB) error {
		st, e := lockState(tx, id)
		if e != nil {
			return e
		}
		q := tx.Model(&Message{}).Where("recipient_id=? AND deleted_at IS NULL", id)
		if token != "" {
			q = q.Where("created_revision<=?", cutoff)
		} else {
			unique := map[int64]bool{}
			for _, v := range ids {
				if v <= 0 {
					return ErrInput
				}
				unique[v] = true
			}
			var n int64
			if e = q.Where("id IN ?", ids).Count(&n).Error; e != nil {
				return e
			}
			if int(n) != len(unique) {
				return gorm.ErrRecordNotFound
			}
			q = tx.Model(&Message{}).Where("recipient_id=? AND deleted_at IS NULL AND id IN ?", id, ids)
		}
		r := q.Where("read_at IS NULL").Update("read_at", time.Now().UTC())
		if r.Error != nil {
			return r.Error
		}
		changed = r.RowsAffected
		if changed > 0 {
			return bump(tx, st, "read", nil)
		}
		return nil
	})
	if err == nil {
		s.Wake()
	}
	return changed, err
}
func (s *Service) Delete(ctx context.Context, id, msg int64) error {
	err := s.transaction(ctx, func(tx *gorm.DB) error {
		st, e := lockState(tx, id)
		if e != nil {
			return e
		}
		var m Message
		if e = tx.Where("recipient_id=? AND id=?", id, msg).Take(&m).Error; e != nil {
			return e
		}
		if m.DeletedAt != nil {
			return nil
		}
		if e = tx.Model(&m).Update("deleted_at", time.Now().UTC()).Error; e != nil {
			return e
		}
		return bump(tx, st, "deleted", &msg)
	})
	if err == nil {
		s.Wake()
	}
	return err
}

func (s *Service) cleanup(ctx context.Context) error {
	if time.Now().Before(s.nextCleanup) {
		return nil
	}
	r := s.DB.WithContext(ctx).Exec("DELETE FROM system.notification_changes WHERE push_status='published' AND created_at<? ORDER BY created_at LIMIT 1000", time.Now().UTC().Add(-7*24*time.Hour))
	if r.Error != nil {
		return r.Error
	}
	s.nextCleanup = time.Now().Add(24 * time.Hour)
	if r.RowsAffected == 1000 {
		s.nextCleanup = time.Now().Add(time.Second)
		time.AfterFunc(time.Second, s.Wake)
	}
	return nil
}
