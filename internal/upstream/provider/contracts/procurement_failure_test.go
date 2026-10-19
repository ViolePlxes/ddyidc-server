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
