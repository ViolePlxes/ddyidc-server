package payment

import (
	"context"
	"net/http"

	dom "ddyidc/internal/payment/domain"
)

type Checkout struct {
	CodeURL string
	Form    string
}

type Channel interface {
	Create(context.Context, *dom.Payment, string) (*Checkout, error)
	Close(context.Context, *dom.Payment) error
	ParseNotification(*http.Request) (dom.VerifiedNotification, error)
}
