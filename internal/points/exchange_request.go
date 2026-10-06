package points

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"regexp"
)

var exchangeRequestPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{16,80}$`)

func (s *Service) ExchangeRequest(ctx context.Context, user, goods int64, quantity int, recipient json.RawMessage, request string) (*Order, error) {
	if !exchangeRequestPattern.MatchString(request) {
		return nil, errors.New("兑换请求标识无效，请刷新页面重试")
	}
	var data any
	if json.Unmarshal(recipient, &data) != nil {
		return nil, errors.New("收货信息格式无效")
	}
	canonical, _ := json.Marshal(data)
	fingerprint := fmt.Sprintf("%x", sha256.Sum256([]byte(fmt.Sprintf("%d:%d:%s", goods, quantity, canonical))))
	var out *Order
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := s.ensureAccountTx(ctx, tx, user); err != nil {
			return err
		}
		var account Account
		if err := tx.Table(accountsTable).Clauses(clause.Locking{Strength: "UPDATE"}).Where("user_id=?", user).Take(&account).Error; err != nil {
			return err
		}
		var old struct {
			Fingerprint string
			OrderID     *int64
		}
		err := tx.Table("system.points_exchange_requests").Where("user_id=? AND request_id=?", user, request).Take(&old).Error
		if err == nil {
			if old.Fingerprint != fingerprint || old.OrderID == nil {
				return errors.New("兑换请求标识已用于其他内容")
			}
			worker := NewService(tx, s.redis)
			out, err = worker.Order(ctx, user, *old.OrderID, false)
			return err
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		worker := NewService(tx, s.redis)
		worker.orders = s.orders
		worker.now = s.now
		out, err = worker.Exchange(ctx, user, goods, quantity, recipient)
		if err != nil {
			return err
		}
		return tx.Table("system.points_exchange_requests").Create(map[string]any{"user_id": user, "request_id": request, "fingerprint": fingerprint, "order_id": out.ID}).Error
	})
	return out, err
}
