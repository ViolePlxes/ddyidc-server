// Package marketing api: cross-module hooks. The customer auth handler uses
// AuthHooks; the order module uses OrderHooks. Both are wired in main.go so
// modules stay decoupled (interface-only dependency direction).
package marketing

import (
	"context"

	svc "ddyidc/internal/marketing/service"
	"gorm.io/gorm"
)

// AuthHooks registration-time hooks exposed to the customer module.
type AuthHooks struct {
	service *svc.Service
}

func NewAuthHooks(service *svc.Service) *AuthHooks { return &AuthHooks{service: service} }

// VerifyInvite 校验邀请码，返回邀请人名称。
func (h *AuthHooks) VerifyInvite(ctx context.Context, code string) (string, error) {
	return h.service.VerifyInviteCode(ctx, code)
}

// VerifySales 校验客服工号，返回销售姓名。
func (h *AuthHooks) VerifySales(ctx context.Context, staffNo string) (string, error) {
	return h.service.VerifySalesCode(ctx, staffNo)
}

// AfterRegister 注册成功后建立邀请关系树并绑定销售。
func (h *AuthHooks) AfterRegister(ctx context.Context, customerID int64, inviteCode, salesCode string) error {
	return h.service.AfterRegister(ctx, customerID, inviteCode, salesCode)
}

// OrderHooks order-flow hooks exposed to the order module. tx carries the
// caller's transaction so coupon redemption commits atomically with the order.
type OrderHooks struct {
	service *svc.Service
}

func NewOrderHooks(service *svc.Service) *OrderHooks { return &OrderHooks{service: service} }

// ChargeCoupon 在订单创建事务内核销优惠券，返回服务端计算的优惠金额（分）。
func (h *OrderHooks) ChargeCoupon(ctx context.Context, tx *gorm.DB, userID, userCouponID, orderID, amountCents int64) (int64, error) {
	return h.service.ChargeCouponForOrder(ctx, tx, userID, userCouponID, orderID, amountCents)
}

// OnOrderPaid 订单支付成功 → 生成分销奖励（bizType=1）。
func (h *OrderHooks) OnOrderPaid(ctx context.Context, orderID int64, orderNo string, customerID, payCents int64) error {
	return h.service.OnPaid(ctx, 1, orderID, orderNo, customerID, payCents)
}

// OnRechargePaid 充值成功 → 生成分销奖励（bizType=2）。
func (h *OrderHooks) OnRechargePaid(ctx context.Context, rechargeID int64, rechargeNo string, customerID, amountCents int64) error {
	return h.service.OnPaid(ctx, 2, rechargeID, rechargeNo, customerID, amountCents)
}

// ReleaseCoupon 订单取消 → 归还优惠券。
func (h *OrderHooks) ReleaseCoupon(ctx context.Context, orderID int64) error {
	return h.service.ReleaseCouponForOrder(ctx, orderID)
}
