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

	t.Run("rich reply persistence and failed image rollback", func(t *testing.T) {
		statements := []string{
			"CREATE TABLE ticket.ticket (id BIGINT PRIMARY KEY,status BIGINT,last_reply_time DATETIME,post_time DATETIME,update_time DATETIME)",
			"CREATE TABLE ticket.ticket_reply (id BIGINT PRIMARY KEY,ticket_id BIGINT,type VARCHAR(16),rel_id BIGINT,content TEXT,attachment JSON,quote_reply_id BIGINT NULL,create_time DATETIME)",
			"CREATE TABLE ticket.ticket_logs (id BIGINT PRIMARY KEY,ticket_id BIGINT,actor_type VARCHAR(16),actor_id BIGINT,action VARCHAR(32),detail VARCHAR(255),create_time DATETIME)",
			"INSERT INTO ticket.ticket(id,status) VALUES (200,1)",
		}
		for _, q := range statements {
			if err := db.Exec(q).Error; err != nil {
				t.Fatal(err)
			}
		}
		migration, err := os.ReadFile("../../shared/migrations/sql/048_ticket_rich_content.sql")
		if err != nil {
			t.Fatal(err)
		}
		sequenceMigration, err := os.ReadFile("../../shared/migrations/sql/049_ticket_reply_order.sql")
		if err != nil {
			t.Fatal(err)
		}
		for i := 0; i < 2; i++ {
			if err := db.Exec(string(sequenceMigration)).Error; err != nil {
				t.Fatal(err)
			}
		}
		for i := 0; i < 2; i++ {
			if err = db.Exec(string(migration)).Error; err != nil {
				t.Fatal(err)
			}
		}
		image := Upload{ID: uuid.NewString(), OwnerType: dom.ActorClient, OwnerID: 10, Name: "screenshot.png", Size: 8, ContentType: "image/png"}
		if err = r.ReserveUpload(ctx, &image); err != nil {
			t.Fatal(err)
		}
		if err = r.CompleteUpload(ctx, image.ID, strings.Repeat("b", 64), 8); err != nil {
			t.Fatal(err)
		}
		imageJSON, _ := json.Marshal([]dom.Attachment{{ID: image.ID}})
		doc := json.RawMessage(`{"version":1,"doc":{"type":"doc","content":[{"type":"image","attrs":{"attachmentId":"` + image.ID + `"}}]}}`)
		row := dom.Reply{ID: 201, TicketID: 200, Type: dom.ActorClient, RelID: 10, Content: "[图片]", AttachmentJSON: imageJSON, RichContent: doc, CreatedAt: time.Now()}
		if err = r.CreateReply(ctx, &row, map[string]any{"status": 2}, &dom.Log{ID: 202, TicketID: 200, ActorType: dom.ActorClient, ActorID: 10, Action: "reply", CreatedAt: time.Now()}, nil); err != nil {
			t.Fatal(err)
		}
		saved, err := r.FindReply(ctx, 200, 201)
		if err != nil || !json.Valid(saved.RichContent) || !strings.Contains(string(saved.RichContent), image.ID) {
			t.Fatalf("rich reply not persisted: %v", err)
		}
		fake := Upload{ID: uuid.NewString(), OwnerType: dom.ActorClient, OwnerID: 10, Name: "log.txt", Size: 4, ContentType: "text/plain"}
		if err = r.ReserveUpload(ctx, &fake); err != nil {
			t.Fatal(err)
		}
		if err = r.CompleteUpload(ctx, fake.ID, strings.Repeat("c", 64), 4); err != nil {
			t.Fatal(err)
		}
		row.ID = 203
		row.RichContent = json.RawMessage(strings.ReplaceAll(string(doc), image.ID, fake.ID))
		row.AttachmentJSON, _ = json.Marshal([]dom.Attachment{{ID: fake.ID, ContentType: "image/png"}})
		if err = r.CreateReply(ctx, &row, map[string]any{"status": 3}, &dom.Log{ID: 204, TicketID: 200, Action: "reply", CreatedAt: time.Now()}, nil); err == nil {
			t.Fatal("forged image MIME accepted")
		}
		var upload Upload
		if err = db.First(&upload, "id=?", fake.ID).Error; err != nil || upload.State != "ready" {
			t.Fatalf("upload binding not rolled back: %v", err)
		}
		var count int64
		db.Table("ticket.ticket_reply").Where("id=203").Count(&count)
		if count != 0 {
			t.Fatal("failed reply persisted")
		}
		var state int
		db.Raw("SELECT status FROM ticket.ticket WHERE id=200").Scan(&state)
		if state != 2 {
			t.Fatal("ticket status changed on failed reply")
		}
		legacy := dom.Reply{ID: 205, TicketID: 200, Type: dom.ActorClient, RelID: 10, Content: "legacy plain text", AttachmentJSON: json.RawMessage(`[]`), CreatedAt: time.Now()}
		if err = r.CreateReply(ctx, &legacy, map[string]any{"status": 2}, &dom.Log{ID: 206, TicketID: 200, Action: "reply", CreatedAt: time.Now()}, nil); err != nil {
			t.Fatal(err)
		}
		old, err := r.FindReply(ctx, 200, 205)
		if err != nil || len(old.RichContent) > 0 || old.Content != "legacy plain text" {
			t.Fatal("legacy plain reply changed")
		}
		for _, item := range []dom.Reply{
			{ID: 900, TicketID: 200, Type: dom.ActorClient, RelID: 10, Content: "first", AttachmentJSON: json.RawMessage("[]"), CreatedAt: time.Now()},
			{ID: 800, TicketID: 200, Type: dom.ActorClient, RelID: 10, Content: "second", AttachmentJSON: json.RawMessage("[]"), CreatedAt: time.Now()},
		} {
			row := item
			if err := r.CreateReply(ctx, &row, map[string]any{}, &dom.Log{ID: item.ID + 1000, TicketID: 200, ActorType: dom.ActorClient, ActorID: 10, Action: "reply", CreatedAt: time.Now()}, nil); err != nil {
				t.Fatal(err)
			}
		}
		delta, err := r.ListReplies(ctx, 200, 900)
		if err != nil || len(delta) != 1 || delta[0].ID != 800 {
			t.Fatalf("opaque cursor lost reply: %v, %v", delta, err)
		}
		if r.LastReplyID(ctx, 200) != 800 {
			t.Fatal("latest reply must follow insertion order")
		}

	})
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
