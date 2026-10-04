package points

import (
	"context"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
	"os"
	"strings"
	"testing"
)

func TestExpiryMixedCollationMySQL(t *testing.T) {
	socket := os.Getenv("FINAL_CUTOVER_MYSQL_SOCKET")
	if socket == "" {
		t.Skip("isolated frozen database required")
	}
	if !strings.HasPrefix(socket, "/tmp/ddyidc-foundation-v10-cutover-target-") || !strings.HasSuffix(socket, "/mysql.sock") {
		t.Fatal("unsafe socket")
	}
	db, err := gorm.Open(mysql.Open("root@unix("+socket+")/?parseTime=true"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	pool, _ := db.DB()
	defer pool.Close()
	var count int64
	if err := db.Table(recordsTable).Count(&count).Error; err != nil || count != 0 {
		t.Fatal("empty frozen points fixture required")
	}
	if count, err := NewService(db, nil).ExpireDue(context.Background()); err != nil || count != 0 {
		t.Fatalf("mixed-collation expiry query failed: %v", err)
	}
}
