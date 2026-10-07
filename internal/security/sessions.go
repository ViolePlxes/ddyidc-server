package security

import (
	"context"
	"ddyidc/pkg/auth"
	"errors"
)

// ValidateSession invalidates pre-MFA sessions, rotated access policies, changed
// factors and delegated sessions whose administrator no longer has assurance.
func (s *Service) ValidateSession(ctx context.Context, kind auth.SessionKind, claims *auth.Claims) error {
	if claims == nil {
		return errors.New("missing session")
	}
	subjectID := claims.UserID
	policyVersion, factorVersion, verified := claims.SecurityVersion, claims.FactorVersion, claims.MFAVerified
	if kind != auth.SessionAdmin {
		if claims.ActorID == 0 {
			return nil
		}
		subjectID = claims.ActorID
		policyVersion, factorVersion, verified = claims.ActorSecurityVersion, claims.ActorFactorVersion, claims.ActorMFAVerified
	}
	c, e := s.Config(ctx)
	if e != nil {
		return e
	}
	if policyVersion != c.Version {
		return errors.New("administrative security policy changed")
	}
	f, e := s.Factor(ctx, Subject{"admin", subjectID})
	if e != nil {
		return e
	}
	if factorVersion != f.Version {
		return errors.New("authentication factor changed")
	}
	if c.MFAEnabled && (!verified || !f.Enabled) {
		return errors.New("second factor required")
	}
	return nil
}
