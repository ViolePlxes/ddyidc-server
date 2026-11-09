package visitanalytics

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

func openIntegrationDB(t *testing.T) *sql.DB {
	t.Helper()
	socket := os.Getenv("FOUNDATION_MYSQL_SOCKET")
	if socket == "" {
		t.Skip("requires disposable FOUNDATION_MYSQL_SOCKET; run deploy/linux/tests/test_schema_migrations.sh")
	}
	if !strings.HasPrefix(socket, "/tmp/ddyidc-foundation-") || !strings.HasSuffix(socket, "/mysql.sock") {
		t.Fatal("unsafe MySQL socket")
	}
	database, err := sql.Open("mysql", "root@unix("+socket+")/?parseTime=true&loc=Local&multiStatements=true")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	if err := database.Ping(); err != nil {
		t.Fatal(err)
	}
	var tables int
	if err := database.QueryRow("SELECT COUNT(*) FROM information_schema.TABLES WHERE TABLE_SCHEMA='system' AND TABLE_NAME IN ('visit_events','visit_daily','visit_analytics_audits')").Scan(&tables); err != nil {
		t.Fatal(err)
	}
	if tables != 3 {
		t.Fatalf("visit analytics schema missing (%d/3); run the offline migration first", tables)
	}
	return database
}

func integrationRecord(at time.Time, host, path, ipHash string, status int, identity string, identityID int64, client Client, source string, country, region, city string) Record {
	return buildRecord(recordInput{
		At: at, RequestID: "it-" + host, Host: host, Method: "GET", Path: path,
		RefererHost: source, IdentityType: identity, IdentityID: identityID,
		StatusCode: status, DurationMS: 12, Client: client, IPHash: ipHash,
		Country: country, Region: region, City: city, API: IsAPIPath(path),
	})
}

func TestVisitAnalyticsMySQL(t *testing.T) {
	database := openIntegrationDB(t)
	ctx := context.Background()
	service := New(Config{PageSamplePercent: 100, APISamplePercent: 100, QueueSize: 64}, database, nil, []byte("integration-secret"))
	today := time.Now().Truncate(time.Millisecond)
	host := fmt.Sprintf("visit-it-%d.example.com", today.UnixNano())
	defer func() {
		database.Exec("DELETE FROM system.visit_events WHERE host=?", host)
		database.Exec("DELETE FROM system.visit_daily WHERE day IN (?,?)", today.Format("2006-01-02"), today.AddDate(0, 0, -1).Format("2006-01-02"))
	}()

	chrome := ParseClient("Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 Chrome/131.0.0.0 Safari/537.36")
	bot := ParseClient("Mozilla/5.0 (compatible; Googlebot/2.1; +http://www.google.com/bot.html)")
	customerID := int64(271828182)
	hashA := HashIP(service.key, "203.0.113.10")
	hashB := HashIP(service.key, "203.0.113.11")
	batch := []Record{
		integrationRecord(today, host, "/products/42", hashA, 200, "guest", 0, chrome, "www.baidu.com", "中国", "广东省", "深圳市"),
		integrationRecord(today, host, "/products/43", hashB, 200, "guest", 0, chrome, "www.baidu.com", "中国", "广东省", "深圳市"),
		integrationRecord(today, host, "/console/orders", hashA, 200, "customer", customerID, chrome, "", "中国", "广东省", "深圳市"),
		integrationRecord(today, host, "/console/orders", hashA, 502, "customer", customerID, chrome, "", "中国", "广东省", "深圳市"),
		integrationRecord(today, host, "/", hashB, 404, "guest", 0, bot, "", "美国", "加利福尼亚州", "洛杉矶"),
	}
	if err := service.flush(batch); err != nil {
		t.Fatal(err)
	}

	window := Range{FromDate: today.Format("2006-01-02"), ToDate: today.Format("2006-01-02"), Days: 1}
	window.From, _ = time.ParseInLocation("2006-01-02", window.FromDate, time.Local)
	window.To = window.From.AddDate(0, 0, 1)

	overview, err := service.Overview(ctx, window, Filter{Identity: "all", Client: "all"})
	if err != nil {
		t.Fatal(err)
	}
	if overview.Requests != 5 || overview.Errors != 1 {
		t.Fatalf("overview = %+v", overview)
	}
	if len(overview.Trend) != 1 || overview.Trend[0].Requests != 5 {
		t.Fatalf("trend = %+v", overview.Trend)
	}
	if overview.Visitors != 2 {
		t.Fatalf("visitors = %d, want 2 distinct addresses in one batch", overview.Visitors)
	}

	filtered, err := service.Overview(ctx, window, Filter{Identity: "customer", Client: "all"})
	if err != nil {
		t.Fatal(err)
	}
	if filtered.Requests != 2 || filtered.Errors != 1 {
		t.Fatalf("customer slice = %+v", filtered)
	}
	botOnly, err := service.Overview(ctx, window, Filter{Identity: "all", Client: "bot"})
	if err != nil || botOnly.Requests != 1 {
		t.Fatalf("bot slice = %+v err=%v", botOnly, err)
	}
	combined, err := service.Overview(ctx, window, Filter{Identity: "customer", Client: "browser"})
	if err != nil || combined.Requests != 2 {
		t.Fatalf("combined slice = %+v err=%v", combined, err)
	}

	flow, err := service.Flow(ctx, window)
	if err != nil {
		t.Fatal(err)
	}
	foundBaidu := false
	for _, row := range flow {
		if row.Source == "www.baidu.com" && row.Page == "/products/:id" && row.Client == "Chrome" && row.Identity == "guest" && row.Requests == 2 {
			foundBaidu = true
		}
		if strings.Contains(row.Page, "/api/") {
			t.Fatalf("interface traffic leaked into the page stage: %+v", row)
		}
	}
	if !foundBaidu {
		t.Fatalf("baidu flow row missing: %+v", flow)
	}

	sources, err := service.Traffic(ctx, window, "source")
	if err != nil || len(sources) == 0 || sources[0].Label != "直接访问" {
		t.Fatalf("source rows = %+v err=%v", sources, err)
	}
	pages, err := service.Traffic(ctx, window, "page")
	if err != nil {
		t.Fatal(err)
	}
	pageCount := int64(0)
	for _, row := range pages {
		if row.Label == "/products/:id" {
			pageCount = row.Requests
		}
	}
	if pageCount != 2 {
		t.Fatalf("page rows = %+v", pages)
	}
	identities, err := service.Traffic(ctx, window, "identity")
	if err != nil || len(identities) == 0 {
		t.Fatalf("identity rows = %+v err=%v", identities, err)
	}

	geography, err := service.Geography(ctx, window)
	if err != nil || len(geography) == 0 {
		t.Fatalf("geography = %+v err=%v", geography, err)
	}
	if geography[0].Country != "中国" || geography[0].Region != "广东省" || geography[0].City != "深圳市" || geography[0].Requests != 4 {
		t.Fatalf("geography rows = %+v", geography)
	}

	clients, err := service.Clients(ctx, window)
	if err != nil || len(clients) == 0 {
		t.Fatalf("clients = %+v err=%v", clients, err)
	}
	bots := int64(0)
	for _, row := range clients {
		if row.IsBot {
			bots += row.Requests
		}
	}
	if bots != 1 {
		t.Fatalf("bot rows = %+v", clients)
	}

	status, err := service.Status(ctx, window)
	if err != nil || len(status) == 0 {
		t.Fatalf("status = %+v err=%v", status, err)
	}
	byCode := map[string]int64{}
	for _, row := range status {
		byCode[row.Status] = row.Requests
	}
	if byCode["502"] != 1 || byCode["404"] != 1 || byCode["200"] != 3 {
		t.Fatalf("status rows = %+v", byCode)
	}

	// Raw addresses and user agents must never be recoverable from the table.
	var addressLeak, agentLeak int
	if err := database.QueryRow("SELECT COUNT(*) FROM system.visit_events WHERE host=? AND (CONCAT_WS('|',ip_hash,path,route,referer_path,page_title,browser,os,device) LIKE '%203.0.113%' OR CONCAT_WS('|',ip_hash,path,route,referer_path,page_title,browser,os,device) LIKE '%Mozilla%')", host).Scan(&addressLeak); err != nil {
		t.Fatal(err)
	}
	if addressLeak != 0 {
		t.Fatal("raw address or user agent is recoverable from visit_events")
	}
	var storedHash string
	if err := database.QueryRow("SELECT ip_hash FROM system.visit_events WHERE host=? LIMIT 1", host).Scan(&storedHash); err != nil {
		t.Fatal(err)
	}
	if len(storedHash) != 64 || !strings.HasPrefix(storedHash, hashA[:8]) && !strings.HasPrefix(storedHash, hashB[:8]) {
		t.Fatalf("stored address digest = %q", storedHash)
	}
	if err := database.QueryRow("SELECT COUNT(*) FROM system.visit_events WHERE host=? AND identity_id IS NOT NULL AND identity_type='customer'", host).Scan(&agentLeak); err != nil {
		t.Fatal(err)
	}
	if agentLeak != 2 {
		t.Fatalf("customer identity rows = %d, want 2", agentLeak)
	}
}

func TestEnsurePartitionsMySQL(t *testing.T) {
	database := openIntegrationDB(t)
	ctx := context.Background()
	first, err := EnsurePartitions(ctx, database, time.Now(), 90)
	if err != nil {
		t.Fatal(err)
	}
	partitions, err := readPartitions(ctx, database)
	if err != nil {
		t.Fatal(err)
	}
	catchAll := 0
	months := 0
	for _, item := range partitions {
		if item.Max {
			catchAll++
		} else {
			months++
		}
	}
	if catchAll != 1 {
		t.Fatalf("MAXVALUE partitions = %d, want 1", catchAll)
	}
	if months < partitionHorizonMonths {
		t.Fatalf("monthly partitions = %d, want at least %d", months, partitionHorizonMonths)
	}
	horizon, err := PartitionHorizon(ctx, database)
	if err != nil {
		t.Fatal(err)
	}
	if horizon.Before(time.Now().AddDate(0, 4, 0)) {
		t.Fatalf("partition horizon %s is too close", horizon)
	}
	second, err := EnsurePartitions(ctx, database, time.Now(), 90)
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Added) != 0 {
		t.Fatalf("second run added partitions: %v", second.Added)
	}
	t.Logf("first run added %d partitions, dropped %d; horizon=%s", len(first.Added), len(first.Dropped), horizon.Format("2006-01-02"))
}

func TestExpiredPartitionSelection(t *testing.T) {
	monthly := map[int64]string{100: "p1", 200: "p2", 300: "p3"}
	expired := expiredPartitions(monthly, 200)
	if len(expired) != 2 || expired[0].Name != "p1" || expired[1].Name != "p2" {
		t.Fatalf("expired = %+v", expired)
	}
	if len(expiredPartitions(monthly, 99)) != 0 {
		t.Fatal("no partition is older than the cutoff")
	}
}

func TestMonthStart(t *testing.T) {
	start := monthStart(time.Date(2026, 11, 9, 15, 4, 5, 0, time.Local))
	if start != time.Date(2026, 11, 1, 0, 0, 0, 0, time.Local) {
		t.Fatalf("monthStart = %s", start)
	}
}
