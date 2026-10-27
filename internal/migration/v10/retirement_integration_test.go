package v10

import (
	"context"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
	"os"
	"strings"
	"testing"
)

func TestRetiredImportersRefuseReplay(t *testing.T) {
	socket := os.Getenv("RETIRED_HISTORY_SOCKET")
	if socket == "" {
		t.Skip("isolated retirement target required")
	}
	if !strings.HasPrefix(socket, "/tmp/ddoidc-legacy-retirement-") || !strings.HasSuffix(socket, "/mysql.sock") {
		t.Fatal("unsafe socket")
	}
	db, err := gorm.Open(mysql.Open("root@unix("+socket+")/?parseTime=true"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	pool, _ := db.DB()
	defer pool.Close()
	var isolated int
	if err := db.Raw("SELECT @@skip_networking").Scan(&isolated).Error; err != nil || isolated != 1 {
		t.Fatal("unsafe target")
	}
	if _, err := Import(context.Background(), nil, db, "", "", ""); err == nil || !strings.Contains(err.Error(), "retired") {
		t.Fatal("retired importer was not fenced")
	}
	if _, err := SupplementHistory(context.Background(), nil, db, "", "", ""); err == nil || !strings.Contains(err.Error(), "retired") {
		t.Fatal("retired history replay was not fenced")
	}
}
