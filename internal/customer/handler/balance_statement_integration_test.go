package customer

import (
	"context"
	balance "ddyidc/internal/billing/balance"
	repository "ddyidc/internal/customer/repository"
	"ddyidc/internal/shared/snowflake"
	"ddyidc/internal/shared/testenv"
	"ddyidc/pkg/auth"
	"ddyidc/pkg/cache"
	"ddyidc/pkg/db"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestBalanceStatementHTTPMySQL(t *testing.T) {
	database := testenv.MySQL(t)
	redis := testenv.Redis(t)
	ctx := context.Background()
	oldDB, oldCache := db.G, cache.RDB
	db.G, cache.RDB = database, redis
	defer func() { db.G, cache.RDB = oldDB, oldCache }()
	id := snowflake.GenID()
	username := fmt.Sprintf("ledger-http-%d", id)
	if e := database.Table("customer.customers").Create(map[string]any{"id": id, "username": username, "password_hash": "isolated", "status": 1, "auth_version": 1}).Error; e != nil {
		t.Fatal(e)
	}
	defer database.Exec("DELETE FROM billing.transactions WHERE customer_id=?", id)
	defer database.Exec("DELETE FROM billing.accounts WHERE customer_id=?", id)
	defer database.Exec("DELETE FROM customer.customers WHERE id=?", id)
	_, e := balance.New(database).Change(ctx, balance.ChangeRequest{CustomerID: id, Type: "manual_credit", Amount: "100.00", RefType: "customer", RefID: id, Channel: "admin_manual", RequestID: fmt.Sprintf("HTTP-%d", id), AdminID: 1, AdminName: "PRIVATE_OPERATOR", OperatorIP: "127.0.0.1", Notes: "PRIVATE_REASON", ClientNotes: "公开增加余额原因"})
	if e != nil {
		t.Fatal(e)
	}
	manager := auth.NewManager("ledger-http-fixture-access", "ledger-http-fixture-refresh", 3600, 3600)
	if e = manager.LoadSecurityConfig(ctx, database); e != nil {
		t.Fatal(e)
	}
	pair, e := manager.IssueDeviceSession(ctx, id, username, 1, "ledger-test", "127.0.0.1")
	if e != nil {
		t.Fatal(e)
	}
	h := &authHandler{customers: repository.NewRepository(database), jwt: manager}
	request := func(query string, authenticated bool) *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", "/api/v1/console/balance/transactions"+query, nil)
		if authenticated {
			r.Header.Set("Authorization", "Bearer "+pair.AccessToken)
		}
		w := httptest.NewRecorder()
		h.balanceStatement(w, r)
		return w
	}
	if w := request("", false); w.Code != 401 {
		t.Fatal("anonymous allowed", w.Code)
	}
	w := request("", true)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	var envelope struct{ Data balance.Statement }
	if e = json.Unmarshal(w.Body.Bytes(), &envelope); e != nil {
		t.Fatal(e)
	}
	if envelope.Data.Total != 1 || envelope.Data.Items[0].Description != "公开增加余额原因" {
		t.Fatal(w.Body.String())
	}
	for _, private := range []string{"PRIVATE_OPERATOR", "PRIVATE_REASON", "127.0.0.1", "request_hash", "admin_name", "operator_ip"} {
		if strings.Contains(w.Body.String(), private) {
			t.Fatal("private field exposed", private)
		}
	}
	for _, q := range []string{"?customer_id=1", "?userId=1", "?pageSize=101", "?snapshotVersion=99", "?type=unsupported", "?startAt=not-a-time"} {
		if w = request(q, true); w.Code != 400 {
			t.Fatal("bad query", q, w.Code)
		}
	}
	if w = request("?format=csv", true); w.Code != 200 || !strings.Contains(w.Body.String(), "公开增加余额原因") || !strings.HasPrefix(w.Header().Get("Content-Type"), "text/csv") {
		t.Fatal("csv", w.Code, w.Body.String())
	}
}
