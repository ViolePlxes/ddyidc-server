package instance

import (
	"context"
	"ddyidc/internal/customer/domain"
	dom "ddyidc/internal/instance/domain"
	"errors"
	"fmt"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"net/mail"
	"strings"
	"time"
)

type ProductionLockEmailVerifier interface {
	ConsumeProductionLockCode(context.Context, string, string, string) error
}
type ProductionLockEmailInput struct {
	Email string `json:"email"`
	Code  string `json:"code"`
}

type DestroyEmailInput struct {
	Email string `json:"email"`
	Code  string `json:"code"`
}
type ProductionLockEmailStatus struct {
	HasEmail  bool   `json:"hasEmail"`
	EmailHint string `json:"emailHint"`
}

func (s *Service) SetProductionLockEmailVerifier(v ProductionLockEmailVerifier) {
	s.lockEmailVerifier = v
}
func normalizeLockEmail(value string) (string, error) {
	value = strings.ToLower(strings.TrimSpace(value))
	a, e := mail.ParseAddress(value)
	if e != nil || a.Address != value || len(value) > 128 || !strings.Contains(value, ".") {
		return "", errors.New("请输入有效的邮箱地址")
	}
	return value, nil
}

// resolveVerificationEmail applies the mailbox rules shared by every action that
// consumes a production-lock email code: a bound mailbox must match, an unbound
// customer may bind one, and an administrator acting on a customer cannot bind.
func (s *Service) resolveVerificationEmail(ctx context.Context, tx *gorm.DB, h dom.Host, candidate string, locking bool) (string, uint64, bool, error) {
	var c customer.Customer
	q := tx.WithContext(ctx).Select("id,email,auth_version,status,deleted_at").Where("id=? AND status=1", h.CustomerID)
	if locking {
		q = q.Clauses(clause.Locking{Strength: "UPDATE"})
	}
	if e := q.Take(&c).Error; e != nil {
		return "", 0, false, ErrForbidden
	}
	current := ""
	if c.Email != nil {
		current = strings.TrimSpace(*c.Email)
	}
	binding := current == ""
	if binding {
		actor, _ := actionActor(ctx, h.CustomerID)
		if actor == 2 {
			return "", 0, false, errors.New("客户尚未绑定邮箱，请由客户本人完成绑定")
		}
		current = candidate
	}
	email, e := normalizeLockEmail(current)
	if e != nil {
		return "", 0, false, e
	}
	if !binding && candidate != "" && !strings.EqualFold(strings.TrimSpace(candidate), email) {
		return "", 0, false, errors.New("请使用账号当前绑定邮箱验证")
	}
	return email, c.AuthVersion, binding, nil
}

func (s *Service) lockEmailTarget(ctx context.Context, tx *gorm.DB, h dom.Host, enabled bool, candidate string, locking bool) (string, string, bool, error) {
	email, authVersion, binding, e := s.resolveVerificationEmail(ctx, tx, h, candidate, locking)
	if e != nil {
		return "", "", false, e
	}
	var lock ProductionLock
	if e := tx.Where("instance_id=?", h.ID).Take(&lock).Error; e != nil && !errors.Is(e, gorm.ErrRecordNotFound) {
		return "", "", false, e
	}
	actor, operator := actionActor(ctx, h.CustomerID)
	scope := fmt.Sprintf("%d:%d:%d:%d:%t:%t:%d:%d", h.CustomerID, h.ID, h.OwnershipVersion, authVersion, enabled, binding, actor, operator)
	scope += fmt.Sprintf(":%t:%d", lock.Enabled, lock.UpdatedAt.UnixMilli())
	return email, scope, binding, nil
}

// destroyEmailTarget keeps destruction in its own code scope, so a code sent to
// unlock a machine can never authorize destroying it.
func (s *Service) destroyEmailTarget(ctx context.Context, tx *gorm.DB, h dom.Host, candidate string, locking bool) (string, string, bool, error) {
	email, authVersion, binding, e := s.resolveVerificationEmail(ctx, tx, h, candidate, locking)
	if e != nil {
		return "", "", false, e
	}
	actor, operator := actionActor(ctx, h.CustomerID)
	scope := fmt.Sprintf("destroy:%d:%d:%d:%d:%t:%d:%d", h.CustomerID, h.ID, h.OwnershipVersion, authVersion, binding, actor, operator)
	return email, scope, binding, nil
}
func (s *Service) ProductionLockEmailStatus(ctx context.Context, customerID, id int64) (*ProductionLockEmailStatus, error) {
	if _, e := s.owned(ctx, customerID, id); e != nil {
		return nil, e
	}
	var c customer.Customer
	if e := s.db.WithContext(ctx).Select("id,email").First(&c, "id=?", customerID).Error; e != nil {
		return nil, e
	}
	out := &ProductionLockEmailStatus{}
	if c.Email != nil && strings.TrimSpace(*c.Email) != "" {
		out.HasEmail = true
		parts := strings.SplitN(strings.TrimSpace(*c.Email), "@", 2)
		if len(parts) == 2 && len(parts[0]) > 0 {
			out.EmailHint = string([]rune(parts[0])[:1]) + "***@" + parts[1]
		} else {
			out.EmailHint = "已绑定邮箱"
		}
	}
	return out, nil
}
func (s *Service) ProductionLockEmailTarget(ctx context.Context, customerID, id int64, enabled bool, email string) (string, string, error) {
	h, e := s.owned(ctx, customerID, id)
	if e != nil {
		return "", "", e
	}
	target, scope, _, e := s.lockEmailTarget(ctx, s.db, *h, enabled, email, false)
	return target, scope, e
}

// DestroyEmailStatus exposes the same bound-mailbox status as the production
// lock; destruction never invents a second verification channel.
func (s *Service) DestroyEmailStatus(ctx context.Context, customerID, id int64) (*ProductionLockEmailStatus, error) {
	return s.ProductionLockEmailStatus(ctx, customerID, id)
}

func (s *Service) DestroyEmailTarget(ctx context.Context, customerID, id int64, email string) (string, string, error) {
	h, e := s.owned(ctx, customerID, id)
	if e != nil {
		return "", "", e
	}
	target, scope, _, e := s.destroyEmailTarget(ctx, s.db, *h, email, false)
	return target, scope, e
}

// consumeVerificationEmailTx is the single one-time-credential path shared by
// the production lock and destruction: duplicate binding check, consume, bind.
func (s *Service) consumeVerificationEmailTx(ctx context.Context, tx *gorm.DB, h dom.Host, email, scope string, binding bool, code string) error {
	if binding {
		var n int64
		if e := tx.Table("customer.customers").Where("LOWER(email)=? AND id<>? AND deleted_at IS NULL", email, h.CustomerID).Count(&n).Error; e != nil {
			return e
		}
		if n > 0 {
			return errors.New("该邮箱已被其他账号绑定")
		}
	}
	if e := s.lockEmailVerifier.ConsumeProductionLockCode(ctx, email, scope, code); e != nil {
		return e
	}
	if binding {
		result := tx.Table("customer.customers").Where("id=? AND (email IS NULL OR TRIM(email)='')", h.CustomerID).Updates(map[string]any{"email": email, "updated_at": time.Now()})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return errors.New("邮箱状态已变更，请刷新后重试")
		}
	}
	return nil
}

func (s *Service) verifyProductionLockEmailTx(ctx context.Context, tx *gorm.DB, h dom.Host, enabled bool, in ProductionLockEmailInput) error {
	if s.lockEmailVerifier == nil {
		return errors.New("邮箱验证服务暂时不可用")
	}
	email, scope, binding, e := s.lockEmailTarget(ctx, tx, h, enabled, in.Email, true)
	if e != nil {
		return e
	}
	return s.consumeVerificationEmailTx(ctx, tx, h, email, scope, binding, in.Code)
}

func (s *Service) verifyDestroyEmailTx(ctx context.Context, tx *gorm.DB, h dom.Host, in DestroyEmailInput) error {
	if s.lockEmailVerifier == nil {
		return errors.New("邮箱验证服务暂时不可用")
	}
	email, scope, binding, e := s.destroyEmailTarget(ctx, tx, h, in.Email, true)
	if e != nil {
		return e
	}
	return s.consumeVerificationEmailTx(ctx, tx, h, email, scope, binding, in.Code)
}
