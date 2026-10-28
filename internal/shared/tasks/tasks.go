// Package tasks stores side-effect intentions in the business transaction.
// Lost leases with potentially external effects require reconciliation, never
// blind replay. A task remains visible even if the process dies after commit.
package tasks

import (
	"context"
	"crypto/rand"
	"ddyidc/internal/shared/snowflake"
	"ddyidc/internal/shared/workgate"
	"ddyidc/pkg/procurementbudget"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"log"
	"strconv"
	"strings"
	"time"
)

type Task struct {
	ID         int64
	TaskKey    string
	Kind       string
	Payload    string
	Status     string
	Attempt    int
	LeaseToken string
	LeaseUntil *time.Time
}

func (Task) TableName() string { return "system.durable_tasks" }

type View struct {
	ID          string `json:"id"`
	TaskKey     string `json:"taskKey"`
	BusinessID  string `json:"businessId"`
	Status      string `json:"status"`
	Attempt     int    `json:"attempt"`
	SafeError   string `json:"safeError"`
	AvailableAt string `json:"availableAt"`
	ReviewNote  string `json:"reviewNote"`
}

func List(ctx context.Context, database *gorm.DB, page int) ([]View, error) {
	rows := make([]View, 0)
	err := database.WithContext(ctx).Table("system.durable_tasks t").Select("CAST(t.id AS CHAR) AS id,t.task_key,COALESCE(JSON_UNQUOTE(JSON_EXTRACT(t.payload,'$.orderId')),JSON_UNQUOTE(JSON_EXTRACT(t.payload,'$.businessId'))) AS business_id,t.status,t.attempt,t.safe_error,CASE WHEN t.status='pending' THEN DATE_FORMAT(t.available_at,'%Y-%m-%d %H:%i:%s') ELSE '' END AS available_at,COALESCE((SELECT r.reason FROM system.task_reconciliations r WHERE r.task_id=t.id ORDER BY r.created_at DESC,r.id DESC LIMIT 1),'') AS review_note").Order("t.created_at DESC,t.id DESC").Offset((page - 1) * 20).Limit(20).Scan(&rows).Error
	return rows, err
}

type PaymentPayload struct {
	CustomerID   int64  `json:"customerId"`
	BusinessID   int64  `json:"businessId"`
	OrderID      int64  `json:"orderId,omitempty"`
	BusinessType string `json:"businessType"`
	Channel      string `json:"channel"`
	AmountFen    int64  `json:"amountFen"`
}

func EnqueuePayment(tx *gorm.DB, key string, payload PaymentPayload) error {
	if tx == nil || key == "" || payload.CustomerID <= 0 || payload.BusinessID <= 0 {
		return errors.New("invalid durable task")
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	row := Task{ID: snowflake.GenID(), TaskKey: key, Kind: "post_payment", Payload: string(body), Status: "pending"}
	// The task key is server-owned. Conflicting payloads must not be ignored.
	var old Task
	err = tx.Where("task_key=?", key).Take(&old).Error
	if err == nil {
		var previous PaymentPayload
		if old.Kind != row.Kind || json.Unmarshal([]byte(old.Payload), &previous) != nil || previous != payload {
			return errors.New("task identity conflict")
		}
		return nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return err
	}
	return tx.Create(&row).Error
}

func Claim(ctx context.Context, database *gorm.DB) (*Task, error) {
	var task Task
	err := database.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var guard struct{ ID int }
		if err := tx.Table("system.task_claim_guard").Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=1").Take(&guard).Error; err != nil {
			return err
		}
		if err := tx.Exec("UPDATE system.durable_tasks SET status='unknown',safe_error='执行租约过期，外部结果待核实',updated_at=NOW(3) WHERE status='running' AND lease_until<=NOW(3)").Error; err != nil {
			return err
		}
		// Finance suppliers may share one mutable remote cart. Serialize this bounded
		// worker pool across nodes until supplier-specific independent carts exist.
		var running int64
		if err := tx.Model(&Task{}).Where("status='running'").Count(&running).Error; err != nil {
			return err
		}
		if running != 0 {
			return nil
		}
		err := tx.Clauses(clause.Locking{Strength: "UPDATE", Options: "SKIP LOCKED"}).Where("status='pending' AND available_at<=NOW(3)").Order("available_at,id").Take(&task).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		token := make([]byte, 16)
		if _, err := rand.Read(token); err != nil {
			return err
		}
		task.LeaseToken = hex.EncodeToString(token)
		result := tx.Model(&Task{}).Where("id=? AND status='pending'", task.ID).Updates(map[string]any{"status": "running", "lease_token": task.LeaseToken, "lease_until": gorm.Expr("DATE_ADD(NOW(3), INTERVAL ? SECOND)", int(taskLease(&task)/time.Second)), "attempt": gorm.Expr("attempt+1"), "updated_at": gorm.Expr("NOW(3)")})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return errors.New("task claim lost")
		}
		task.Attempt++
		return nil
	})
	if err == nil && task.ID == 0 {
		return nil, nil
	}
	return &task, err
}

func Finish(ctx context.Context, database *gorm.DB, task *Task, runErr error) error {
	status, message := "succeeded", ""
	if runErr != nil {
		status, message = "unknown", "执行结果未确认，请核对业务与供应商状态"
	}
	changes := map[string]any{"status": status, "safe_error": message, "updated_at": gorm.Expr("NOW(3)")}
	var safe interface{ SafeToRetry() bool }
	if runErr != nil && strings.HasPrefix(task.TaskKey, "delivery-unit:") && errors.As(runErr, &safe) && safe.SafeToRetry() {
		changes["status"], changes["safe_error"] = "failed", "采购前报价连续失败，尚未提交开通，请检查连接后处理"
		if task.Attempt < 4 {
			delay := []int{30, 120, 300}[max(0, task.Attempt-1)]
			changes["status"], changes["safe_error"] = "pending", "采购前报价暂时不可用，尚未提交开通，等待重试"
			changes["available_at"] = gorm.Expr("DATE_ADD(NOW(3), INTERVAL ? SECOND)", delay)
		}
	}
	result := database.WithContext(ctx).Model(&Task{}).Where("id=? AND status='running' AND lease_token=? AND lease_until>NOW(3)", task.ID, task.LeaseToken).Updates(changes)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return fmt.Errorf("task lease lost: %d", task.ID)
	}
	return nil
}

func Run(ctx context.Context, database *gorm.DB, handle func(context.Context, *Task) error) {
	var lastClaimError time.Time
	timer := time.NewTicker(2 * time.Second)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			_ = workgate.Run(ctx, func(ctx context.Context) error {
				claimCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
				task, err := Claim(claimCtx, database)
				cancel()
				if err != nil {
					if time.Since(lastClaimError) >= time.Minute {
						log.Print(`{"event":"durable_task_claim_failed","result":"database_unavailable"}`)
						lastClaimError = time.Now()
					}
					return nil
				}
				if task == nil {
					return nil
				}
				runCtx, stop := context.WithTimeout(ctx, taskExecution(task))
				started := time.Now()
				logTask(task, "started", 0)
				runErr := func() (err error) {
					defer func() {
						if recover() != nil {
							err = errors.New("task panic")
						}
					}()
					return handle(runCtx, task)
				}()
				stop()
				finishCtx, done := context.WithTimeout(context.Background(), 5*time.Second)
				result := "succeeded"
				if runErr != nil {
					result = "unknown"
				}
				if finishErr := Finish(finishCtx, database, task, runErr); finishErr != nil {
					result = "completion_not_persisted"
				}
				logTask(task, result, time.Since(started).Milliseconds())
				done()
				return nil
			})
		}
	}
}

func logTask(task *Task, result string, duration int64) {
	fields := map[string]any{"event": "durable_task", "task_id": strconv.FormatInt(task.ID, 10), "kind": task.Kind, "attempt": task.Attempt, "result": result, "duration_ms": duration}
	var payload struct {
		OrderID    int64 `json:"orderId"`
		BusinessID int64 `json:"businessId"`
	}
	if json.Unmarshal([]byte(task.Payload), &payload) == nil {
		if payload.OrderID > 0 {
			fields["order_id"] = strconv.FormatInt(payload.OrderID, 10)
		}
		if payload.BusinessID > 0 {
			fields["business_id"] = strconv.FormatInt(payload.BusinessID, 10)
		}
	}
	raw, _ := json.Marshal(fields)
	log.Print(string(raw))
}

// Only paid delivery units receive the longer procurement window. The lease
// always outlives execution and finalization; expired work is never replayed.
func taskExecution(task *Task) time.Duration {
	if task.Kind == "post_payment" && strings.HasPrefix(task.TaskKey, "delivery-unit:") {
		return procurementbudget.Execution
	}
	return 2 * time.Minute
}
func taskLease(task *Task) time.Duration {
	if taskExecution(task) == procurementbudget.Execution {
		return procurementbudget.Lease
	}
	return 3 * time.Minute
}
