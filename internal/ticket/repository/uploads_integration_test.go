package ticket

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	dom "ddyidc/internal/ticket/domain"
	"github.com/google/uuid"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func TestOwnedUploadsMySQL(t *testing.T) {
	socket := os.Getenv("FOUNDATION_MYSQL_SOCKET")
	if socket == "" {
		t.Skip("requires isolated MySQL script")
	}
	if !strings.HasPrefix(socket, "/tmp/ddyidc-foundation-") || !strings.HasSuffix(socket, "/mysql.sock") {
		t.Fatal("non-isolated socket refused")
	}
	db, err := gorm.Open(mysql.Open("root@unix("+socket+")/?parseTime=true&multiStatements=true"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	pool, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if err = db.Exec("CREATE DATABASE ticket").Error; err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile("../../../migrations/20260923_ticket_uploads.sql")
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err = db.Exec(string(raw)).Error; err != nil {
			t.Fatal(err)
		}
	}
	r := NewRepository(db)
	ctx := context.Background()
	u := Upload{ID: uuid.NewString(), OwnerType: dom.ActorClient, OwnerID: 10, Name: "original.txt", Size: 4, ContentType: "text/plain"}
	if err = r.ReserveUpload(ctx, &u); err != nil {
		t.Fatal(err)
	}
	if err = r.CompleteUpload(ctx, u.ID, strings.Repeat("a", 64), 4); err != nil {
		t.Fatal(err)
	}
	input, _ := json.Marshal([]dom.Attachment{{ID: u.ID, Name: "forged.html", Size: 999, ContentType: "text/html"}})
	if err = r.Transaction(ctx, func(tx *gorm.DB) error { _, e := r.bindUploads(tx, input, dom.ActorClient, 11, 1); return e }); err == nil {
		t.Fatal("foreign upload bound")
	}
	if err = r.Transaction(ctx, func(tx *gorm.DB) error { _, e := r.bindUploads(tx, input, dom.ActorAdmin, 10, 1); return e }); err == nil {
		t.Fatal("cross-role upload bound")
	}
	rollback := errors.New("later write failed")
	if err = r.Transaction(ctx, func(tx *gorm.DB) error {
		_, e := r.bindUploads(tx, input, dom.ActorClient, 10, 1)
		if e != nil {
			return e
		}
		return rollback
	}); !errors.Is(err, rollback) {
		t.Fatal(err)
	}
	var canonical json.RawMessage
	if err = r.Transaction(ctx, func(tx *gorm.DB) error {
		var e error
		canonical, e = r.bindUploads(tx, input, dom.ActorClient, 10, 1)
		return e
	}); err != nil {
		t.Fatal(err)
	}
	var items []dom.Attachment
	if err = json.Unmarshal(canonical, &items); err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].Name != u.Name || items[0].Size != 4 || items[0].ContentType != u.ContentType {
		t.Fatal("client metadata trusted")
	}
	if err = r.Transaction(ctx, func(tx *gorm.DB) error { _, e := r.bindUploads(tx, input, dom.ActorClient, 10, 2); return e }); err == nil {
		t.Fatal("upload rebound")
	}
	if ok, err := r.DiscardUpload(ctx, u.ID); err != nil || ok {
		t.Fatal("bound upload discarded")
	}
	for i := 0; i < 20; i++ {
		if err = r.ReserveUpload(ctx, &Upload{ID: uuid.NewString(), OwnerType: dom.ActorClient, OwnerID: 12, Name: "quota.txt", Size: 1, ContentType: "text/plain"}); err != nil {
			t.Fatal(err)
		}
	}
	if err = r.ReserveUpload(ctx, &Upload{ID: uuid.NewString(), OwnerType: dom.ActorClient, OwnerID: 12, Name: "quota.txt", Size: 1, ContentType: "text/plain"}); err == nil {
		t.Fatal("pending quota exceeded")
	}
	if err = db.Model(&Upload{}).Where("owner_id=12").Update("expires_at", time.Now().Add(-time.Hour)).Error; err != nil {
		t.Fatal(err)
	}
	expired, err := r.ExpiredUploads(ctx)
	if err != nil || len(expired) != 20 {
		t.Fatalf("expired count=%d err=%v", len(expired), err)
	}
	for _, v := range expired {
		if v.ID == u.ID {
			t.Fatal("bound upload selected for gc")
		}
		if err = r.FinishDelete(ctx, v.ID); err != nil {
			t.Fatal(err)
		}
	}
}
