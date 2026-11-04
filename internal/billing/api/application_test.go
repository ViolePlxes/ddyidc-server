package billing

import (
	"context"
	dom "ddyidc/internal/billing/domain"
	typ "ddyidc/internal/billing/types"
	"errors"
	"testing"
)

func TestInvoiceAdapterDispatch(t *testing.T) {
	called := 0
	adapter := PaymentAdapter{
		Checkout: func(ctx context.Context, user, id int64, method, mode string) (any, error) {
			called++
			if typ.ExpectedInvoice(ctx) != "invoice-1" || user != 17 || id != 23 || method != "alipay" || mode != "page" {
				t.Fatal("invoice identity lost at adapter boundary")
			}
			return "gateway", nil
		},
		Balance: func(ctx context.Context, user, id int64) (any, error) { called++; return "balance", nil },
	}
	a := NewApplication(nil, map[string]PaymentAdapter{"order": adapter, "recharge": adapter})
	ctx := typ.WithInvoice(context.Background(), "invoice-1")
	doc := dom.Invoice{ID: "invoice-1", SourceType: "order", SourceID: 23}
	out, e := a.dispatch(ctx, 17, doc, "alipay", "page")
	if e != nil || out != "gateway" || called != 1 {
		t.Fatal(out, e, called)
	}
	if _, e = a.dispatch(ctx, 17, doc, "cash", "page"); !errors.Is(e, typ.ErrConflict) {
		t.Fatal("unregistered payment accepted")
	}
	if _, e = a.dispatch(ctx, 17, doc, "alipay", "native"); !errors.Is(e, typ.ErrConflict) {
		t.Fatal("invalid gateway mode accepted")
	}
	doc.SourceType = "recharge"
	if _, e = a.dispatch(ctx, 17, doc, "balance", ""); !errors.Is(e, typ.ErrConflict) {
		t.Fatal("balance self-recharge accepted")
	}
	doc.SourceType = "unregistered"
	if _, e = a.dispatch(ctx, 17, doc, "alipay", "page"); !errors.Is(e, typ.ErrConflict) {
		t.Fatal("unregistered business accepted")
	}
	if called != 1 {
		t.Fatal("rejected request reached payment adapter")
	}
}
