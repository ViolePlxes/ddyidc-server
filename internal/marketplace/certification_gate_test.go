package marketplace

import (
	"context"
	"ddyidc/internal/certification"
	"errors"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
	"os"
	"strconv"
	"testing"
)

func TestCertificationGateRealDatabase(t *testing.T) {
	dsn := os.Getenv("CERT_TEST_DSN")
	if dsn == "" {
		t.Skip("requires isolated VM certification fixtures")
	}
	db, err := gorm.Open(mysql.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal("database unavailable")
	}
	yes, _ := strconv.ParseInt(os.Getenv("CERT_APPROVED_ID"), 10, 64)
	no, _ := strconv.ParseInt(os.Getenv("CERT_UNAPPROVED_ID"), 10, 64)
	svc := NewService(db, nil, nil)
	settings := &Settings{RequireRealnameListing: true, RequireRealnamePurchase: true}
	for _, listing := range []bool{false, true} {
		if err := svc.checkUserRisk(context.Background(), no, settings, listing); !errors.Is(err, certification.ErrRequired) {
			t.Fatal("unapproved customer not blocked")
		}
		if err := svc.checkUserRisk(context.Background(), yes, settings, listing); err != nil {
			t.Fatal(err)
		}
	}
}
