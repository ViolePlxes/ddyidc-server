package payment

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	dom "ddyidc/internal/payment/domain"
	rep "ddyidc/internal/payment/repository"
)

type fakeRepository struct {
	payment       *dom.Payment
	prepared      *rep.Prepared
	completeCalls int
}

func (f *fakeRepository) PrepareOrder(context.Context, int64, int64, string, time.Time) (*rep.Prepared, error) {
	return f.prepared, nil
}
func (f *fakeRepository) PrepareRecharge(context.Context, int64, int64, string, time.Time) (*rep.Prepared, error) {
	return f.prepared, nil
}
func (f *fakeRepository) PrepareMarketplace(context.Context, int64, int64, string, time.Time) (*rep.Prepared, error) {
	return f.prepared, nil
}
func (f *fakeRepository) FindByOutTradeNo(context.Context, string) (*dom.Payment, error) {
	return f.payment, nil
}
func (f *fakeRepository) FindByPaymentNo(context.Context, string) (*dom.Payment, error) {
	return f.payment, nil
}
func (f *fakeRepository) FindByBiz(context.Context, int64, dom.BizType, int64) (*dom.Payment, error) {
	return f.payment, nil
}
func (f *fakeRepository) Complete(_ context.Context, notification dom.VerifiedNotification) (*dom.Payment, bool, error) {
	f.completeCalls++
	if f.payment.Status == dom.StatusPaid {
		return f.payment, false, nil
	}
	if f.payment.Status != dom.StatusPending {
		return f.payment, false, rep.ErrInvalidState
	}
	now := notification.PaidAt
	f.payment.Status = dom.StatusPaid
	f.payment.PaidAt = &now
	f.payment.TransactionID = notification.TransactionID
	return f.payment, true, nil
}
func (f *fakeRepository) Close(context.Context, int64, dom.BizType, int64) (*dom.Payment, error) {
	f.payment.Status = dom.StatusClosed
	return f.payment, nil
}

type fakeChannel struct {
	notification dom.VerifiedNotification
	parseErr     error
	created      *dom.Payment
}

type fakeQueryableChannel struct {
	fakeChannel
	query PlatformQuery
	err   error
}

func (f *fakeQueryableChannel) Query(context.Context, string) (PlatformQuery, error) {
	return f.query, f.err
}

func (f *fakeChannel) Create(_ context.Context, payment *dom.Payment, _, _ string) (*Checkout, error) {
	f.created = payment
	return &Checkout{CodeURL: "weixin://wxpay/test"}, nil
}
func (f *fakeChannel) Close(context.Context, *dom.Payment) error { return nil }
func (f *fakeChannel) ParseNotification(*http.Request) (dom.VerifiedNotification, error) {
	return f.notification, f.parseErr
}

type fakeDispatcher struct{ calls int }

func (f *fakeDispatcher) Dispatch(*dom.Payment) { f.calls++ }

func pendingPayment() *dom.Payment {
	return &dom.Payment{
		ID: 1, BizType: dom.BizOrder, BizID: 8, CustomerID: 9,
		Channel: "wechat", OutTradeNo: "DDY-1", Amount: "12.34", AmountFen: 1234,
		Currency: "CNY", Status: dom.StatusPending, ExpireAt: time.Now().Add(time.Minute),
	}
}

func TestNotificationSignatureFailureLeavesStateUnchanged(t *testing.T) {
	payment := pendingPayment()
	repository := &fakeRepository{payment: payment}
	channel := &fakeChannel{parseErr: errors.New("invalid signature")}
	service := NewService(repository, Config{}, map[string]Channel{"wechat": channel}, &fakeDispatcher{})

	err := service.HandleNotification("wechat", httptest.NewRequest(http.MethodPost, "/notify", nil))
	if err == nil {
		t.Fatal("expected signature rejection")
	}
	if payment.Status != dom.StatusPending || repository.completeCalls != 0 {
		t.Fatalf("state changed after invalid signature: status=%s complete_calls=%d", payment.Status, repository.completeCalls)
	}
}

func TestNotificationAmountMismatchLeavesStateUnchanged(t *testing.T) {
	payment := pendingPayment()
	repository := &fakeRepository{payment: payment}
	channel := &fakeChannel{notification: dom.VerifiedNotification{
		Channel: "wechat", OutTradeNo: payment.OutTradeNo, TransactionID: "WX-1", AmountFen: 1235, Currency: "CNY",
	}}
	service := NewService(repository, Config{}, map[string]Channel{"wechat": channel}, &fakeDispatcher{})

	err := service.HandleNotification("wechat", httptest.NewRequest(http.MethodPost, "/notify", nil))
	if err == nil || err.Error() != "支付金额不一致" {
		t.Fatalf("expected amount mismatch, got %v", err)
	}
	if payment.Status != dom.StatusPending || repository.completeCalls != 0 {
		t.Fatalf("state changed after amount mismatch: status=%s complete_calls=%d", payment.Status, repository.completeCalls)
	}
}

func TestDuplicateNotificationTransitionsAndDispatchesOnce(t *testing.T) {
	payment := pendingPayment()
	repository := &fakeRepository{payment: payment}
	dispatcher := &fakeDispatcher{}
	channel := &fakeChannel{notification: dom.VerifiedNotification{
		Channel: "wechat", OutTradeNo: payment.OutTradeNo, TransactionID: "WX-1", AmountFen: 1234, Currency: "CNY", PaidAt: time.Now(),
	}}
	service := NewService(repository, Config{}, map[string]Channel{"wechat": channel}, dispatcher)
	request := func() *http.Request { return httptest.NewRequest(http.MethodPost, "/notify", nil) }

	if err := service.HandleNotification("wechat", request()); err != nil {
		t.Fatal(err)
	}
	if err := service.HandleNotification("wechat", request()); err != nil {
		t.Fatal(err)
	}
	if payment.Status != dom.StatusPaid || repository.completeCalls != 2 || dispatcher.calls != 1 {
		t.Fatalf("unexpected idempotency result: status=%s completes=%d dispatches=%d", payment.Status, repository.completeCalls, dispatcher.calls)
	}
}

func TestCreateOrderUsesRepositoryServerAmount(t *testing.T) {
	payment := pendingPayment()
	repository := &fakeRepository{payment: payment, prepared: &rep.Prepared{Payment: payment, Subject: "云服务器"}}
	channel := &fakeChannel{}
	service := NewService(repository, Config{ExpireMinutes: 15}, map[string]Channel{"wechat": channel}, nil)

	response, err := service.CreateOrder(context.Background(), payment.CustomerID, payment.BizID, "wechat", "page")
	if err != nil {
		t.Fatal(err)
	}
	if channel.created == nil || channel.created.AmountFen != 1234 || response.Wechat == nil {
		t.Fatalf("channel did not receive server-priced payment: %#v", channel.created)
	}
}

func TestParseYuanToCentsExact(t *testing.T) {
	tests := map[string]int64{"0.01": 1, "12": 1200, "12.3": 1230, "999999.99": 99999999}
	for input, expected := range tests {
		actual, err := parseYuanToCents(input)
		if err != nil || actual != expected {
			t.Fatalf("parseYuanToCents(%q)=%d,%v want %d", input, actual, err, expected)
		}
	}
	for _, input := range []string{"-1", "1.001", "abc", ""} {
		if _, err := parseYuanToCents(input); err == nil {
			t.Fatalf("expected invalid amount %q to fail", input)
		}
	}
}

func TestAdminQueryReconcilesVerifiedPaidTradeOnce(t *testing.T) {
	payment := pendingPayment()
	payment.PaymentNo = "PAY-QUERY"
	payment.OutTradeNo = payment.PaymentNo
	payment.Channel = "alipay"
	repository := &fakeRepository{payment: payment}
	dispatcher := &fakeDispatcher{}
	channel := &fakeQueryableChannel{query: PlatformQuery{
		Found: true, OutTradeNo: payment.OutTradeNo, TransactionID: "ALI-QUERY", Status: "TRADE_SUCCESS",
		AmountFen: payment.AmountFen, Currency: payment.Currency, PaidAt: time.Now(),
	}}
	service := NewService(repository, Config{}, map[string]Channel{"alipay": channel}, dispatcher)

	first, err := service.QueryAlipay(context.Background(), payment.PaymentNo)
	if err != nil {
		t.Fatal(err)
	}
	second, err := service.QueryAlipay(context.Background(), payment.PaymentNo)
	if err != nil {
		t.Fatal(err)
	}
	if !first.Connected || !first.Found || !first.Reconciled || second.Reconciled || dispatcher.calls != 1 || payment.Status != dom.StatusPaid {
		t.Fatalf("unexpected query reconciliation: first=%#v second=%#v dispatches=%d status=%s", first, second, dispatcher.calls, payment.Status)
	}
}

func TestStatusByPayNoRejectsOtherCustomer(t *testing.T) {
	payment := pendingPayment()
	payment.PaymentNo = "PAY-OWNER"
	service := NewService(&fakeRepository{payment: payment}, Config{}, nil, nil)
	_, err := service.StatusByPayNo(context.Background(), payment.CustomerID+1, payment.PaymentNo)
	var businessErr *BusinessError
	if !errors.As(err, &businessErr) || businessErr.Code != 40002 {
		t.Fatalf("expected ownership rejection, got %v", err)
	}
}

func TestWechatQueryReconciliationBoundaries(t *testing.T) {
	for _, name := range []string{"success", "wrong amount", "wrong currency", "wrong trade", "not paid", "wrong channel"} {
		t.Run(name, func(t *testing.T) {
			p := pendingPayment()
			p.PaymentNo, p.OutTradeNo, p.Channel = "WX-QUERY", "WX-QUERY", "wechat"
			q := PlatformQuery{Found: true, OutTradeNo: p.OutTradeNo, TransactionID: "WX-TX", Status: "SUCCESS", AmountFen: p.AmountFen, Currency: p.Currency, PaidAt: time.Now()}
			switch name {
			case "wrong amount":
				q.AmountFen++
			case "wrong currency":
				q.Currency = "USD"
			case "wrong trade":
				q.OutTradeNo = "other"
			case "not paid":
				q.Status = "NOTPAY"
			case "wrong channel":
				p.Channel = "alipay"
			}
			dispatcher := &fakeDispatcher{}
			svc := NewService(&fakeRepository{payment: p}, Config{}, map[string]Channel{"wechat": &fakeQueryableChannel{query: q}}, dispatcher)
			_, err := svc.QueryWechat(context.Background(), p.PaymentNo)
			if name == "success" {
				if err != nil || p.Status != dom.StatusPaid || dispatcher.calls != 1 {
					t.Fatal("verified trade not reconciled")
				}
				if _, err = svc.QueryWechat(context.Background(), p.PaymentNo); err != nil || dispatcher.calls != 1 {
					t.Fatal("repeat query not idempotent")
				}
			} else {
				if p.Status != dom.StatusPending || dispatcher.calls != 0 {
					t.Fatal("unverified trade changed payment")
				}
				if name != "not paid" && err == nil {
					t.Fatal("mismatched trade accepted")
				}
			}
		})
	}
}
