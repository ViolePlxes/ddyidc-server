package admintodo

import (
	"context"
	"ddyidc/internal/shared/snowflake"
	"ddyidc/internal/shared/testenv"
	ticket "ddyidc/internal/ticket/domain"
	ticketrepo "ddyidc/internal/ticket/repository"
	ticketsvc "ddyidc/internal/ticket/service"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"
)

func TestTicketCloseReopenRestoresTodoWithoutReplayingLaterCloseMySQL(t *testing.T) {
	tx := testenv.MySQL(t).Begin()
	defer tx.Rollback()
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	client, id := snowflake.GenID(), snowflake.GenID()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(tx.Table("customer.customers").Create(map[string]any{"id": client, "tenant_id": 0, "username": fmt.Sprintf("ticket-%d", client), "password_hash": "isolated", "status": 1, "created_at": now, "updated_at": now}).Error)
	row := ticket.Ticket{ID: id, TicketNum: fmt.Sprintf("T%d", id), ClientID: client, Title: "isolated closed ticket", TicketTypeID: 1, Content: "isolated", StatusID: ticket.StatusClosed, AttachmentJSON: json.RawMessage(`[]`), CreatedAt: now, UpdatedAt: now, LastReplyTime: now, PostTime: now}
	must(tx.Create(&row).Error)
	repository := ticketrepo.NewRepository(tx)
	service := ticketsvc.NewService(repository)
	filter := Filter{Page: 1, PageSize: 20, CustomerID: fmt.Sprint(client)}
	page, err := List(ctx, tx, "tickets", filter)
	must(err)
	if page.Total != 0 {
		t.Fatal("closed ticket remained pending")
	}
	request := fmt.Sprintf("REOPEN%d", id)
	if err := service.Reopen(ctx, 1, id, "verification", request, "127.0.0.1", now.Add(-time.Second)); !errors.Is(err, ticketsvc.ErrRevision) {
		t.Fatalf("stale revision=%v", err)
	}
	must(service.Reopen(ctx, 1, id, "verification", request, "127.0.0.1", now))
	page, err = List(ctx, tx, "tickets", filter)
	must(err)
	if page.Total != 1 || page.Items[0].LastAdminID != "1" {
		t.Fatalf("reopen todo=%+v", page)
	}
	must(service.Reopen(ctx, 1, id, "verification", request, "127.0.0.1", now))
	if err := service.Reopen(ctx, 2, id, "verification", request, "127.0.0.1", now); !errors.Is(err, ticketsvc.ErrRevision) {
		t.Fatal("another admin reused request")
	}
	must(service.Close(ctx, ticket.ActorAdmin, 1, id))
	must(service.Reopen(ctx, 1, id, "verification", request, "127.0.0.1", now))
	page, err = List(ctx, tx, "tickets", filter)
	must(err)
	if page.Total != 0 {
		t.Fatal("replayed old request reopened later close")
	}
	log := ticket.Log{TicketID: id, ActorType: ticket.ActorAdmin, ActorID: 1, Action: "receive", CreatedAt: now}
	if err := repository.UpdateTicket(ctx, id, map[string]any{"status": ticket.StatusProcessing}, &log); !errors.Is(err, ticketrepo.ErrClosed) {
		t.Fatal("stale receive reopened closed ticket")
	}
	var count int64
	must(tx.Table("system.todo_audits").Where("kind='tickets' AND object_id=? AND action='reopen'", id).Count(&count).Error)
	if count != 1 {
		t.Fatalf("reopen audits=%d", count)
	}
}
