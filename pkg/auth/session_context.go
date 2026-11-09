package auth

import "context"

// Session is the validated identity a request was resolved to by the cookie
// middleware. Downstream observers (audit, analytics) reuse it instead of
// validating the same token twice.
type Session struct {
	Kind   SessionKind
	Claims *Claims
}

type sessionContextKey struct{}

// WithSession stores the resolved session on the request context.
func WithSession(ctx context.Context, kind SessionKind, claims *Claims) context.Context {
	if claims == nil || kind == "" {
		return ctx
	}
	return context.WithValue(ctx, sessionContextKey{}, Session{Kind: kind, Claims: claims})
}

// SessionFrom returns the resolved session when the middleware validated one.
func SessionFrom(ctx context.Context) (SessionKind, *Claims, bool) {
	session, ok := ctx.Value(sessionContextKey{}).(Session)
	if !ok || session.Claims == nil || session.Kind == "" {
		return "", nil, false
	}
	return session.Kind, session.Claims, true
}
