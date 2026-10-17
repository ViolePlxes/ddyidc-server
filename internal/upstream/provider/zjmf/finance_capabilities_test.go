package zjmf

import (
	billing "ddyidc/internal/billing/balance"
	"ddyidc/internal/upstream/provider/contracts"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRescueFormAndCapabilityDeclarations(t *testing.T) {
	form := instanceActionForm(55101, "rescue_system", contracts.InstanceActionInput{Action: "rescue", Password: "ExamplePass123", RescueSystem: "2", Force: true})
	if form.Get("temp_pass") != "ExamplePass123" || form.Get("system") != "2" || form.Get("force") != "on" || form.Get("password") != "" || form.Get("id") != "55101" {
		t.Fatal("incorrect rescue form mapping")
	}
	value := map[string]any{"control": map[string]any{"hard_off": map[string]any{"func": "hard_off"}, "hard_reboot": map[string]any{"func": "hard_reboot", "disabled": true}}, "custom": []any{map[string]any{"func": "exitRescue"}}}
	if !hasModuleAction(value, "hard_off") || hasModuleAction(value, "hard_reboot") || !hasModuleAction(value, "exitRescue") || hasModuleAction(value, "rescue_system") {
		t.Fatal("capability declaration ignored")
	}
}

func TestUpgradePlansAcceptFinanceProductFieldNames(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/zjmf_api_login":
			fmt.Fprint(w, `{"status":200,"jwt":"isolated"}`)
		case "/upgrade/upgrade_product/55101":
			fmt.Fprint(w, `{"status":200,"data":{"old_host":{"pid":247},"host":[{"pid":256,"host":"香港云服务器-C型","cycle":[{"billingcycle":"monthly","billingcycle_zh":"月付"}]}]}}`)
		default:
			t.Errorf("unexpected request %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	p := New(contracts.Supplier{URL: server.URL, Username: "isolated", Token: "isolated"})
	plans, err := p.UpgradePlans(55101)
	if err != nil || len(plans) != 1 || plans[0].ProductID != "256" || plans[0].Name != "香港云服务器-C型" || len(plans[0].Cycles) != 1 || plans[0].Cycles[0].ID != "monthly" {
		t.Fatalf("finance product plans not recognized: %#v %v", plans, err)
	}
}

func TestUpgradeInvoiceChecksHostAmountAndAlreadyPaidCredit(t *testing.T) {
	host := "55101"
	state := "Unpaid"
	subtotal := "15.58"
	payments := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/zjmf_api_login":
			fmt.Fprint(w, `{"status":200,"jwt":"isolated"}`)
		case "/get_invoices_detail":
			fmt.Fprintf(w, `{"status":200,"data":{"detail":{"id":501,"status":%q,"subtotal":%q},"invoice_items":[{"type":"upgrade","hid":%q,"amount":"15.58"}]}}`, state, subtotal, host)
		case "/apply_credit":
			payments++
			fmt.Fprint(w, `{"status":1001}`)
		default:
			t.Errorf("unexpected request %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	p := New(contracts.Supplier{URL: server.URL, Username: "isolated", Token: "isolated"})
	cost, _ := billing.ParseMoney("15.58")
	if _, e := p.verifyUpgradeInvoice("501", "55101", cost); e != nil {
		t.Fatal(e)
	}
	host = "55102"
	if _, e := p.verifyUpgradeInvoice("501", "55101", cost); e == nil {
		t.Fatal("wrong host accepted")
	}
	host = "55101"
	subtotal = "15.59"
	if _, e := p.verifyUpgradeInvoice("501", "55101", cost); e == nil {
		t.Fatal("changed invoice amount accepted")
	}
	subtotal = "15.58"
	state = "Paid"
	if e := p.PayUpgradeInvoice("501"); e != nil || payments != 0 {
		t.Fatal("paid invoice debited credit again", e)
	}
	state = "Unpaid"
	if e := p.PayUpgradeInvoice("501"); e != nil || payments != 1 {
		t.Fatal("unpaid invoice credit request incorrect", e)
	}
	state = "Cancelled"
	if e := p.PayUpgradeInvoice("501"); e == nil || payments != 1 {
		t.Fatal("cancelled invoice reached payment")
	}
}
