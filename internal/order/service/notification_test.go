package order

import (
	"context"
	"testing"
	"time"

	instancedom "ddyidc/internal/instance/domain"
	dom "ddyidc/internal/order/domain"
)

type notificationCall struct {
	scene, businessID string
	customerID        int64
	params            map[string]string
}

type recordingNotifier struct{ calls []notificationCall }

func (r *recordingNotifier) NotifyCustomer(_ context.Context, scene string, customerID int64, businessID string, params map[string]string) error {
	r.calls = append(r.calls, notificationCall{scene: scene, customerID: customerID, businessID: businessID, params: params})
	return nil
}

func TestBusinessNotificationParameters(t *testing.T) {
	recorder := &recordingNotifier{}
	service := &Service{notifier: recorder}
	orderRow := &dom.Order{ID: 101, OrderNo: "DDY101", CustomerID: 7, PayAmount: 1299}
	item := &dom.OrderItem{ProductName: "云主机"}
	service.notifyOrderCreated(context.Background(), orderRow, item)

	expires := time.Date(2026, 9, 20, 12, 0, 0, 0, time.Local)
	host := &instancedom.Host{ID: 202, InstanceNo: "INS202", ProductName: "云主机", ExpiresAt: &expires}
	if err := service.notifyActivation(context.Background(), orderRow, host); err != nil {
		t.Fatal(err)
	}
	if err := service.notifyRecharge(context.Background(), 303, "RC303", 7, 5000); err != nil {
		t.Fatal(err)
	}

	if len(recorder.calls) != 3 {
		t.Fatalf("notification calls = %d", len(recorder.calls))
	}
	if got := recorder.calls[0]; got.scene != "order" || got.customerID != 7 || got.businessID != "101" || got.params["order_no"] != "DDY101" || got.params["amount"] != "¥12.99" {
		t.Fatalf("order notification = %#v", got)
	}
	if got := recorder.calls[1]; got.scene != "activation" || got.businessID != "202" || got.params["instance_no"] != "INS202" || got.params["expires_at"] != "2026-09-20 12:00" {
		t.Fatalf("activation notification = %#v", got)
	}
	if got := recorder.calls[2]; got.scene != "recharge" || got.businessID != "303" || got.params["recharge_no"] != "RC303" || got.params["amount"] != "¥50.00" {
		t.Fatalf("recharge notification = %#v", got)
	}
}
