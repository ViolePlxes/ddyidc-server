package instance

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestHideProcurementRemovesSupplierBilling(t *testing.T) {
	view := &DetailView{
		FirstPayment:    "45.50",
		UpstreamOrderID: "58492",
		UpstreamOrderNo: "2026103098499999",
		InvoiceID:       "951438",
		RenewalAmount:   "65.00",
	}
	view.HideProcurement()
	raw, err := json.Marshal(view)
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"firstPayment", "upstreamOrderId", "upstreamOrderNo", "invoiceId"} {
		if strings.Contains(string(raw), field) {
			t.Fatalf("customer view leaked %s: %s", field, raw)
		}
	}
	if !strings.Contains(string(raw), `"renewalAmount":"65.00"`) {
		t.Fatalf("retail renewal price must stay: %s", raw)
	}
}
