package payment

import (
	"context"
	dom "ddyidc/internal/payment/domain"
	"errors"
	"testing"
)

type refundRepo struct {
	fakeRepository
	refund              *dom.Refund
	prepares, completes int
}

func (r *refundRepo) PrepareRefund(_ context.Context, _, _ int64, amount, _, _ string) (*dom.Payment, *dom.Refund, error) {
	r.prepares++
	if amount != r.payment.Amount {
		return nil, nil, errors.New("amount mismatch")
	}
	return r.payment, r.refund, nil
}
func (r *refundRepo) CompleteRefund(context.Context, int64) (*dom.Refund, error) {
	r.completes++
	r.refund.Status = "refunded"
	return r.refund, nil
}

type refundChannel struct {
	fakeChannel
	calls int
	err   error
}

func (c *refundChannel) Refund(context.Context, *dom.Payment, *dom.Refund) error {
	c.calls++
	return c.err
}

func TestGatewayRefundBanHasNoSideEffects(t *testing.T) {
	for _, channel := range []string{"alipay", "wechat"} {
		r := &refundRepo{fakeRepository: fakeRepository{payment: &dom.Payment{Amount: "0.01", Channel: channel}}, refund: &dom.Refund{Status: "pending", RequestNo: "RF1"}}
		c := &refundChannel{}
		s := NewService(r, Config{}, map[string]Channel{channel: c}, nil)
		for i := 0; i < 2; i++ {
			result, err := s.Refund(context.Background(), 1, 1, "0.01", "test", "127.0.0.1")
			assertGatewayRefundBlocked(t, err)
			if result != nil {
				t.Fatal("blocked refund returned success")
			}
			assertGatewayRefundBlocked(t, s.RefundMarketplace(context.Background(), 1))
		}
		if r.prepares != 0 || r.completes != 0 || c.calls != 0 || r.refund.Status != "pending" {
			t.Fatal("blocked refund touched repository or provider")
		}
	}
}

func assertGatewayRefundBlocked(t *testing.T, err error) {
	t.Helper()
	var business *BusinessError
	if !errors.As(err, &business) || business.Code != 40007 {
		t.Fatalf("expected hard refund ban, got %v", err)
	}
}

func TestRealGatewayRefundBanDoesNotInitializeClients(t *testing.T) {
	// Nil SDK clients would panic if any real network path were reached.
	assertGatewayRefundBlocked(t, (&alipayChannel{}).Refund(context.Background(), nil, nil))
	assertGatewayRefundBlocked(t, (&alipayChannel{}).confirmRefund(context.Background(), nil, nil))
	assertGatewayRefundBlocked(t, (&wechatChannel{}).Refund(context.Background(), nil, nil))
	initialized := false
	deferred := &deferredWechatChannel{build: func(context.Context) (Channel, error) {
		initialized = true
		return nil, errors.New("must not initialize")
	}}
	assertGatewayRefundBlocked(t, deferred.Refund(context.Background(), nil, nil))
	if initialized {
		t.Fatal("refund initialized WeChat SDK")
	}
}
