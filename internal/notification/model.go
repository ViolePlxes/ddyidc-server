// Package notification owns the durable inbox. Public IDs are opaque, never cursors.
package notification

import (
	"context"
	"ddyidc/internal/shared/snowflake"
	"encoding/json"
	"errors"
	"fmt"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

var ErrInput = errors.New("消息参数无效")
var ErrConflict = errors.New("消息幂等键与原请求不一致")
var ErrDisabled = errors.New("消息通知未启用")

type Event struct {
	Key          string    `json:"key"`
	RecipientID  int64     `json:"recipientId,string"`
	Type         string    `json:"type"`
	Scene        string    `json:"scene"`
	Title        string    `json:"title"`
	Summary      string    `json:"summary"`
	Content      string    `json:"content"`
	BusinessType string    `json:"businessType"`
	BusinessID   string    `json:"businessId"`
	OccurredAt   time.Time `json:"occurredAt"`
}

func (e Event) Validate() error {
	if e.RecipientID <= 0 || len(e.Key) > 191 || strings.TrimSpace(e.Key) == "" || len(e.Scene) > 64 || strings.TrimSpace(e.Scene) == "" || strings.TrimSpace(e.Title) == "" || utf8.RuneCountInString(e.Title) > 128 || utf8.RuneCountInString(e.Summary) > 255 || len(e.Content) > 32768 {
		return ErrInput
	}
	switch e.Type {
	case "ticket", "expiry", "instance", "security", "billing", "marketing":
	default:
		return ErrInput
	}
	switch e.BusinessType {
	case "", "ticket", "instance", "order", "bill":
	default:
		return ErrInput
	}
	if len(e.BusinessID) > 64 {
		return ErrInput
	}
	return nil
}

type Message struct {
	Seq              uint64     `gorm:"column:inbox_seq;primaryKey;autoIncrement" json:"-"`
	ID               int64      `json:"id,string"`
	RecipientID      int64      `json:"-"`
	Type             string     `json:"type"`
	Scene            string     `json:"scene"`
	Title            string     `json:"title"`
	Summary          string     `json:"summary"`
	Content          string     `json:"content"`
	BusinessType     string     `json:"businessType"`
	BusinessID       string     `json:"businessId"`
	SourceEventKey   string     `json:"-"`
	SourceOccurredAt time.Time  `json:"sourceOccurredAt"`
	CreatedRevision  uint64     `json:"createdRevision,string"`
	ReadAt           *time.Time `json:"readAt"`
	DeletedAt        *time.Time `json:"-"`
	CreatedAt        time.Time  `json:"createdAt"`
}

func (Message) TableName() string { return "system.notification_messages" }

type State struct {
	RecipientID int64 `gorm:"primaryKey"`
	Revision    uint64
	UpdatedAt   time.Time
}

func (State) TableName() string { return "system.notification_user_states" }

type Change struct {
	RecipientID int64  `gorm:"primaryKey"`
	Revision    uint64 `gorm:"primaryKey"`
	Kind        string
	MessageID   *int64
	PushStatus  string
	CreatedAt   time.Time
}

func (Change) TableName() string { return "system.notification_changes" }

type Outbox struct {
	ID            int64 `gorm:"primaryKey"`
	EventKey      string
	Payload       string
	Status        string
	Attempts      int
	NextAttemptAt time.Time
	LastErrorCode string
	CreatedAt     time.Time
}

func (Outbox) TableName() string { return "system.notification_outbox" }

type Preference struct {
	RecipientID           int64     `gorm:"primaryKey" json:"-"`
	MarketingToastEnabled bool      `json:"marketingToastEnabled"`
	UpdatedAt             time.Time `json:"-"`
}

func (Preference) TableName() string { return "system.notification_preferences" }

// Wake must be invoked after commit. It is a hint only; the outbox is authoritative.
var wakeMu sync.RWMutex
var wakeFn func()
var disconnectFn func()

func Wake() {
	wakeMu.RLock()
	f := wakeFn
	wakeMu.RUnlock()
	if f != nil {
		f()
	}
}
func setWake(f func()) { wakeMu.Lock(); wakeFn = f; wakeMu.Unlock() }

// Enqueue participates in the caller's business transaction and never sends mail.
func Enqueue(tx *gorm.DB, e Event) error {
	if err := e.Validate(); err != nil {
		return err
	}
	var enabled string
	err := tx.Table("system.system_configs").Where("`key`=?", "notification.inapp.enabled").Select("value").Scan(&enabled).Error
	if err != nil {
		return err
	}
	if enabled != "true" && enabled != "1" {
		return nil
	}
	if e.OccurredAt.IsZero() {
		e.OccurredAt = time.Now().UTC()
	}
	// Time is metadata, not part of the idempotent payload signature.
	raw, err := json.Marshal(e)
	if err != nil {
		return err
	}
	row := Outbox{ID: snowflake.GenID(), EventKey: fmt.Sprintf("%d:%s", e.RecipientID, e.Key), Payload: string(raw), Status: "pending", NextAttemptAt: time.Now().UTC(), CreatedAt: time.Now().UTC()}
	if len(row.EventKey) > 191 {
		return ErrInput
	}
	r := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&row)
	if r.Error != nil {
		return r.Error
	}
	if r.RowsAffected == 0 {
		var old Outbox
		if err := tx.Where("event_key=?", row.EventKey).Take(&old).Error; err != nil {
			return err
		}
		var prev Event
		if json.Unmarshal([]byte(old.Payload), &prev) != nil {
			return ErrConflict
		}
		prev.OccurredAt = e.OccurredAt
		a, _ := json.Marshal(prev)
		if string(a) != string(raw) {
			return ErrConflict
		}
		if old.Status == "skipped" && e.Scene == "expiry" {
			if err := tx.Model(&old).Updates(map[string]any{"status": "pending", "next_attempt_at": time.Now().UTC(), "attempts": 0, "last_error_code": ""}).Error; err != nil {
				return err
			}
		}
	}
	markQueued(tx)
	return nil
}

// Publisher is intentionally transport independent; a shared-bus adapter can
// replace Hub later without changing persisted events or the SSE API.
type Publisher interface {
	Publish(context.Context, int64) error
}
type Hub struct {
	mu    sync.Mutex
	users map[int64]map[chan struct{}]bool
}

func NewHub() *Hub { return &Hub{users: make(map[int64]map[chan struct{}]bool)} }
func (h *Hub) Subscribe(id int64) (<-chan struct{}, func(), error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.users[id]) >= 8 {
		return nil, nil, errors.New("连接数量过多")
	}
	if h.users[id] == nil {
		h.users[id] = map[chan struct{}]bool{}
	}
	ch := make(chan struct{}, 1)
	h.users[id][ch] = true
	return ch, func() {
		h.mu.Lock()
		delete(h.users[id], ch)
		if len(h.users[id]) == 0 {
			delete(h.users, id)
		}
		h.mu.Unlock()
	}, nil
}
func (h *Hub) Publish(_ context.Context, id int64) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	for ch := range h.users[id] {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
	return nil
}

func DisconnectRealtime() {
	wakeMu.RLock()
	f := disconnectFn
	wakeMu.RUnlock()
	if f != nil {
		f()
	}
}
func (h *Hub) DisconnectAll() {
	h.mu.Lock()
	for _, set := range h.users {
		for ch := range set {
			close(ch)
		}
	}
	h.users = make(map[int64]map[chan struct{}]bool)
	h.mu.Unlock()
}

const summaryColumns = "inbox_seq,id,recipient_id,type,scene,title,summary,business_type,business_id,source_occurred_at,created_revision,read_at,created_at"
