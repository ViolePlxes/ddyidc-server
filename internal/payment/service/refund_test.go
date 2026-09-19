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

func TestRefundValidatesAndPersistsOnlyConfirmedResult(t *testing.T) {
	r := &refundRepo{fakeRepository: fakeRepository{payment: &dom.Payment{Amount: "0.01", Channel: "alipay"}}, refund: &dom.Refund{Status: "pending", RequestNo: "RF1"}}
	c := &refundChannel{err: errors.New("timeout")}
	s := NewService(r, Config{}, map[string]Channel{"alipay": c}, nil)
	for _, amount := range []string{"", "0", "-0.01", "0.001", "1e2", "0.02"} {
		if _, err := s.Refund(context.Background(), 1, 1, amount, "test", "127.0.0.1"); err == nil {
			t.Fatalf("accepted %q", amount)
		}
	}
	if c.calls != 0 {
		t.Fatal("invalid input reached provider")
	}
	if _, err := s.Refund(context.Background(), 1, 0, "0.01", "test", "127.0.0.1"); err == nil {
		t.Fatal("missing admin accepted")
	}
	if _, err := s.Refund(context.Background(), 1, 1, "0.01", "test", "127.0.0.1"); err == nil {
		t.Fatal("timeout succeeded")
	}
	if r.completes != 0 || r.refund.Status != "pending" {
		t.Fatal("uncertain refund finalized")
	}
	c.err = nil
	if _, err := s.Refund(context.Background(), 1, 1, "0.01", "test", "127.0.0.1"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Refund(context.Background(), 1, 1, "0.01", "test", "127.0.0.1"); err != nil {
		t.Fatal(err)
	}
	if c.calls != 2 || r.completes != 1 || r.refund.RequestNo != "RF1" {
		t.Fatal("duplicate refund or changed idempotency key")
	}
}
