package service

import (
	"ddyidc/internal/finance/executionlog"
	"fmt"
	"time"
)

// ListFinanceExecutions returns the finance ledger page. Admin-only diagnostics:
// it shows what was executed, what is retrying and what needs a human.
func (s *Store) ListFinanceExecutions(module, status string, page, size int) ([]executionlog.Event, int64, error) {
	if s.db == nil {
		return nil, 0, fmt.Errorf("finance ledger unavailable")
	}
	return executionlog.List(s.db, module, status, page, size)
}

// CompleteFinanceExecution records a human decision that the business action did
// happen, with the supplier reference that proves it. Only unknown or failed rows
// may be completed; anything still retrying must finish first.
func (s *Store) CompleteFinanceExecution(id int64, upstreamRef, note string) error {
	if s.db == nil {
		return fmt.Errorf("finance ledger unavailable")
	}
	if upstreamRef == "" {
		return fmt.Errorf("请填写上游单号或主机号")
	}
	var event executionlog.Event
	if err := s.db.Take(&event, id).Error; err != nil {
		return err
	}
	switch event.Status {
	case executionlog.StatusDispatchUnknown, executionlog.StatusFailed, executionlog.StatusManualReconcile:
	default:
		return fmt.Errorf("当前状态 %s 不允许人工完成", event.Status)
	}
	message := fmt.Sprintf("人工确认完成：%s %s", upstreamRef, note)
	return executionlog.Transition(s.db, id, event.Status, executionlog.StatusCompleted, event.ErrorCode, message)
}

// ReconcileFinanceExecution parks a row for manual follow-up without claiming any
// outcome. Used when the supplier cannot be reached or the answer is ambiguous.
func (s *Store) ReconcileFinanceExecution(id int64, note string) error {
	if s.db == nil {
		return fmt.Errorf("finance ledger unavailable")
	}
	var event executionlog.Event
	if err := s.db.Take(&event, id).Error; err != nil {
		return err
	}
	if event.Status == executionlog.StatusCompleted {
		return fmt.Errorf("已完成的记录不能转人工核对")
	}
	return executionlog.Transition(s.db, id, "", executionlog.StatusManualReconcile, event.ErrorCode, note)
}

// RetryableFinanceExecutions lists rows the platform may execute again without a
// human: only definite pre-execution failures inside the attempt budget.
func (s *Store) RetryableFinanceExecutions() ([]executionlog.Event, error) {
	if s.db == nil {
		return nil, fmt.Errorf("finance ledger unavailable")
	}
	var rows []executionlog.Event
	if err := s.db.Where("status IN ?", []string{executionlog.StatusRetryPending, executionlog.StatusFailed}).Order("updated_at").Limit(50).Find(&rows).Error; err != nil {
		return nil, err
	}
	out := rows[:0]
	for _, row := range rows {
		if executionlog.Retryable(row) {
			out = append(out, row)
		}
	}
	return out, nil
}

// ExecutionBackoff exposes the retry delay schedule for the recovery worker.
func ExecutionBackoff(attempts int) time.Duration { return executionlog.NextDelay(attempts) }
