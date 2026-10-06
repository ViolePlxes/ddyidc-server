package instance

import (
	"context"
	dom "ddyidc/internal/instance/domain"
	repo "ddyidc/internal/instance/repository"
	workflow "ddyidc/internal/payment/repository/refundworkflow"
	"ddyidc/internal/shared/snowflake"
	"ddyidc/internal/shared/testenv"
	"ddyidc/internal/upstream/provider/contracts"
	"ddyidc/internal/upstream/provider/zjmf"
	"ddyidc/pkg/outbound"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestMachineRefundDecisionsAndSupplierConfirmationMySQL(t *testing.T) {
	db := testenv.MySQL(t).Begin()
	defer db.Rollback()
	ctx := context.Background()
	var writes atomic.Int32
	var mu sync.Mutex
	reasons := map[string]string{}
	statuses := map[string]string{}
	amounts := map[string]string{}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		if r.URL.Path == "/zjmf_api_login" {
			fmt.Fprint(w, `{"status":200,"jwt":"isolated"}`)
			return
		}
		if r.URL.Path != "/addons" {
			t.Errorf("unexpected supplier call %s", r.URL.Path)
			http.Error(w, "unexpected", 400)
			return
		}
		if r.Method == http.MethodGet {
			fmt.Fprint(w, `<script src="/plugins/addons/qkstack/template/static/client/index.js"></script>`)
			return
		}
		switch r.Form.Get("action") {
		case "list":
			json.NewEncoder(w).Encode(map[string]any{"status": 200, "data": map[string]any{"total": 3, "data": []any{map[string]any{"id": 42}, map[string]any{"id": 43}, map[string]any{"id": 44}}}})
		case "refund":
			writes.Add(1)
			mu.Lock()
			reasons[r.Form.Get("hid")] = r.Form.Get("reason")
			statuses[r.Form.Get("hid")] = "Pending"
			mu.Unlock()
			fmt.Fprint(w, `{"status":200,"data":{}}`)
		case "getRefundList":
			mu.Lock()
			rows := []any{}
			for host, reason := range reasons {
				money := amounts[host]
				if money == "" {
					money = "80.00"
				}
				rows = append(rows, map[string]any{"id": host, "hid": host, "amount": money, "status": statuses[host], "refund_reason": reason, "update_time": time.Now().Format("2006-01-02 15:04:05")})
			}
			mu.Unlock()
			json.NewEncoder(w).Encode(map[string]any{"status": 200, "data": map[string]any{"total": len(rows), "data": rows}})
		default:
			t.Errorf("unexpected plugin action %s", r.Form.Get("action"))
		}
	}))
	defer upstream.Close()
	port := upstream.Listener.Addr().(*net.TCPAddr).Port
	if e := outbound.Configure(outbound.Config{PrivateCIDRs: []string{"127.0.0.1/32"}, Ports: []int{port}}); e != nil {
		t.Fatal(e)
	}
	defer outbound.Configure(outbound.Config{})
	service := NewService(repo.NewRepository(db), securityResolver{zjmf.New(contracts.Supplier{URL: upstream.URL, Username: "isolated", Token: "isolated"})})
	service.SetManagement(db)
	must := func(e error) {
		t.Helper()
		if e != nil {
			t.Fatal(e)
		}
	}
	actor := workflow.Actor{ID: 1, Name: "integration", IP: "127.0.0.1"}
	for index, action := range []string{"direct_balance", "upstream", "upstream_rejected"} {
		writes.Store(0)
		customer, order, item, host := snowflake.GenID(), snowflake.GenID(), snowflake.GenID(), snowflake.GenID()
		now := time.Now().UTC().Truncate(time.Second)
		start := now.Add(-10 * time.Hour)
		end := start.Add(30 * 24 * time.Hour)
		supplier := int64(1)
		must(db.Table("customer.customers").Create(map[string]any{"id": customer, "username": fmt.Sprintf("rf-%d", customer), "password_hash": "isolated", "status": 1}).Error)
		must(db.Table("`order`.orders").Create(map[string]any{"id": order, "order_no": fmt.Sprintf("RFTEST%d", order), "customer_id": customer, "status": 6, "pay_amount": 10000, "total_amount": 10000, "setup_fee": 1000, "created_at": now, "updated_at": now}).Error)
		must(db.Table("`order`.order_items").Create(map[string]any{"id": item, "order_id": order, "product_id": 1, "product_name": "isolated machine", "module_type": "dcimcloud", "qty": 1, "unit_price": 10000, "subtotal": 10000, "config_snapshot": "{}", "delivery_status": 2}).Error)
		h := dom.Host{ID: host, InstanceNo: fmt.Sprintf("RFHOST%d", host), CustomerID: customer, OrderID: order, OrderItemID: item, ProductID: 1, ProductName: "isolated", ModuleType: "dcimcloud", ConfigSnapshot: "{}", Status: 3, OwnershipVersion: 1, UpstreamProviderID: &supplier, UpstreamInstanceID: fmt.Sprint(42 + index), ActivatedAt: &start, ExpiresAt: &end, CreatedAt: now, UpdatedAt: now}
		must(db.Create(&h).Error)
		application, e := workflow.SubmitOrder(ctx, db, customer, order, "测试退款", fmt.Sprintf("RF-APPLY-%d", order))
		must(e)
		quote, e := service.MachineRefundQuote(ctx, application.ID)
		must(e)
		if quote.Calculation.Refund.String() != "87.00" {
			t.Fatal("setup or application-day proration incorrect", quote.Calculation)
		}
		var count int64
		must(db.Table("billing.transactions").Where("customer_id=?", customer).Count(&count).Error)
		if count != 0 || writes.Load() != 0 {
			t.Fatal("application or quote caused money or supplier effect")
		}
		decision := action
		if action == "upstream_rejected" {
			decision = "upstream"
		}
		in := MachineRefundInput{Action: decision, RequestID: fmt.Sprintf("RF-DECIDE-%d", order), ExpectedAmount: "999.00", ExpectedUpdatedAt: application.UpdatedAt, Reason: "原账单及服务核验完成"}
		if _, e := service.MachineRefundAction(ctx, application.ID, actor, in); e == nil {
			t.Fatal("tampered refund amount accepted")
		}
		in.ExpectedAmount = "87.00"
		out, e := service.MachineRefundAction(ctx, application.ID, actor, in)
		must(e)
		if action == "direct_balance" {
			if out.State != "completed" || writes.Load() != 0 {
				t.Fatal("direct refund reached supplier or failed")
			}
			_, e = service.MachineRefundAction(ctx, application.ID, actor, in)
			must(e)
		} else {
			if out.State != "waiting" || writes.Load() != 1 {
				t.Fatal("supplier request not durable or duplicated")
			}
			must(service.ReconcileMachineRefund(ctx, application.ID))
			must(db.Table("billing.transactions").Where("customer_id=?", customer).Count(&count).Error)
			if count != 0 {
				t.Fatal("pending supplier review prematurely credited balance")
			}
			_, e = service.MachineRefundAction(ctx, application.ID, actor, in)
			must(e)
			if writes.Load() != 1 {
				t.Fatal("request replay submitted refund again")
			}

			if action == "upstream_rejected" {
				mu.Lock()
				statuses[h.UpstreamInstanceID] = "Rejected"
				mu.Unlock()
				must(service.ReconcileMachineRefund(ctx, application.ID))
				must(db.Table("billing.transactions").Where("customer_id=?", customer).Count(&count).Error)
				if count != 0 {
					t.Fatal("supplier rejection credited balance")
				}
				in.Action = "direct_balance"
				in.RequestID += "-fallback"
				_, e = service.MachineRefundAction(ctx, application.ID, actor, in)
				must(e)
				if writes.Load() != 1 {
					t.Fatal("direct fallback submitted another supplier request")
				}
			} else {
				mu.Lock()
				statuses[h.UpstreamInstanceID] = "Refunded"
				amounts[h.UpstreamInstanceID] = "0.00"
				mu.Unlock()
				if e := service.ReconcileMachineRefund(ctx, application.ID); e == nil {
					t.Fatal("zero refund proof accepted")
				}
				must(db.Table("billing.transactions").Where("customer_id=?", customer).Count(&count).Error)
				if count != 0 {
					t.Fatal("unverified supplier refund credited balance")
				}
				mu.Lock()
				amounts[h.UpstreamInstanceID] = "80.00"
				mu.Unlock()
				must(service.ReconcileMachineRefund(ctx, application.ID))
			}
			must(service.ReconcileMachineRefund(ctx, application.ID))
		}
		must(db.Table("billing.transactions").Where("customer_id=? AND type='refund'", customer).Count(&count).Error)
		if count != 1 {
			t.Fatal("refund must credit once", count)
		}
		must(db.First(&h, host).Error)
		if h.Status != 6 {
			t.Fatal("refunded entitlement remains active")
		}
	}
}
