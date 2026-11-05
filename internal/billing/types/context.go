package billing

import "context"

type invoiceContextKey struct{}

// WithInvoice pins a checkout to the document the customer actually reviewed.
// Repository checks run inside the original business transaction, not just HTTP.
func WithInvoice(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, invoiceContextKey{}, id)
}
func ExpectedInvoice(ctx context.Context) string {
	value, _ := ctx.Value(invoiceContextKey{}).(string)
	return value
}
