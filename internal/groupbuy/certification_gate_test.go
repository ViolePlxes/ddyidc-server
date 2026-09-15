package groupbuy

import (
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
	activity := &Activity{RequireCertification: true}
	if err := svc.validateMember(db, activity, no, true); !errors.Is(err, certification.ErrRequired) {
		t.Fatal("unapproved customer not blocked")
	}
	if err := svc.validateMember(db, activity, yes, true); err != nil {
		t.Fatal(err)
	}
	activity.RequireCertification = false
	if err := svc.validateMember(db, activity, no, true); err != nil {
		t.Fatal(err)
	}
}
