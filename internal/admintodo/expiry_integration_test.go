package admintodo

import (
	"context"
	"ddyidc/internal/notify"
	"ddyidc/internal/shared/snowflake"
	"ddyidc/internal/shared/testenv"
	"fmt"
	"testing"
)

func TestExpiryAdmissionMySQL(t *testing.T) {
	tx := testenv.MySQL(t).Begin()
	defer tx.Rollback()
	ctx := context.Background()
	repository := notify.NewRepository(tx)
	id := snowflake.GenID()
	business := fmt.Sprintf("%d:1791130000", id)
	claimed, err := repository.ClaimExpiry(ctx, business, id, 1, "127.0.0.1", "rollback-only reminder")
	if err != nil || !claimed {
		t.Fatal(claimed, err)
	}
	claimed, err = repository.ClaimExpiry(ctx, business, id, 2, "127.0.0.1", "concurrent actor")
	if err != nil || claimed {
		t.Fatal("same expiry admitted twice", claimed, err)
	}
	if err := repository.FinishExpiry(ctx, business, id, 1, "127.0.0.1", "reminder_unknown"); err != nil {
		t.Fatal(err)
	}
	claimed, err = repository.ClaimExpiry(ctx, business, id, 1, "127.0.0.1", "manual retry")
	if err != nil || claimed {
		t.Fatal("unknown outcome admitted again", claimed, err)
	}
	claimed, err = repository.ClaimExpiry(ctx, fmt.Sprintf("%d:1791216400", id), id, 1, "127.0.0.1", "renewed expiry")
	if err != nil || !claimed {
		t.Fatal("renewed expiry blocked", claimed, err)
	}
	t.Log("isolated database: pre-send admission once, unknown remains blocked, renewed expiry admitted separately; no provider called; rollback")
}
