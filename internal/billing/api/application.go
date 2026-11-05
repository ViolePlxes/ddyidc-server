package billing

import (
	"context"
	dom "ddyidc/internal/billing/domain"
	svc "ddyidc/internal/billing/service"
	typ "ddyidc/internal/billing/types"
	"gorm.io/gorm"
)

type Filter = svc.Filter
type View = svc.View
type Detail = svc.Detail

// UserError is a reviewed public business rejection supplied by an adapter.
// Unexpected SQL/provider errors are never exposed to customers by this type.
type UserError struct{ Message string }

func (e *UserError) Error() string { return e.Message }

type PaymentAdapter struct {
	Checkout func(context.Context, int64, int64, string, string) (any, error)
	Balance  func(context.Context, int64, int64) (any, error)
}

// Application receives business adapters at the composition root. Billing never
// imports product implementations or owns an alternative balance writer.
type Application struct {
	db       *gorm.DB
	adapters map[string]PaymentAdapter
}

func NewApplication(db *gorm.DB, adapters map[string]PaymentAdapter) *Application {
	copy := make(map[string]PaymentAdapter, len(adapters))
	for kind, a := range adapters {
		copy[kind] = a
	}
	return &Application{db: db, adapters: copy}
}
func (a *Application) List(ctx context.Context, user int64, f Filter) ([]View, int64, error) {
	return svc.List(ctx, a.db, user, f)
}
func (a *Application) Detail(ctx context.Context, user int64, id string) (Detail, error) {
	return svc.Get(ctx, a.db, user, id)
}
func (a *Application) Pay(ctx context.Context, user int64, id, method, mode string) (any, error) {
	doc, e := svc.Resolve(ctx, a.db, user, id)
	if e != nil {
		return nil, e
	}
	return a.dispatch(typ.WithInvoice(ctx, id), user, doc, method, mode)
}
func (a *Application) dispatch(ctx context.Context, user int64, doc dom.Invoice, method, mode string) (any, error) {
	adapter, ok := a.adapters[doc.SourceType]
	if !ok {
		return nil, typ.ErrConflict
	}
	if method == "balance" {
		if doc.SourceType == "recharge" || adapter.Balance == nil {
			return nil, typ.ErrConflict
		}
		return adapter.Balance(ctx, user, doc.SourceID)
	}
	if (method != "alipay" && method != "wechat") || (mode != "page" && mode != "wap") || adapter.Checkout == nil {
		return nil, typ.ErrConflict
	}
	return adapter.Checkout(ctx, user, doc.SourceID, method, mode)
}
