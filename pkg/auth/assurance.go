package auth

import "context"

// SessionAssurance is supplied only by the authentication boundary after the
// selected factor has passed. Policies remain injected, independent of roles.
type SessionAssurance struct {
	PolicyVersion int64
	FactorVersion int64
	Verified      bool
}
type sessionAssuranceKey struct{}

func WithSessionAssurance(ctx context.Context, value SessionAssurance) context.Context {
	return context.WithValue(ctx, sessionAssuranceKey{}, value)
}
func (m *Manager) SetSessionValidator(validate func(context.Context, SessionKind, *Claims) error) {
	m.sessionValidator = validate
}
