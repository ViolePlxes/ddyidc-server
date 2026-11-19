// Package executionlog is the finance-side execution ledger: one row per paid
// business action (delivery, renewal) for finance-type suppliers.
//
// It exists to answer one question without guessing: did we already execute this
// paid action, and if not, may it be executed again? The unique key
// (module, idempotency_key) is the final idempotency guard; callers may check the
// row first, but a duplicate writer can never insert a second row.
//
// Only finance suppliers use this ledger. West and other adapters keep their own
// contracts and never write here.
package executionlog

import (
	"errors"
	"time"

	"gorm.io/gorm"
)

const (
	ModuleDelivery = "delivery"
	ModuleRenewal  = "renewal"
)

const (
	StatusPending         = "pending"
	StatusQueued          = "queued"
	StatusProcessing      = "processing"
	StatusCompleted       = "completed"
	StatusRetryPending    = "retry_pending"
	StatusFailed          = "failed"
	StatusDispatchUnknown = "dispatch_unknown"
	StatusManualReconcile = "manual_reconcile"
)

// MaxAttempts mirrors the finance system's MAX_ATTEMPTS: three attempts, then a
// human decides. Unknown outcomes never count towards this budget because they
// are not retried automatically at all.
const MaxAttempts = 3

// RetryDelays is the backoff schedule between attempts.
var RetryDelays = []time.Duration{30 * time.Second, 2 * time.Minute, 10 * time.Minute}

type Event struct {
	ID             int64     `gorm:"column:id;primaryKey"`
	Module         string    `gorm:"column:module"`
	IdempotencyKey string    `gorm:"column:idempotency_key"`
	TraceID        string    `gorm:"column:trace_id"`
	EntityType     string    `gorm:"column:entity_type"`
	EntityUID      string    `gorm:"column:entity_uid"`
	Operation      string    `gorm:"column:operation"`
	Status         string    `gorm:"column:status"`
	Attempts       int       `gorm:"column:attempts"`
	ExpectedStatus string    `gorm:"column:expected_status"`
	ErrorCode      string    `gorm:"column:error_code"`
	ErrorMessage   string    `gorm:"column:error_message"`
	Payload        string    `gorm:"column:payload"`
	CreatedAt      time.Time `gorm:"column:created_at"`
	UpdatedAt      time.Time `gorm:"column:updated_at"`
}

func (Event) TableName() string { return "order.execution_events" }

type BeginInput struct {
	Module         string
	IdempotencyKey string
	TraceID        string
	EntityType     string
	EntityUID      string
	Operation      string
	Payload        string
}

// Begin returns the existing row for this idempotency key, or creates it as
// pending. created reports whether this call inserted the row.
func Begin(db *gorm.DB, in BeginInput) (Event, bool, error) {
	if in.Module == "" || in.IdempotencyKey == "" {
		return Event{}, false, errors.New("execution log requires module and idempotency key")
	}
	var existing Event
	err := db.Where("module = ? AND idempotency_key = ?", in.Module, in.IdempotencyKey).Take(&existing).Error
	if err == nil {
		return existing, false, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return Event{}, false, err
	}
	now := time.Now()
	event := Event{
		Module: in.Module, IdempotencyKey: in.IdempotencyKey, TraceID: in.TraceID,
		EntityType: in.EntityType, EntityUID: in.EntityUID, Operation: in.Operation,
		Status: StatusPending, Attempts: 0, Payload: in.Payload, CreatedAt: now, UpdatedAt: now,
	}
	if err := db.Create(&event).Error; err != nil {
		// A concurrent writer won the unique key: return its row, never a second.
		if again := db.Where("module = ? AND idempotency_key = ?", in.Module, in.IdempotencyKey).Take(&existing); again.Error == nil {
			return existing, false, nil
		}
		return Event{}, false, err
	}
	return event, true, nil
}

// Transition moves the row with an optimistic check on the expected status.
// A mismatch is reported as a conflict so the caller can mark manual reconcile
// instead of silently overwriting another writer's outcome.
func Transition(db *gorm.DB, id int64, expected, next, code, message string) error {
	updates := map[string]any{
		"status": next, "expected_status": expected,
		"error_code": code, "error_message": message, "updated_at": time.Now(),
	}
	query := db.Model(&Event{}).Where("id = ?", id)
	if expected != "" {
		query = query.Where("status = ?", expected)
	}
	result := query.Updates(updates)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return gorm.ErrRecordNotFound
	}
	return nil
}

// Record settles a business action in one call: it begins the row when needed and
// moves it to the final status. Used by the delivery and renewal hooks.
func Record(db *gorm.DB, in BeginInput, status, code, message string) (Event, error) {
	event, _, err := Begin(db, in)
	if err != nil {
		return Event{}, err
	}
	if event.Status == status && event.ErrorCode == code {
		return event, nil
	}
	if err := db.Model(&Event{}).Where("id = ?", event.ID).Updates(map[string]any{
		"status": status, "error_code": code, "error_message": message,
		"attempts": gorm.Expr("attempts + 1"), "updated_at": time.Now(),
	}).Error; err != nil {
		return event, err
	}
	event.Status, event.ErrorCode, event.ErrorMessage = status, code, message
	return event, nil
}

// Retryable reports whether the platform may execute this action again without a
// human. Only definite pre-execution failures qualify; anything that might have
// reached the supplier stays with a human.
func Retryable(event Event) bool {
	switch event.Status {
	case StatusRetryPending, StatusFailed:
		return event.Attempts < MaxAttempts
	default:
		return false
	}
}

// NextDelay returns the backoff before the next attempt.
func NextDelay(attempts int) time.Duration {
	if attempts < 1 {
		attempts = 1
	}
	if attempts > len(RetryDelays) {
		return RetryDelays[len(RetryDelays)-1]
	}
	return RetryDelays[attempts-1]
}

// List returns a page of events, newest first.
func List(db *gorm.DB, module, status string, page, size int) ([]Event, int64, error) {
	if page < 1 {
		page = 1
	}
	if size < 1 || size > 100 {
		size = 20
	}
	base := db.Model(&Event{})
	if module != "" {
		base = base.Where("module = ?", module)
	}
	if status != "" {
		base = base.Where("status = ?", status)
	}
	var total int64
	if err := base.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var rows []Event
	err := base.Order("updated_at DESC, id DESC").Offset((page - 1) * size).Limit(size).Find(&rows).Error
	return rows, total, err
}

// Find loads one event by module and idempotency key.
func Find(db *gorm.DB, module, key string) (Event, error) {
	var event Event
	err := db.Where("module = ? AND idempotency_key = ?", module, key).Take(&event).Error
	return event, err
}
