package customer

import (
	"context"
	domain "ddyidc/internal/customer/domain"
	repository "ddyidc/internal/customer/repository"
	"ddyidc/pkg/auth"
	"ddyidc/pkg/cache"
	"ddyidc/pkg/db"
	"encoding/json"
	"fmt"
	"github.com/redis/go-redis/v9"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestV10HistoryReadAPI(t *testing.T) {
	socket := os.Getenv("V10_TARGET_SOCKET")
	if socket == "" {
		t.Skip("isolated target required")
	}
	if !strings.HasPrefix(socket, "/tmp/ddyidc-foundation-v10-target-") {
		t.Fatal("unsafe history API test target")
	}
	database, err := gorm.Open(mysql.Open("root@unix("+socket+")/?parseTime=true"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	pool, _ := database.DB()
	defer pool.Close()
	previous, previousPool := db.G, db.DB
	db.G, db.DB = database, pool
	defer func() { db.G, db.DB = previous, previousPool }()
	var isolated int
	database.Raw("SELECT @@skip_networking").Scan(&isolated)
	if isolated != 1 {
		t.Fatal("unsafe target")
	}
	redisSocket := filepath.Join(t.TempDir(), "redis.sock")
	command := exec.Command("redis-server", "--port", "0", "--unixsocket", redisSocket, "--unixsocketperm", "700", "--save", "", "--appendonly", "no")
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { command.Process.Kill(); command.Wait() }()
	client := redis.NewClient(&redis.Options{Network: "unix", Addr: redisSocket})
	defer client.Close()
	oldCache := cache.RDB
	cache.RDB = client
	defer func() { cache.RDB = oldCache }()
	for deadline := time.Now().Add(5 * time.Second); client.Ping(context.Background()).Err() != nil; {
		if time.Now().After(deadline) {
			t.Fatal("private Redis unavailable")
		}
		time.Sleep(10 * time.Millisecond)
	}
	manager := auth.NewManager("history-api-fixture-access", "history-api-fixture-refresh", 3600, 3600)
	if err := manager.LoadSecurityConfig(context.Background(), database); err != nil {
		t.Fatal(err)
	}
	h := &authHandler{customers: repository.NewRepository(database), jwt: manager}
	anonymous := httptest.NewRecorder()
	h.legacyHistory(anonymous, httptest.NewRequest("GET", "/api/v1/console/history/records?kind=balance", nil))
	if anonymous.Code != 401 {
		t.Fatal("anonymous history read allowed")
	}
	var owners []domain.Customer
	if err := database.Raw("SELECT c.* FROM customer.customers AS c WHERE c.id IN (SELECT customer_id FROM system.v10_history_records) AND c.status=1").Scan(&owners).Error; err != nil {
		t.Fatal(err)
	}
	totalRead := 0
	totalReplies := 0
	for _, owner := range owners {
		pair, err := manager.IssueDeviceSession(context.Background(), owner.ID, owner.Username, owner.AuthVersion, "offline-history-api-test", "127.0.0.1")
		if err != nil {
			t.Fatal(err)
		}
		for _, kind := range []string{"balance", "payment", "ticket"} {
			var expected int64
			database.Table("system.v10_history_records").Where("customer_id=? AND kind=?", owner.ID, kind).Count(&expected)
			seen := map[string]bool{}
			for page := 1; ; page++ {
				request := httptest.NewRequest("GET", fmt.Sprintf("/api/v1/console/history/records?kind=%s&page=%d&pageSize=20&customer_id=0", kind, page), nil)
				request.Header.Set("Authorization", "Bearer "+pair.AccessToken)
				response := httptest.NewRecorder()
				h.legacyHistory(response, request)
				if response.Code != 200 {
					t.Fatal("active owner's history denied")
				}
				var result struct {
					Data struct {
						List []struct {
							ID       string         `json:"id"`
							SourceID string         `json:"sourceId"`
							Data     map[string]any `json:"data"`
						}
						Total int64
					}
				}
				if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
					t.Fatal(err)
				}
				if result.Data.Total != expected {
					t.Fatal("owner filter count mismatch")
				}
				for _, row := range result.Data.List {
					if seen[row.ID] {
						t.Fatal("pagination duplicates")
					}
					seen[row.ID] = true
					for _, private := range []string{"notes", "admin_id", "rel_id", "client_id"} {
						if _, ok := row.Data[private]; ok {
							t.Fatal("private source field exposed")
						}
					}
					var actualOwner int64
					database.Table("system.v10_history_records").Select("customer_id").Where("id=?", row.ID).Scan(&actualOwner)
					if actualOwner != owner.ID {
						t.Fatal("cross-owner history leaked")
					}
					if kind == "ticket" {
						detailRequest := httptest.NewRequest("GET", "/api/v1/console/history/ticket?id="+row.ID, nil)
						detailRequest.Header.Set("Authorization", "Bearer "+pair.AccessToken)
						detail := httptest.NewRecorder()
						h.legacyTicket(detail, detailRequest)
						if detail.Code != 200 {
							t.Fatal("owned ticket detail denied")
						}
						var conversation struct {
							Data struct{ Replies []legacyHistoryRow }
						}
						json.Unmarshal(detail.Body.Bytes(), &conversation)
						var expectedReplies int64
						database.Table("system.v10_history_records").Where("kind='reply' AND customer_id=? AND source_ticket_id=?", owner.ID, row.SourceID).Count(&expectedReplies)
						if int64(len(conversation.Data.Replies)) != expectedReplies {
							t.Fatal("ticket replies incomplete")
						}
						totalReplies += len(conversation.Data.Replies)
					}
				}
				if int64(len(seen)) >= expected {
					break
				}
				if len(result.Data.List) == 0 {
					t.Fatal("pagination ended before all records")
				}
			}
			totalRead += len(seen)
		}
		var foreignID int64
		database.Table("system.v10_history_records").Select("id").Where("kind='ticket' AND customer_id<>?", owner.ID).Limit(1).Scan(&foreignID)
		if foreignID > 0 {
			request := httptest.NewRequest("GET", fmt.Sprintf("/api/v1/console/history/ticket?id=%d", foreignID), nil)
			request.Header.Set("Authorization", "Bearer "+pair.AccessToken)
			response := httptest.NewRecorder()
			h.legacyTicket(response, request)
			if response.Code != 404 {
				t.Fatal("foreign ticket detail accessible")
			}
		}
	}
	t.Logf("all %d active history owners: paginated %d list records and %d replies; unauthenticated denied; cross-owner ticket denied; private notes filtered", len(owners), totalRead, totalReplies)
}
