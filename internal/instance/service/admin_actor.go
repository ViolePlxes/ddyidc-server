package instance

import "context"

type adminActorKey struct{}

// WithAdminActor is used only after the HTTP handler authenticates an admin.
// Customer ownership is still checked independently by every service method.
func WithAdminActor(ctx context.Context, adminID int64) context.Context {
	return context.WithValue(ctx, adminActorKey{}, adminID)
}
func actionActor(ctx context.Context, customerID int64) (uint8, int64) {
	if id, ok := ctx.Value(adminActorKey{}).(int64); ok && id > 0 {
		return 2, id
	}
	return 1, customerID
}
