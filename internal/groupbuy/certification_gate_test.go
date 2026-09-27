package groupbuy

import (
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
