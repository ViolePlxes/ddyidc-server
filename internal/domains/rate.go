package domains

import (
	"context"
	"errors"
)

var ErrRate = errors.New("域名请求较多，请稍后重试")

func (s *Service) call(ctx context.Context, cfg Settings, op OperationSpec, input map[string]string) (*ProviderResult, error) {
	window := s.now().Unix() / 60
	if e := s.db.WithContext(ctx).Exec("INSERT IGNORE INTO system.domain_rate_limits(bucket,window_start,used) VALUES('west',?,0)", window).Error; e != nil {
		return nil, e
	}
	update := s.db.WithContext(ctx).Exec("UPDATE system.domain_rate_limits SET used=IF(window_start=?,used+1,1),window_start=? WHERE bucket='west' AND (window_start<>? OR used<120)", window, window, window)
	if update.Error != nil {
		return nil, update.Error
	}
	if update.RowsAffected != 1 {
		return nil, ErrRate
	}
	return s.provider.Call(ctx, cfg, op, input)
}
