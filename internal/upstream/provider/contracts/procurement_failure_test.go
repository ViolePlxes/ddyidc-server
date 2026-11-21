package contracts

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestProcurementFailureKeepsCategoryWithoutRawSecret(t *testing.T) {
	for _, tc := range []struct {
		cause error
		want  string
	}{
		{fmt.Errorf("URL_SECRET: %w", context.DeadlineExceeded), "timeout"},
		{fmt.Errorf("TOKEN_SECRET: %w", context.Canceled), "cancelled"},
		{errors.New("PASSWORD_SECRET CUSTOMER_SECRET"), "unclassified"},
	} {
		failure := ClassifyProcurementFailure("cart_settle", tc.cause)
		if failure.Code != tc.want || failure.Stage != "cart_settle" || !errors.Is(failure, tc.cause) || strings.Contains(failure.Error(), "SECRET") {
			t.Fatalf("unsafe diagnostic category: %v", failure)
		}
	}
	inner := &ProcurementFailure{Code: "supplier_rejected", SupplierStatus: 400, Cause: errors.New("RAW_SECRET")}
	failure := ClassifyProcurementFailure("payment", fmt.Errorf("wrapper: %w", inner))
	if failure.Stage != "payment" || inner.Stage != "" || failure.SupplierStatus != 400 || strings.Contains(failure.Error(), "SECRET") {
		t.Fatal("nested failure changed or leaked")
	}
	untrusted := ClassifyProcurementFailure("RAW_SECRET", &ProcurementFailure{Stage: "TOKEN_SECRET", Code: "PASSWORD_SECRET"})
	if strings.Contains(untrusted.Error(), "SECRET") || untrusted.Stage != "provider" || untrusted.Code != "unclassified" {
		t.Fatal("diagnostic fields must be allowlisted")
	}
}

func TestSupplierMessageSurvivesOnlyWhenProvablySafe(t *testing.T) {
	kept := ClassifyProcurementFailure("payment", errors.New("Insufffcient balance 余额不足"))
	if kept.SupplierMessage != "Insufffcient balance 余额不足" || !strings.Contains(kept.Error(), "上游返回 Insufffcient balance 余额不足") {
		t.Fatalf("safe supplier text dropped: %#v", kept)
	}
	for name, cause := range map[string]error{
		"url":    errors.New("call https://api.supplier.example/order failed"),
		"secret": errors.New("PASSWORD_SECRET CUSTOMER_SECRET"),
		"token":  errors.New("token=abc123 rejected"),
		"email":  errors.New("contact ops@example.com for help"),
		"blob":   errors.New("digest 35b52a92c566d36d53c897f179c33de82a5cffc796e2fb045c055c58d89b2266"),
	} {
		failure := ClassifyProcurementFailure("provider", cause)
		if failure.SupplierMessage != "" {
			t.Fatalf("%s leaked supplier text: %q", name, failure.SupplierMessage)
		}
		if strings.Contains(failure.Error(), "上游返回") {
			t.Fatalf("%s leaked into the persisted text: %v", name, failure)
		}
	}
	long := ClassifyProcurementFailure("provider", errors.New(strings.Repeat("拒", 400)))
	if runes := []rune(long.SupplierMessage); len(runes) != 161 || runes[160] != '…' {
		t.Fatalf("supplier text not truncated: %d", len(runes))
	}
}