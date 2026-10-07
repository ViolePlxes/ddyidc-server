package security

import (
	"context"
	"gorm.io/gorm"
)

// VerifyAdminAction allows sibling security modules to use the same factor
// policy in their own transaction without exposing TOTP secrets.
func (s *Service) VerifyAdminAction(tx *gorm.DB, ctx context.Context, subject Subject, code string) error {
	c, e := s.Config(ctx)
	if e != nil {
		return e
	}
	f, e := s.Factor(ctx, subject)
	if e != nil {
		return e
	}
	if c.MFAEnabled || f.Enabled {
		return s.verifyTx(tx, subject, code)
	}
	return nil
}
