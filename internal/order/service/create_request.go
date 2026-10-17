package order

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	dom "ddyidc/internal/order/domain"
	"encoding/hex"
	"encoding/json"
	"errors"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"regexp"
)

type createRequest struct {
	CustomerID  int64  `gorm:"primaryKey"`
	RequestID   string `gorm:"primaryKey"`
	Fingerprint string
	OrderID     int64
}

func (createRequest) TableName() string { return "order.create_requests" }

var createRequestID = regexp.MustCompile(`^[A-Za-z0-9_-]{16,64}$`)

func (s *Service) requestFingerprint(in *CreateOrderInput) (string, error) {
	if in == nil {
		return "", errors.New("订单参数缺失")
	}
	if in.RequestID == "" {
		return "", nil
	} // Older clients remain compatible.
	if !createRequestID.MatchString(in.RequestID) {
		return "", errors.New("订单请求编号无效")
	}
	if len(s.snapshotKey) < 16 {
		return "", errors.New("订单请求安全配置不可用")
	}
	copy := *in
	copy.RequestID = ""
	copy.QuoteToken = ""
	copy.VerifiedQuote = nil
	raw, err := json.Marshal(copy)
	if err != nil {
		return "", err
	}
	mac := hmac.New(sha256.New, s.snapshotKey)
	mac.Write([]byte("order-create-v1:"))
	mac.Write(raw)
	return hex.EncodeToString(mac.Sum(nil)), nil
}
func (s *Service) replayOrder(ctx context.Context, customerID, orderID int64) (*OrderView, error) {
	row, err := s.repo.FindByID(ctx, orderID)
	if err != nil {
		return nil, err
	}
	if row == nil || row.CustomerID != customerID {
		return nil, errors.New("原订单不可用，请核对订单记录")
	}
	items, err := s.repo.ListItems(ctx, orderID)
	if err != nil {
		return nil, err
	}
	view := orderToView(row)
	for _, item := range items {
		view.Items = append(view.Items, itemToView(item))
	}
	return view, nil
}
func (s *Service) existingRequest(ctx context.Context, customerID int64, in *CreateOrderInput, fingerprint string) (int64, error) {
	if in.RequestID == "" {
		return 0, nil
	}
	var row createRequest
	err := s.repo.Transaction(ctx, func(tx *gorm.DB) error {
		return tx.Where("customer_id=? AND request_id=?", customerID, in.RequestID).Take(&row).Error
	})
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	if row.Fingerprint != fingerprint {
		return 0, errors.New("该请求编号已用于其他订单配置，请核对已有订单")
	}
	return row.OrderID, nil
}
func claimCreateRequest(tx *gorm.DB, customerID int64, in *CreateOrderInput, fingerprint string) (int64, error) {
	if in.RequestID == "" {
		return 0, nil
	}
	row := createRequest{CustomerID: customerID, RequestID: in.RequestID, Fingerprint: fingerprint}
	if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&row).Error; err != nil {
		return 0, err
	}
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("customer_id=? AND request_id=?", customerID, in.RequestID).Take(&row).Error; err != nil {
		return 0, err
	}
	if row.Fingerprint != fingerprint {
		return 0, errors.New("该请求编号已用于其他订单配置，请核对已有订单")
	}
	return row.OrderID, nil
}
func finishCreateRequest(tx *gorm.DB, customerID int64, in *CreateOrderInput, order *dom.Order) error {
	if in.RequestID == "" {
		return nil
	}
	result := tx.Model(&createRequest{}).Where("customer_id=? AND request_id=? AND order_id=0", customerID, in.RequestID).Update("order_id", order.ID)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return errors.New("订单请求状态已变化")
	}
	return nil
}
