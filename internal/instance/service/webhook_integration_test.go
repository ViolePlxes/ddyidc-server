package instance

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	dom "ddyidc/internal/instance/domain"
	repo "ddyidc/internal/instance/repository"
	"ddyidc/internal/shared/snowflake"
	"ddyidc/internal/shared/tasks"
	um "ddyidc/internal/upstream/model"
	upstream "ddyidc/internal/upstream/service"
	crypt "ddyidc/pkg/crypto"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

func TestWebhookIsolationMySQL(t *testing.T) {
	socket := os.Getenv("FOUNDATION_MYSQL_SOCKET")
	if socket == "" {
		t.Skip("isolated MySQL required")
	}
	if !strings.HasPrefix(socket, "/tmp/ddyidc-foundation-") || !strings.HasSuffix(socket, "/mysql.sock") {
		t.Fatal("unsafe socket")
	}
	db, err := gorm.Open(mysql.Open("root@unix("+socket+")/?parseTime=true"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	pool, _ := db.DB()
	defer pool.Close()
	ctx := context.Background()
	if err := snowflake.Configure(ctx, pool); err != nil {
		t.Fatal(err)
	}
	var reads atomic.Int32
	var delay atomic.Bool
	entered, release := make(chan struct{}), make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/zjmf_api_login":
			fmt.Fprint(w, `{"status":200,"jwt":"isolated-webhook-jwt"}`)
		case "/provision/default":
			reads.Add(1)
			_ = r.ParseForm()
			if r.Form.Get("id") != "42" || r.Form.Get("func") != "status" {
				t.Error("unexpected provider operation")
			}
			if delay.CompareAndSwap(true, false) {
				close(entered)
				<-release
				fmt.Fprint(w, `{"status":200,"data":{"status":"off"}}`)
				return
			}
			fmt.Fprint(w, `{"status":200,"data":{"status":"on"}}`)
		default:
			t.Error("unexpected outbound path", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	key := []byte("0123456789abcdef0123456789abcdef")
	secret := "isolated-webhook-key"
	cipher, err := crypt.Encrypt(secret, key)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []int64{801, 802} {
		supplier := um.Supplier{ID: fmt.Sprint(id), Name: "isolated webhook", Type: "finance", URL: server.URL, Username: fmt.Sprint(id), Token: cipher, Secret: cipher, Rate: 1, Status: 1}
		if err := db.Create(&supplier).Error; err != nil {
			t.Fatal(err)
		}
		host := dom.Host{ID: 9000 + id, InstanceNo: fmt.Sprintf("HOOK%d", id), CustomerID: 77, ProductID: 91001, ModuleType: "cloud", ConfigSnapshot: "{}", Status: 4, PowerStatus: "stopped", UpstreamProviderID: &id, UpstreamInstanceID: "42", OwnershipVersion: 1}
		if err := db.Create(&host).Error; err != nil {
			t.Fatal(err)
		}
		if err := db.Create(&um.UpstreamHost{ID: fmt.Sprint(10000 + id), HostID: fmt.Sprint(host.ID), SupplierID: fmt.Sprint(id), UpstreamHostID: "42", Status: "stopped"}).Error; err != nil {
			t.Fatal(err)
		}
	}
	var matches int64
	if err := db.Table("instance.instances AS i").Joins("JOIN instance.upstream_hosts h ON h.host_id=i.id AND h.supplier_id=i.upstream_provider_id AND BINARY h.upstream_host_id=BINARY i.upstream_instance_id").Where("i.id=9801 AND i.upstream_provider_id=801 AND i.deleted_at IS NULL").Count(&matches).Error; err != nil || matches != 1 {
		t.Fatalf("fixture binding: count=%d error=%v", matches, err)
	}
	store := upstream.NewStore(db, key)
	sign := func(body []byte) string {
		h := hmac.New(sha256.New, []byte(secret))
		h.Write(body)
		return hex.EncodeToString(h.Sum(nil))
	}
	body := []byte(`{"HostID":"9801","Status":"deleted","DueTime":1}`)
	if store.HandleWebhook(ctx, "802", sign(body), body) == nil {
		t.Fatal("correct signature crossed supplier binding")
	}
	unknown := []byte(`{"HostID":"999999","Status":"running"}`)
	if store.HandleWebhook(ctx, "801", sign(unknown), unknown) == nil {
		t.Fatal("unknown mapping fabricated")
	}
	if store.HandleWebhook(ctx, "801", "forged", body) == nil {
		t.Fatal("forged signature accepted")
	}
	var wg sync.WaitGroup
	start := make(chan struct{})
	for n := 0; n < 12; n++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			if err := store.HandleWebhook(ctx, "801", sign(body), body); err != nil {
				t.Error(err)
			}
		}()
	}
	close(start)
	wg.Wait()
	var count, deliveries int64
	db.Table("system.upstream_event_inbox").Count(&count)
	if count != 1 {
		t.Fatal("duplicate inbox evidence", count)
	}
	db.Raw("SELECT deliveries FROM system.upstream_event_inbox").Scan(&deliveries)
	if deliveries != 12 {
		t.Fatal("lost receipt count", deliveries)
	}
	db.Table("system.durable_tasks").Where("kind='upstream_refresh' AND status='pending'").Count(&count)
	if count != 1 {
		t.Fatal("duplicate pending refresh", count)
	}
	var host dom.Host
	db.First(&host, 9801)
	if host.PowerStatus != "stopped" || host.ExpiresAt != nil || reads.Load() != 0 {
		t.Fatal("callback applied unversioned body or called supplier inline")
	}
	var task tasks.Task
	db.Where("kind='upstream_refresh' AND status='pending'").Take(&task)
	var payload tasks.RefreshPayload
	if err := json.Unmarshal([]byte(task.Payload), &payload); err != nil {
		t.Fatal(err)
	}
	service := NewService(repo.NewRepository(db), store)
	service.SetManagement(db)
	if err := service.RefreshWebhook(ctx, payload); err != nil {
		t.Fatal(err)
	}
	db.First(&host, 9801)
	if host.PowerStatus != "running" || host.Status != 3 {
		t.Fatal("fresh provider state not applied")
	}
	var other dom.Host
	db.First(&other, 9802)
	if other.PowerStatus != "stopped" {
		t.Fatal("other supplier mutated")
	}
	db.Model(&tasks.Task{}).Where("id=?", task.ID).Update("status", "succeeded")
	if err := store.HandleWebhook(ctx, "801", sign(body), body); err != nil {
		t.Fatal(err)
	}
	if err := service.RefreshWebhook(ctx, payload); err != nil {
		t.Fatal(err)
	}
	db.First(&host, 9801)
	if host.PowerStatus != "running" {
		t.Fatal("old replay overwrote fresh status")
	}
	delay.Store(true)
	oldResult := make(chan *StatusView, 1)
	oldError := make(chan error, 1)
	go func() { v, e := service.Status(ctx, 77, 9801); oldResult <- v; oldError <- e }()
	<-entered
	latest, latestErr := service.Status(ctx, 77, 9801)
	close(release)
	older, olderErr := <-oldResult, <-oldError
	if latestErr != nil || olderErr != nil || latest.PowerStatus != "running" || older.PowerStatus != "running" {
		t.Fatal("older read overwrote or returned stale state")
	}
	db.First(&host, 9801)
	if host.PowerStatus != "running" {
		t.Fatal("older read overwrote database state")
	}
	db.Model(&dom.Host{}).Where("id=9801").Update("ownership_version", 2)
	before := reads.Load()
	if service.RefreshWebhook(ctx, payload) == nil || reads.Load() != before {
		t.Fatal("stale binding queried remote")
	}
	t.Log("real MySQL + controlled HTTP: wrong supplier/unknown mapping/forged signature rejected; 12 signed duplicates -> one pending read, raw state never applied; replay reads current truth; changed ownership rejected before network")
}
