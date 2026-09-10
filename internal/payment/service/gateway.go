package payment

import (
	"context"
	"net/http"
	"time"

	dom "ddyidc/internal/payment/domain"
)

type Checkout struct {
	CodeURL string
	Form    string
	PayURL  string
	Mode    string
}

type Channel interface {
	Create(context.Context, *dom.Payment, string, string) (*Checkout, error)
	Close(context.Context, *dom.Payment) error
	ParseNotification(*http.Request) (dom.VerifiedNotification, error)
}

type PlatformQuery struct {
	Found         bool
	OutTradeNo    string
	TransactionID string
	Status        string
	AmountFen     int64
	Currency      string
	PaidAt        time.Time
}

type QueryableChannel interface {
	Query(context.Context, string) (PlatformQuery, error)
}

type PlatformQueryError struct {
	Code    string
	SubCode string
}

func (e *PlatformQueryError) Error() string { return "支付平台查询失败" }
