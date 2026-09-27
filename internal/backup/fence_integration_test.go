package backup

import (
	"context"
	"database/sql"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
	"os"
	"strings"
	"testing"
	"time"
)

func TestRuntimeAccountFenceMySQL(t *testing.T) {
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
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := db.Exec("CREATE USER 'ddyidc_runtime'@'localhost'").Error; err != nil {
		t.Fatal(err)
	}
	defer db.Exec("DROP USER 'ddyidc_runtime'@'localhost'")
	var nodes []*sql.DB
	for n := 0; n < 2; n++ {
		node, err := sql.Open("mysql", "ddyidc_runtime@unix("+socket+")/")
		if err != nil {
			t.Fatal(err)
		}
		defer node.Close()
		if err := node.PingContext(ctx); err != nil {
			t.Fatal(err)
		}
		nodes = append(nodes, node)
	}
	accounts, err := runtimeAccounts(ctx, db, "ddyidc_runtime")
	if err != nil {
		t.Fatal(err)
	}
	if err := fenceAccounts(ctx, db, accounts); err != nil {
		t.Fatal(err)
	}
	for _, node := range nodes {
		if err := node.PingContext(ctx); err == nil {
			t.Fatal("fenced node remained connected or reconnected")
		}
	}
	third, err := sql.Open("mysql", "ddyidc_runtime@unix("+socket+")/")
	if err != nil {
		t.Fatal(err)
	}
	defer third.Close()
	if err := third.PingContext(ctx); err == nil {
		t.Fatal("fresh node bypassed account fence")
	}
	var one int
	if err := pool.QueryRowContext(ctx, "SELECT 1").Scan(&one); err != nil || one != 1 {
		t.Fatal("maintenance identity was fenced")
	}
	if _, err := runtimeAccounts(ctx, db, "root"); err == nil {
		t.Fatal("root fencing allowed")
	}
	t.Log("isolated MySQL: two application connection pools disconnected; new authentication blocked; independent maintenance identity remains usable; no production account changed")
}
