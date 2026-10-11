package main

import (
	"context"
	"ddyidc/internal/cdnsell"
	paymentservice "ddyidc/internal/payment/service"
	"ddyidc/internal/shared/testenv"
	"ddyidc/internal/shared/workgate"
	"os"
	"strings"
	"testing"
	"time"
)

func TestCDNBrowserDispatcherMySQL(t *testing.T) {
	if os.Getenv("CDN_BROWSER_DISPATCHER") != "1" {
		t.Skip("explicit isolated browser worker required")
	}
	if !strings.HasPrefix(os.Getenv("FOUNDATION_MYSQL_SOCKET"), "/tmp/ddyidc-foundation-rollback-") {
		t.Fatal("non-rehearsal database refused")
	}
	db := testenv.MySQL(t)
	pool, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	workgate.Configure(pool)
	var foreign int64
	if err = db.Table("system.durable_tasks").Where("status IN ('pending','running') AND (kind<>'post_payment' OR JSON_UNQUOTE(JSON_EXTRACT(payload,'$.customerId'))<>'930100')").Count(&foreign).Error; err != nil {
		t.Fatal(err)
	}
	if foreign != 0 {
		t.Fatal("foreign task in isolated worker database")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 75*time.Second)
	defer cancel()
	paymentservice.NewDeferredDurableDispatcher(db, paymentCoordinator{cdnsell: cdnsell.NewService(db, nil, nil)}).Start(ctx)
	if err = os.WriteFile("/home/ddyidc/cdn-browser-dispatcher-ready-20261011", []byte("ready"), 0600); err != nil {
		t.Fatal(err)
	}
	<-ctx.Done()
	t.Log("isolated actual payment coordinator and durable queue ran without external providers")
}
