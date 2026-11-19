package executionlog

import (
	"testing"
	"time"
)

func TestRetryableOnlyForDefinitePreExecutionFailures(t *testing.T) {
	cases := []struct {
		event Event
		want  bool
	}{
		{Event{Status: StatusRetryPending, Attempts: 0}, true},
		{Event{Status: StatusFailed, Attempts: 1}, true},
		{Event{Status: StatusRetryPending, Attempts: MaxAttempts}, false},
		{Event{Status: StatusFailed, Attempts: MaxAttempts + 1}, false},
		// Unknown outcomes are never retried automatically, whatever the budget.
		{Event{Status: StatusDispatchUnknown, Attempts: 0}, false},
		{Event{Status: StatusManualReconcile, Attempts: 0}, false},
		{Event{Status: StatusCompleted, Attempts: 0}, false},
	}
	for _, c := range cases {
		if got := Retryable(c.event); got != c.want {
			t.Fatalf("Retryable(%s, attempts=%d)=%v want %v", c.event.Status, c.event.Attempts, got, c.want)
		}
	}
}

func TestNextDelayFollowsScheduleAndCaps(t *testing.T) {
	want := []time.Duration{30 * time.Second, 2 * time.Minute, 10 * time.Minute}
	for i, expected := range want {
		if got := NextDelay(i + 1); got != expected {
			t.Fatalf("NextDelay(%d)=%v want %v", i+1, got, expected)
		}
	}
	if got := NextDelay(0); got != want[0] {
		t.Fatalf("NextDelay(0)=%v want %v", got, want[0])
	}
	if got := NextDelay(99); got != want[len(want)-1] {
		t.Fatalf("NextDelay(99)=%v want cap %v", got, want[len(want)-1])
	}
}

func TestBeginRejectsIncompleteIdentity(t *testing.T) {
	if _, _, err := Begin(nil, BeginInput{Module: ModuleDelivery}); err == nil {
		t.Fatal("missing idempotency key accepted")
	}
	if _, _, err := Begin(nil, BeginInput{IdempotencyKey: "delivery:1"}); err == nil {
		t.Fatal("missing module accepted")
	}
}
