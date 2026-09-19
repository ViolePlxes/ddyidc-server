package instance

import (
	"context"
	"testing"
)

func TestActionActor(t *testing.T) {
	k, id := actionActor(context.Background(), 42)
	if k != 1 || id != 42 {
		t.Fatal("customer actor changed")
	}
	k, id = actionActor(WithAdminActor(context.Background(), 7), 42)
	if k != 2 || id != 7 {
		t.Fatal("admin must be audited as admin")
	}
}
