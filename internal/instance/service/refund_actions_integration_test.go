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
			json.NewEncoder(w).Encode(map[string]any{"status": 200, "data": map[string]any{"total": 4, "data": []any{map[string]any{"id": 42}, map[string]any{"id": 43}, map[string]any{"id": 44}, map[string]any{"id": 45}}}})
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
	for index, action := range []string{"direct_balance", "upstream", "upstream_rejected", "upstream_waiting_direct"} {
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
		if action == "upstream_rejected" || action == "upstream_waiting_direct" {
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

			if action == "upstream_rejected" || action == "upstream_waiting_direct" {
				mu.Lock()
				if action == "upstream_rejected" {
					statuses[h.UpstreamInstanceID] = "Rejected"
				}
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
				_, e = service.MachineRefundAction(ctx, application.ID, actor, in)
				must(e)
				another := in
				another.RequestID += "-again"
				if _, e = service.MachineRefundAction(ctx, application.ID, actor, another); e == nil {
					t.Fatal("second direct decision accepted")
				}
				mu.Lock()
				statuses[h.UpstreamInstanceID] = "Refunded"
				mu.Unlock()
				for n := 0; n < 3; n++ {
					must(service.ReconcileMachineRefund(ctx, application.ID))
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
		var credited string
		must(db.Table("billing.accounts").Select("balance").Where("customer_id=?", customer).Scan(&credited).Error)
		if credited != "87.00" {
			t.Fatal("unexpected final balance", credited)
		}
		t.Logf("EVIDENCE case=%s balance_before=0.00 balance_after=%s refund_rows=%d supplier_submit_calls=%d", action, credited, count, writes.Load())
		must(db.First(&h, host).Error)
		if h.Status != 6 {
			t.Fatal("refunded entitlement remains active")
		}
	}
}

func TestMachineRefundPendingDirectRaceMySQL(t *testing.T) {
	db := testenv.MySQL(t)
	ctx := context.Background()
	var writes atomic.Int32
	var mu sync.Mutex
	var recordGate, recordStarted chan struct{}
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
			json.NewEncoder(w).Encode(map[string]any{"status": 200, "data": map[string]any{"total": 4, "data": []any{map[string]any{"id": 42}, map[string]any{"id": 43}, map[string]any{"id": 44}, map[string]any{"id": 45}}}})
		case "refund":
			writes.Add(1)
			mu.Lock()
			reasons[r.Form.Get("hid")] = r.Form.Get("reason")
			statuses[r.Form.Get("hid")] = "Pending"
			mu.Unlock()
			fmt.Fprint(w, `{"status":200,"data":{}}`)
		case "getRefundList":
			mu.Lock()
			gate, started := recordGate, recordStarted
			mu.Unlock()
			if gate != nil {
				close(started)
				<-gate
			}
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
	for index, schedule := range []string{"direct-first", "upstream-first", "simultaneous", "upstream-in-flight"} {
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
		up := MachineRefundInput{Action: "upstream", RequestID: fmt.Sprintf("RACE-UP-%d", order), ExpectedAmount: "87.00", ExpectedUpdatedAt: application.UpdatedAt, Reason: "isolated race"}
		_, e = service.MachineRefundAction(ctx, application.ID, actor, up)
		must(e)
		mu.Lock()
		statuses[h.UpstreamInstanceID] = "Refunded"
		mu.Unlock()
		direct := up
		direct.Action = "direct_balance"
		direct.RequestID = fmt.Sprintf("RACE-DIRECT-%d", order)
		var directErr, upstreamErr error
		if schedule == "direct-first" {
			_, directErr = service.MachineRefundAction(ctx, application.ID, actor, direct)
			upstreamErr = service.ReconcileMachineRefund(ctx, application.ID)
		} else if schedule == "upstream-first" {
			upstreamErr = service.ReconcileMachineRefund(ctx, application.ID)
			_, directErr = service.MachineRefundAction(ctx, application.ID, actor, direct)
		} else if schedule == "upstream-in-flight" {
			gate, started := make(chan struct{}), make(chan struct{})
			mu.Lock()
			recordGate, recordStarted = gate, started
			mu.Unlock()
			done := make(chan struct{})
			go func() { defer close(done); upstreamErr = service.ReconcileMachineRefund(ctx, application.ID) }()
			<-started
			_, directErr = service.MachineRefundAction(ctx, application.ID, actor, direct)
			close(gate)
			<-done
			mu.Lock()
			recordGate, recordStarted = nil, nil
			mu.Unlock()
		} else {
			start := make(chan struct{})
			var workers sync.WaitGroup
			workers.Add(2)
			go func() {
				defer workers.Done()
				<-start
				_, directErr = service.MachineRefundAction(ctx, application.ID, actor, direct)
			}()
			go func() {
				defer workers.Done()
				<-start
				upstreamErr = service.ReconcileMachineRefund(ctx, application.ID)
			}()
			close(start)
			workers.Wait()
		}
		if (schedule == "direct-first" || schedule == "upstream-in-flight") && directErr != nil {
			t.Fatal(directErr)
		}
		if schedule == "upstream-first" && (upstreamErr != nil || directErr == nil) {
			t.Fatalf("upstream winner not protected: %v / %v", upstreamErr, directErr)
		}
		if directErr != nil && upstreamErr != nil {
			t.Fatalf("both paths failed: %v / %v", directErr, upstreamErr)
		}
		for n := 0; n < 3; n++ {
			must(service.ReconcileMachineRefund(ctx, application.ID))
		}
		var count int64
		var credited string
		var final MachineRefundAction
		must(db.Table("billing.transactions").Where("customer_id=? AND type='refund'", customer).Count(&count).Error)
		must(db.Table("billing.accounts").Select("balance").Where("customer_id=?", customer).Scan(&credited).Error)
		must(db.First(&final, "application_id=?", application.ID).Error)
		if count != 1 || credited != "87.00" || final.State != "completed" || writes.Load() != 1 {
			t.Fatalf("double refund or incomplete: count=%d balance=%s state=%s supplier=%d", count, credited, final.State, writes.Load())
		}
		t.Logf("EVIDENCE schedule=%s winner=%s balance_before=0.00 balance_after=%s refund_rows=%d supplier_submit_calls=%d direct_error=%v reconcile_error=%v", schedule, final.Action, credited, count, writes.Load(), directErr, upstreamErr)
	}
}
