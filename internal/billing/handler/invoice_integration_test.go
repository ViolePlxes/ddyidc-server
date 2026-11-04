package billing

import (
	"context"
	api "ddyidc/internal/billing/api"
	typ "ddyidc/internal/billing/types"
	"ddyidc/internal/shared/snowflake"
	"ddyidc/internal/shared/testenv"
	"ddyidc/pkg/auth"
	"ddyidc/pkg/cache"
	sharedDB "ddyidc/pkg/db"
	"fmt"
	"github.com/zeromicro/go-zero/rest/pathvar"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestInvoiceCustomerHTTPMySQL(t *testing.T) {
	database := testenv.MySQL(t).Begin()
	defer database.Rollback()
	previousDB, previousCache := sharedDB.G, cache.RDB
	sharedDB.G, cache.RDB = database, testenv.Redis(t)
	defer func() { sharedDB.G, cache.RDB = previousDB, previousCache }()
	must := func(e error) {
		t.Helper()
		if e != nil {
			t.Fatal(e)
		}
	}
	ctx := context.Background()
	user, other, order := snowflake.GenID(), snowflake.GenID(), snowflake.GenID()
	manager := auth.NewManager("isolated-invoice-access", "isolated-invoice-refresh", 3600, 3600)
	must(manager.LoadSecurityConfig(ctx, database))
	tokens := map[int64]string{}
	for _, id := range []int64{user, other} {
		name := fmt.Sprint("invoice-http-", id)
		must(database.Table("customer.customers").Create(map[string]any{"id": id, "username": name, "password_hash": "isolated-disabled", "status": 1, "auth_version": 1}).Error)
		pair, e := manager.IssueDeviceSession(ctx, id, name, 1, "invoice-test", "127.0.0.1")
		must(e)
		tokens[id] = pair.AccessToken
	}
	must(database.Table("order.orders").Create(map[string]any{"id": order, "customer_id": user, "order_no": fmt.Sprint("HTTP", order), "pay_amount": 8000, "status": 1}).Error)
	must(database.Table("order.order_items").Create(map[string]any{"id": snowflake.GenID(), "order_id": order, "product_id": 1, "product_name": "invoice HTTP fixture", "module_type": "cloud", "qty": 1, "unit_price": 8000, "subtotal": 8000, "config_snapshot": "{}"}).Error)
	bill, e := api.IssueSourceTx(database, "order", order, user)
	must(e)
	calls := 0
	app := api.NewApplication(database, map[string]api.PaymentAdapter{"order": {
		Checkout: func(paymentCtx context.Context, owner, source int64, method, mode string) (any, error) {
			calls++
			if owner != user || source != order || typ.ExpectedInvoice(paymentCtx) != bill.ID {
				t.Fatal("checkout changed owner/source/document")
			}
			_, e := api.CheckSourcePayableTx(database.WithContext(paymentCtx), "order", source, owner, 8000)
			return map[string]any{"localTest": true}, e
		},
	}})
	h := &handler{app: app, jwt: manager}
	request := func(action, body string, customer int64) *httptest.ResponseRecorder {
		url := "/api/v1/billing/invoices"
		method := "GET"
		if action == "pay" {
			method = "POST"
			url += "/" + bill.ID + "/pay"
		}
		if action == "detail" {
			url += "/" + bill.ID
		}
		if strings.HasPrefix(action, "?") {
			url += action
		}
		r := httptest.NewRequest(method, url, strings.NewReader(body))
		r = pathvar.WithVars(r, map[string]string{"invoiceId": bill.ID})
		if customer != 0 {
			r.Header.Set("Authorization", "Bearer "+tokens[customer])
		}
		w := httptest.NewRecorder()
		switch action {
		case "detail":
			h.detail(w, r)
		case "pay":
			h.pay(w, r)
		default:
			h.list(w, r)
		}
		return w
	}
	if w := request("", "", 0); w.Code != 401 {
		t.Fatal("anonymous invoice access", w.Code)
	}
	if w := request("detail", "", other); w.Code != 404 {
		t.Fatal("foreign invoice detail", w.Code)
	}
	if w := request("pay", `{"method":"alipay","mode":"page"}`, other); w.Code != 404 {
		t.Fatal("foreign invoice payment", w.Code)
	}
	if w := request("?customerId=1", "", user); w.Code != 400 {
		t.Fatal("customer override parameter accepted")
	}
	for _, body := range []string{`{"method":"alipay","mode":"page","amount":"0.01"}`, `{"method":"alipay","mode":"page"} {}`, `{"method":"unknown"}`} {
		if w := request("pay", body, user); w.Code != 400 {
			t.Fatal("invalid payment input accepted", w.Code)
		}
	}
	if calls != 0 {
		t.Fatal("rejected input reached payment adapter")
	}
	if w := request("detail", "", user); w.Code != 200 || !strings.Contains(w.Body.String(), `"amount":"80.00"`) {
		t.Fatal("owned invoice snapshot missing", w.Code, w.Body.String())
	}
	if w := request("pay", `{"method":"alipay","mode":"page"}`, user); w.Code != 200 || calls != 1 {
		t.Fatal("invoice checkout did not dispatch exactly once", w.Code, w.Body.String())
	}
	must(database.Table("order.orders").Where("id=?", order).Updates(map[string]any{"status": 2, "paid_at": time.Now()}).Error)
	if w := request("pay", `{"method":"alipay","mode":"page"}`, user); w.Code != 409 || calls != 1 {
		t.Fatal("already paid source charged again", w.Code)
	}
}
