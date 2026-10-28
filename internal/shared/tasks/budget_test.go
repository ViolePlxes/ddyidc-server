package tasks

import (
	"ddyidc/pkg/procurementbudget"
	"testing"
	"time"
)

func TestProcurementBudgetDoesNotWidenOtherTasks(t *testing.T) {
	for _, task := range []*Task{{Kind: "post_payment", TaskKey: "payment:1"}, {Kind: "upstream_refresh", TaskKey: "delivery-unit:1"}} {
		if taskExecution(task) != 2*time.Minute || taskLease(task) != 3*time.Minute {
			t.Fatal("unrelated budget widened")
		}
	}
	task := &Task{Kind: "post_payment", TaskKey: "delivery-unit:1"}
	if taskExecution(task) != procurementbudget.Execution || taskLease(task) < taskExecution(task)+time.Minute || procurementbudget.Request >= taskExecution(task) {
		t.Fatal("unsafe procurement budget")
	}
}
