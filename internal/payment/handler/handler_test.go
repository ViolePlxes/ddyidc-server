package payment

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	dom "ddyidc/internal/payment/domain"
	rep "ddyidc/internal/payment/repository"
	svc "ddyidc/internal/payment/service"
)

type notifyRepository struct{ payment *dom.Payment }

func (r *notifyRepository) PrepareOrder(context.Context, int64, int64, string, time.Time) (*rep.Prepared, error) {
	return nil, errors.New("unused")
}
func (r *notifyRepository) PrepareRecharge(context.Context, int64, int64, string, time.Time) (*rep.Prepared, error) {
	return nil, errors.New("unused")
}
func (r *notifyRepository) FindByOutTradeNo(context.Context, string) (*dom.Payment, error) {
	return r.payment, nil
}
func (r *notifyRepository) FindByPaymentNo(context.Context, string) (*dom.Payment, error) {
	return r.payment, nil
}
func (r *notifyRepository) FindByBiz(context.Context, int64, dom.BizType, int64) (*dom.Payment, error) {
	return r.payment, nil
}
func (r *notifyRepository) Complete(context.Context, dom.VerifiedNotification) (*dom.Payment, bool, error) {
	return r.payment, true, nil
}
func (r *notifyRepository) Close(context.Context, int64, dom.BizType, int64) (*dom.Payment, error) {
	return r.payment, nil
}

type notifyChannel struct{ err error }

func (c *notifyChannel) Create(context.Context, *dom.Payment, string, string) (*svc.Checkout, error) {
	return nil, errors.New("unused")
}
func (c *notifyChannel) Close(context.Context, *dom.Payment) error { return nil }
func (c *notifyChannel) ParseNotification(*http.Request) (dom.VerifiedNotification, error) {
	return dom.VerifiedNotification{}, c.err
}

func TestAlipayNotifyReturnsPlainFailureWhenVerificationFails(t *testing.T) {
	service := svc.NewService(&notifyRepository{}, svc.Config{}, map[string]svc.Channel{"alipay": &notifyChannel{err: errors.New("bad signature")}}, nil)
	h := &handler{service: service}
	request := httptest.NewRequest(http.MethodPost, "/api/v1/payment/alipay/notify", strings.NewReader("sign=invalid"))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response := httptest.NewRecorder()

	h.alipayNotify(response, request)

	if strings.TrimSpace(response.Body.String()) != "failure" {
		t.Fatalf("unexpected callback response %q", response.Body.String())
	}
}
