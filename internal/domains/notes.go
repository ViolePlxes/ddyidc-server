package domains

import (
	"context"
	"errors"
	"strings"
	"time"

	"ddyidc/internal/shared/snowflake"
	"gorm.io/gorm/clause"
)

// DNSNote stores a platform-only note for one upstream DNS record.
type DNSNote struct {
	ID         int64     `json:"-" gorm:"column:id;primaryKey"`
	CustomerID int64     `json:"-" gorm:"column:customer_id"`
	Domain     string    `json:"domain" gorm:"column:domain"`
	RecordID   string    `json:"recordId" gorm:"column:record_id"`
	Remark     string    `json:"remark" gorm:"column:remark"`
	CreatedAt  time.Time `json:"-" gorm:"column:created_at"`
	UpdatedAt  time.Time `json:"updatedAt" gorm:"column:updated_at"`
}

func (DNSNote) TableName() string { return "system.domain_dns_notes" }

func (s *Service) DNSNotes(ctx context.Context, user int64, domain string) (map[string]string, error) {
	domain = strings.ToLower(strings.TrimSpace(domain))
	var asset Asset
	if e := s.db.WithContext(ctx).Take(&asset, "customer_id=? AND domain=?", user, domain).Error; e != nil {
		return nil, ErrOwnership
	}
	rows := []DNSNote{}
	if e := s.db.WithContext(ctx).Where("customer_id=? AND domain=?", user, domain).Find(&rows).Error; e != nil {
		return nil, e
	}
	result := make(map[string]string, len(rows))
	for _, row := range rows {
		result[row.RecordID] = row.Remark
	}
	return result, nil
}

func (s *Service) SetDNSNote(ctx context.Context, user int64, domain, recordID, remark string) error {
	domain = strings.ToLower(strings.TrimSpace(domain))
	recordID = strings.TrimSpace(recordID)
	if recordID == "" || len(recordID) > 64 || len(remark) > 255 {
		return errors.New("备注内容无效")
	}
	var asset Asset
	if e := s.db.WithContext(ctx).Take(&asset, "customer_id=? AND domain=?", user, domain).Error; e != nil {
		return ErrOwnership
	}
	now := s.now()
	row := DNSNote{ID: snowflake.GenID(), CustomerID: user, Domain: domain, RecordID: recordID, Remark: remark, CreatedAt: now, UpdatedAt: now}
	return s.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "domain"}, {Name: "record_id"}},
		DoUpdates: clause.Assignments(map[string]any{"remark": remark, "updated_at": now}),
	}).Create(&row).Error
}
