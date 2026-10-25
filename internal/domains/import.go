package domains

import (
	"context"
	"ddyidc/internal/shared/snowflake"
	"errors"
	"fmt"
	"gorm.io/gorm"
	"strings"
)

// Existing registrar assets require an explicit audited administrator binding;
// querying the reseller's global domain list never grants a customer ownership.
func (s *Service) Import(ctx context.Context, admin, user int64, raw, notes, ip string) (*Asset, error) {
	if admin <= 0 || user <= 0 || len(strings.TrimSpace(notes)) < 2 || len(notes) > 500 {
		return nil, errors.New("导入参数无效")
	}
	domain, e := normalizeDomain(raw)
	if e != nil {
		return nil, e
	}
	cfg, e := s.ready(ctx)
	if e != nil {
		return nil, e
	}
	info, e := s.providerData(ctx, cfg, "4.8", map[string]string{"domain": domain})
	if e != nil {
		return nil, e
	}
	if stringValue(info["domain"]) != domain {
		return nil, ErrOwnership
	}
	expiry, e := domainTime(stringValue(info["rexpiredate"]))
	if e != nil {
		return nil, e
	}
	asset := Asset{ID: snowflake.GenID(), CustomerID: user, Domain: domain, Status: "active", ExpiresAt: &expiry, CreatedAt: s.now(), UpdatedAt: s.now()}
	e = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var count int64
		if e := tx.Table("customer.customers").Where("id=? AND status=1", user).Count(&count).Error; e != nil {
			return e
		}
		if count != 1 {
			return ErrOwnership
		}
		if e := tx.Create(&asset).Error; e != nil {
			return errors.New("该域名已有业务记录，导入未执行")
		}
		return tx.Table("system.domain_admin_audits").Create(map[string]any{"id": snowflake.GenID(), "admin_id": admin, "operator_ip": ip, "notes": fmt.Sprintf("domain=%s customer_id=%d; %s", domain, user, string([]rune(notes)[:min(len([]rune(notes)), 150)])), "action": "import_domain", "created_at": s.now()}).Error
	})
	return &asset, e
}
