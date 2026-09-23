package auth

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
)

// BeginImpersonationAudit persists actor and subject before the delegated
// request executes. No token, request body or query string is retained.
func (m *Manager) BeginImpersonationAudit(ctx context.Context, claims *Claims, requestID, method, path string) (func(int) error, error) {
	if claims == nil || claims.ActorID == 0 {
		return func(int) error { return nil }, nil
	}
	if m.accounts == nil || len(path) > 2048 || len(method) > 16 {
		return nil, errors.New("delegation audit unavailable")
	}
	if _, err := uuid.Parse(requestID); err != nil {
		requestID = uuid.NewString()
	}
	now := time.Now()
	err := m.accounts.WithContext(ctx).Table("system.impersonation_request_audits").Create(map[string]any{
		"request_id": requestID, "actor_admin_id": claims.ActorID, "subject_customer_id": claims.UserID,
		"method": method, "path": path, "http_status": 0, "started_at": now,
	}).Error
	if err != nil {
		return nil, errors.New("delegation audit unavailable")
	}
	return func(status int) error {
		finishCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
		defer cancel()
		return m.accounts.WithContext(finishCtx).Table("system.impersonation_request_audits").Where("request_id=? AND finished_at IS NULL", requestID).
			Updates(map[string]any{"http_status": status, "finished_at": time.Now()}).Error
	}, nil
}
