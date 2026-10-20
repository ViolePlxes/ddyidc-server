package instance

import (
	"context"
	"ddyidc/internal/shared/renewalquote"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// An absent configuration preserves the five-minute security default. Invalid
// explicit values fail closed rather than silently extending checkout validity.
func quoteLifetime(raw string) (time.Duration, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return renewalquote.Lifetime, nil
	}
	seconds, err := strconv.Atoi(raw)
	if err != nil || seconds < 30 || seconds > 900 {
		return 0, fmt.Errorf("续费报价有效期配置无效")
	}
	return time.Duration(seconds) * time.Second, nil
}
func (s *Service) renewalQuoteLifetime(ctx context.Context) (time.Duration, error) {
	var value string
	if err := s.db.WithContext(ctx).Table("system.system_configs").Select("value").Where("`key`=?", "instance.renewal.quote_ttl_seconds").Scan(&value).Error; err != nil {
		return 0, err
	}
	return quoteLifetime(value)
}
