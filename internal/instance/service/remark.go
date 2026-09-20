package instance

import (
	"context"
	dom "ddyidc/internal/instance/domain"
	"errors"
	"fmt"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"strings"
	"time"
	"unicode/utf8"
)

var ErrInvalidRemark = errors.New("备注参数无效")

type RemarkInput struct {
	Remark *string `json:"remark"`
}

func remarkValue(in RemarkInput) (string, error) {
	if in.Remark == nil {
		return "", fmt.Errorf("%w: 请提供备注字段", ErrInvalidRemark)
	}
	if !utf8.ValidString(*in.Remark) || utf8.RuneCountInString(*in.Remark) > 1000 || strings.ContainsRune(*in.Remark, 0) {
		return "", fmt.Errorf("%w: 备注最多 1000 字，且不能包含无效字符", ErrInvalidRemark)
	}
	return strings.TrimSpace(*in.Remark), nil
}

// Remarks are local customer-visible labels. Upstream refreshes must not overwrite them.
func (s *Service) SaveRemark(ctx context.Context, customerID, id int64, in RemarkInput) error {
	value, err := remarkValue(in)
	if err != nil {
		return err
	}
	previous, err := s.owned(ctx, customerID, id)
	if err != nil {
		return err
	}
	if s.db == nil {
		return ErrUnsupported
	}
	actor, operator := actionActor(ctx, customerID)
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var current dom.Host
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=? AND customer_id=?", id, customerID).Take(&current).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrNotFound
			}
			return err
		}
		if current.OwnershipVersion != previous.OwnershipVersion {
			return ErrForbidden
		}
		now := time.Now()
		if err := tx.Model(&current).Updates(map[string]any{"remark": value, "updated_at": now}).Error; err != nil {
			return err
		}
		return tx.Create(&dom.Event{InstanceID: id, EventType: "remark", OperatorType: actor, OperatorID: &operator, Status: 2, UpstreamRequest: `{}`, UpstreamResponse: `{"saved":true}`, CreatedAt: now, UpdatedAt: now, ProcessedAt: &now}).Error
	})
}
