package marketplace

import (
	"context"
	"ddyidc/internal/certification"
	"ddyidc/internal/shared/snowflake"
	"ddyidc/internal/shared/testenv"
	"errors"
	"fmt"
	"testing"
)

func TestCertificationGateRealDatabase(t *testing.T) {
	db := testenv.MySQL(t).Begin()
	if db.Error != nil {
		t.Fatal(db.Error)
	}
	defer db.Rollback()
	yes, no := snowflake.GenID(), snowflake.GenID()
	for _, id := range []int64{yes, no} {
		if err := db.Exec("INSERT INTO customer.customers(id,username,password_hash,status) VALUES(?,?,'disabled-fixture',1)", id, fmt.Sprint("gate-", id)).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Create(&certification.Record{ID: snowflake.GenID(), CustomerID: yes, Type: 1, Name: "isolated gate fixture", DocumentHash: fmt.Sprintf("%064d", yes), Status: 2}).Error; err != nil {
		t.Fatal(err)
	}
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
