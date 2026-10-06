package points

import (
	"context"
	"crypto/rand"
	marketingdom "ddyidc/internal/marketing/domain"
	marketingrepo "ddyidc/internal/marketing/repository"
	orderservice "ddyidc/internal/order/service"
	"encoding/hex"
	"encoding/json"
	"errors"
	"gorm.io/gorm"
	"strconv"
	"time"
)

func (s *Service) SetOrderService(service *orderservice.Service) { s.orders = service }

func (s *Service) createSystemOrder(ctx context.Context, tx *gorm.DB, user int64, goods Goods) (string, error) {
	if s.orders == nil {
		return "", errors.New("系统商品交付服务尚未配置")
	}
	var input orderservice.CreateOrderInput
	if json.Unmarshal(goods.Config, &input) != nil || input.ProductID == "" || input.DurationMonth < 1 || input.BillingCycle == "" {
		return "", errors.New("系统商品配置不完整")
	}
	input.Quantity = 1
	input.UserCouponID = ""
	var random [12]byte
	if _, err := rand.Read(random[:]); err != nil {
		return "", err
	}
	input.Password = "Aa1" + hex.EncodeToString(random[:])[:9]
	input.Hostname = "points-" + hex.EncodeToString(random[6:])
	worker := s.orders.WithTransaction(tx)
	prepared, err := worker.PrepareOrder(ctx, user, &input)
	if err != nil {
		return "", err
	}
	if prepared.TotalAmount() <= 0 {
		return "", errors.New("系统商品定价无效")
	}
	coupon := marketingdom.Coupon{Name: "积分系统商品专用抵扣", Type: 1, FaceValue: prepared.TotalAmount(), TotalCount: 1, PerUserLimit: 1, ValidDays: 1, Status: 1, ClaimCode: marketingdom.StrPtr("POINTS-SYSTEM-" + randomCode())}
	repo := marketingrepo.NewRepository(tx)
	if err = repo.CreateCoupon(ctx, &coupon); err != nil {
		return "", err
	}
	issued, err := repo.ClaimCouponTx(ctx, tx, user, &coupon, 2)
	if err != nil {
		return "", err
	}
	input.UserCouponID = strconv.FormatInt(issued.ID, 10)
	input.Remark = "积分商城兑换：" + goods.Name
	result, err := worker.CreateOrder(ctx, user, &input)
	if err != nil {
		return "", err
	}
	if result.PayAmount != 0 {
		return "", errors.New("积分抵扣未覆盖系统订单，兑换已回滚")
	}
	if err = repo.InsertCouponLog(ctx, tx, &marketingdom.CouponLog{CouponID: coupon.ID, UserCouponID: &issued.ID, UserID: &user, Action: 3, Remark: marketingdom.StrPtr("积分系统商品兑换")}); err != nil {
		return "", err
	}
	return result.ID, nil
}

func (s *Service) SyncSystemOrders(ctx context.Context) error {
	var rows []struct {
		ID      int64
		Status  uint8
		OrderID int64
	}
	if err := s.db.WithContext(ctx).Table("system.points_orders p").Select("p.id,o.status,o.id AS order_id").Joins("JOIN `order`.orders o ON o.id=CAST(JSON_UNQUOTE(JSON_EXTRACT(p.config_snapshot,'$.platformOrderId')) AS UNSIGNED) AND o.customer_id=p.user_id").Where("p.goods_type='system' AND p.status='processing' AND o.status IN (3,4,6)").Limit(100).Find(&rows).Error; err != nil {
		return err
	}
	for _, row := range rows {
		status := "cancelled"
		if row.Status == 6 {
			status = "completed"
		}
		if err := s.SetOrderStatus(ctx, row.ID, status); err != nil {
			return err
		}
	}
	// Delivery time, not original exchange time, starts the receipt window.
	return s.db.WithContext(ctx).Table(ordersTable).Where("goods_type='physical' AND status='processing' AND updated_at<=?", s.now().Add(-14*24*time.Hour)).Update("status", "completed").Error
}
