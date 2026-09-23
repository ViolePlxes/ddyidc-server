// M4 coupons business logic: claim center, targeted issue, order-time
// redemption. Coupon claiming and redemption run inside transactions with
// conditional updates to prevent over-issue under concurrency.
package marketing

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	dom "ddyidc/internal/marketing/domain"
	repo "ddyidc/internal/marketing/repository"
	"gorm.io/gorm"
)

// Coupon input for admin creation.
type CouponInput struct {
	Name         string  `json:"name"`
	Type         uint8   `json:"type"` // 1满减 2折扣
	FaceValue    int64   `json:"faceValue"`
	DiscountRate float64 `json:"discountRate"`
	MaxDiscount  int64   `json:"maxDiscount"`
	MinAmount    int64   `json:"minAmount"`
	TotalCount   int32   `json:"totalCount"`
	PerUserLimit uint32  `json:"perUserLimit"`
	ClaimCode    string  `json:"claimCode"`
	ValidDays    uint32  `json:"validDays"`
	EndAt        string  `json:"endAt"`
}

// CreateCoupon 管理端创建优惠券模板。
func (s *Service) CreateCoupon(ctx context.Context, input *CouponInput) (*CouponDTO, error) {
	if strings.TrimSpace(input.Name) == "" {
		return nil, errors.New("请输入优惠券名称")
	}
	if input.Type != 1 && input.Type != 2 {
		return nil, errors.New("优惠券类型错误")
	}
	if input.Type == 1 && input.FaceValue <= 0 {
		return nil, errors.New("满减券面额必须大于0")
	}
	if input.Type == 2 && (input.DiscountRate <= 0 || input.DiscountRate >= 1) {
		return nil, errors.New("折扣率需在0-1之间（0.9表示9折）")
	}
	if input.TotalCount == 0 {
		input.TotalCount = -1
	}
	if input.PerUserLimit == 0 {
		input.PerUserLimit = 1
	}
	validDays := input.ValidDays
	if validDays == 0 {
		values, err := s.configMap(ctx)
		if err != nil {
			return nil, err
		}
		validDays = uint32(configInt(values, CfgCouponValidDays, 30))
	}
	coupon := &dom.Coupon{
		Name: strings.TrimSpace(input.Name), Type: input.Type,
		FaceValue: input.FaceValue, DiscountRate: input.DiscountRate,
		MaxDiscount: input.MaxDiscount, MinAmount: input.MinAmount,
		TotalCount: input.TotalCount, PerUserLimit: input.PerUserLimit,
		ValidDays: validDays, Status: dom.CouponEnabled,
	}
	if code := strings.TrimSpace(input.ClaimCode); code != "" {
		coupon.ClaimCode = dom.StrPtr(strings.ToUpper(code))
	}
	if input.EndAt != "" {
		t, err := time.ParseInLocation("2006-01-02 15:04:05", input.EndAt, time.Local)
		if err != nil {
			t2, err2 := time.ParseInLocation("2006-01-02", input.EndAt, time.Local)
			if err2 != nil {
				return nil, errors.New("发放截止时间格式错误")
			}
			t = t2
		}
		coupon.EndAt = &t
	}
	if err := s.repo.CreateCoupon(ctx, coupon); err != nil {
		if isDuplicateEntry(err) {
			return nil, errors.New("领取码已存在，请更换")
		}
		return nil, fmt.Errorf("创建优惠券失败: %w", err)
	}
	if err := s.logCoupon(ctx, nil, coupon.ID, nil, nil, 1, nil, "admin", "创建优惠券"); err != nil {
		return nil, err
	}
	return couponToDTO(coupon), nil
}

// ListCoupons 管理端优惠券列表。
func (s *Service) ListCoupons(ctx context.Context, page, pageSize int, status *uint8, keyword string) ([]*CouponDTO, int64, error) {
	rows, total, err := s.repo.ListCoupons(ctx, page, pageSize, status, keyword)
	if err != nil {
		return nil, 0, fmt.Errorf("查询优惠券失败: %w", err)
	}
	out := make([]*CouponDTO, 0, len(rows))
	for _, row := range rows {
		out = append(out, couponToDTO(row))
	}
	return out, total, nil
}

// SetCouponStatus 管理端停用/启用。
func (s *Service) SetCouponStatus(ctx context.Context, id int64, status uint8) error {
	coupon, err := s.repo.FindCoupon(ctx, id)
	if err != nil {
		return fmt.Errorf("查询优惠券失败: %w", err)
	}
	if coupon == nil {
		return errors.New("优惠券不存在")
	}
	coupon.Status = status
	if err := s.repo.UpdateCoupon(ctx, coupon); err != nil {
		return fmt.Errorf("更新优惠券失败: %w", err)
	}
	action := uint8(5)
	remark := "停用优惠券"
	if status == dom.CouponEnabled {
		remark = "启用优惠券"
	}
	return s.logCoupon(ctx, nil, coupon.ID, nil, nil, action, nil, "admin", remark)
}

// IssueCoupon 管理端定向发券。
func (s *Service) IssueCoupon(ctx context.Context, couponID int64, userIDs []int64, operator string) (int, error) {
	if len(userIDs) == 0 {
		return 0, errors.New("请指定发放用户")
	}
	coupon, err := s.repo.FindCoupon(ctx, couponID)
	if err != nil {
		return 0, fmt.Errorf("查询优惠券失败: %w", err)
	}
	if coupon == nil {
		return 0, errors.New("优惠券不存在")
	}
	if coupon.Status != dom.CouponEnabled {
		return 0, errors.New("优惠券已停用，不能发放")
	}
	var issued int
	txErr := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		n, err := s.repo.IssueCouponTx(ctx, tx, coupon, userIDs, 2, operator)
		issued = n
		return err
	})
	if txErr != nil {
		return issued, fmt.Errorf("发放优惠券失败: %w", txErr)
	}
	return issued, nil
}

// ClaimCenter 领券中心。
func (s *Service) ClaimCenter(ctx context.Context, userID int64) ([]*ClaimableCouponDTO, error) {
	values, err := s.configMap(ctx)
	if err != nil {
		return nil, err
	}
	if !configBool(values, CfgCouponSelfClaim) {
		return []*ClaimableCouponDTO{}, nil
	}
	if err := s.repo.ExpireOverdueCoupons(ctx); err != nil {
		return nil, fmt.Errorf("刷新优惠券状态失败: %w", err)
	}
	rows, err := s.repo.ListClaimableCoupons(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("查询可领优惠券失败: %w", err)
	}
	out := make([]*ClaimableCouponDTO, 0, len(rows))
	for _, row := range rows {
		dto := &ClaimableCouponDTO{CouponDTO: *couponToDTO(&row.Coupon), Remaining: row.Remaining, UserClaimed: row.UserClaimed}
		out = append(out, dto)
	}
	return out, nil
}

// ClaimCoupon 自主领取 / 领取码领取共用入口。
func (s *Service) ClaimCoupon(ctx context.Context, userID int64, couponID int64, claimCode string, source uint8) (*UserCouponDTO, error) {
	values, err := s.configMap(ctx)
	if err != nil {
		return nil, err
	}
	if source == 1 && !configBool(values, CfgCouponSelfClaim) {
		return nil, errors.New("领券中心未开启")
	}
	var coupon *dom.Coupon
	if source == 3 {
		if strings.TrimSpace(claimCode) == "" {
			return nil, errors.New("请输入领取码")
		}
		coupon, err = s.repo.FindCouponByClaimCode(ctx, strings.ToUpper(strings.TrimSpace(claimCode)))
	} else {
		coupon, err = s.repo.FindCoupon(ctx, couponID)
	}
	if err != nil {
		return nil, fmt.Errorf("查询优惠券失败: %w", err)
	}
	if coupon == nil {
		return nil, errors.New("优惠券不存在")
	}
	if coupon.Status != dom.CouponEnabled {
		return nil, errors.New("优惠券已停用")
	}
	if coupon.EndAt != nil && coupon.EndAt.Before(time.Now()) {
		return nil, errors.New("优惠券已过发放截止时间")
	}
	var created *dom.UserCoupon
	txErr := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		held, err := s.repo.CountUserCoupons(ctx, userID, coupon.ID)
		if err != nil {
			return err
		}
		if coupon.PerUserLimit > 0 && held >= int64(coupon.PerUserLimit) {
			return errors.New("已达每人限领数量")
		}
		created, err = s.repo.ClaimCouponTx(ctx, tx, userID, coupon, source)
		if err != nil {
			return err
		}
		uid := userID
		return s.repo.InsertCouponLog(ctx, tx, &dom.CouponLog{
			CouponID: coupon.ID, UserCouponID: &created.ID, UserID: &uid,
			Action: 3, Operator: dom.StrPtr(fmt.Sprintf("user:%d", userID)), Remark: dom.StrPtr("领取优惠券"),
		})
	})
	if txErr != nil {
		return nil, txErr
	}
	return userCouponToDTO(&repo.UserCouponRow{UserCoupon: *created, Name: coupon.Name, Type: coupon.Type}), nil
}

// MyCoupons 我的优惠券。
func (s *Service) MyCoupons(ctx context.Context, userID int64, status *uint8, page, pageSize int) ([]*UserCouponDTO, int64, error) {
	if err := s.repo.ExpireOverdueCoupons(ctx); err != nil {
		return nil, 0, fmt.Errorf("刷新优惠券状态失败: %w", err)
	}
	rows, total, err := s.repo.ListUserCoupons(ctx, userID, status, page, pageSize)
	if err != nil {
		return nil, 0, fmt.Errorf("查询我的优惠券失败: %w", err)
	}
	out := make([]*UserCouponDTO, 0, len(rows))
	for _, row := range rows {
		out = append(out, userCouponToDTO(row))
	}
	return out, total, nil
}

// UsableCouponsForOrder 下单页可选券（含优惠预览）。
func (s *Service) UsableCouponsForOrder(ctx context.Context, userID int64, amountCents int64) ([]*UserCouponDTO, error) {
	rows, err := s.repo.ListUsableCoupons(ctx, userID, amountCents)
	if err != nil {
		return nil, fmt.Errorf("查询可用优惠券失败: %w", err)
	}
	out := make([]*UserCouponDTO, 0, len(rows))
	for _, row := range rows {
		dto := userCouponToDTO(row)
		dto.PreviewDiscount = computeDiscount(row.Type, row.FaceValue, row.DiscountRate, row.MaxDiscount, amountCents)
		out = append(out, dto)
	}
	return out, nil
}

// ChargeCouponForOrder 由订单模块在创建订单的事务内调用：校验并把券核销。
// 返回服务端计算的优惠金额（分）。
func (s *Service) ChargeCouponForOrder(ctx context.Context, tx *gorm.DB, userID, userCouponID, orderID, amountCents int64) (int64, error) {
	row, err := s.repo.FindUserCoupon(ctx, userCouponID)
	if err != nil {
		return 0, fmt.Errorf("查询优惠券失败: %w", err)
	}
	if row == nil || row.UserID != userID {
		return 0, errors.New("优惠券不存在")
	}
	coupon, err := s.repo.FindCoupon(ctx, row.CouponID)
	if err != nil {
		return 0, fmt.Errorf("查询优惠券失败: %w", err)
	}
	if coupon == nil || coupon.Status != dom.CouponEnabled {
		return 0, errors.New("优惠券已停用")
	}
	if row.Status != dom.UserCouponUnused {
		return 0, errors.New("优惠券已使用或已作废")
	}
	if row.ExpireAt.Before(time.Now()) {
		return 0, errors.New("优惠券已过期")
	}
	if amountCents < coupon.MinAmount {
		return 0, fmt.Errorf("未达到优惠券使用门槛（满 %s 元可用）", dom.FormatCents(coupon.MinAmount))
	}
	discount := computeDiscount(coupon.Type, coupon.FaceValue, coupon.DiscountRate, coupon.MaxDiscount, amountCents)
	if discount <= 0 {
		return 0, errors.New("优惠券无可优惠金额")
	}
	if err := s.repo.MarkUserCouponUsedTx(ctx, tx, userCouponID, userID, orderID); err != nil {
		return 0, err
	}
	if err := tx.WithContext(ctx).Exec(
		"UPDATE marketing.coupons SET used_count = used_count + 1 WHERE id = ?", coupon.ID).Error; err != nil {
		return 0, fmt.Errorf("更新优惠券核销统计失败: %w", err)
	}
	if err := s.logCouponTx(ctx, tx, coupon.ID, &userCouponID, &userID, 4, &orderID, fmt.Sprintf("user:%d", userID), "下单核销"); err != nil {
		return 0, err
	}
	return discount, nil
}

// ReleaseCouponForOrder 订单取消时归还优惠券。
func (s *Service) ReleaseCouponForOrder(ctx context.Context, tx *gorm.DB, orderID int64) error {
	return s.repo.ReleaseUserCouponTx(ctx, tx, orderID)
}

// computeDiscount 服务端金额计算：全部以分为单位。
func computeDiscount(couponType uint8, faceValue int64, rate float64, maxDiscount, amountCents int64) int64 {
	var discount int64
	if couponType == 2 && rate > 0 && rate < 1 {
		discount = int64(math.Round(float64(amountCents) * (1 - rate)))
		if maxDiscount > 0 && discount > maxDiscount {
			discount = maxDiscount
		}
	} else {
		discount = faceValue
	}
	if discount > amountCents {
		discount = amountCents
	}
	if discount < 0 {
		discount = 0
	}
	return discount
}

func (s *Service) logCoupon(ctx context.Context, tx *gorm.DB, couponID int64, userCouponID, userID *int64, action uint8, orderID *int64, operator, remark string) error {
	return s.logCouponTx(ctx, tx, couponID, userCouponID, userID, action, orderID, operator, remark)
}

func (s *Service) logCouponTx(ctx context.Context, tx *gorm.DB, couponID int64, userCouponID, userID *int64, action uint8, orderID *int64, operator, remark string) error {
	return s.repo.InsertCouponLog(ctx, tx, &dom.CouponLog{
		CouponID: couponID, UserCouponID: userCouponID, UserID: userID,
		Action: action, OrderID: orderID, Operator: dom.StrPtr(operator), Remark: dom.StrPtr(remark),
	})
}

func couponToDTO(v *dom.Coupon) *CouponDTO {
	return &CouponDTO{
		ID: formatInt64(v.ID), Name: v.Name, Type: v.Type,
		FaceValue: v.FaceValue, DiscountRate: v.DiscountRate, MaxDiscount: v.MaxDiscount,
		MinAmount: v.MinAmount, TotalCount: v.TotalCount,
		ClaimedCount: int64(v.ClaimedCount), UsedCount: int64(v.UsedCount),
		PerUserLimit: v.PerUserLimit, ClaimCode: v.ClaimCode, ValidDays: v.ValidDays,
		EndAt: formatTimePtr(v.EndAt), Status: v.Status, CreatedAt: formatTime(v.CreatedAt),
	}
}

func userCouponToDTO(row *repo.UserCouponRow) *UserCouponDTO {
	dto := &UserCouponDTO{
		ID: formatInt64(row.ID), CouponID: formatInt64(row.CouponID), Code: row.Code,
		Name: row.Name, Type: row.Type, FaceValue: row.FaceValue,
		DiscountRate: row.DiscountRate, MaxDiscount: row.MaxDiscount, MinAmount: row.MinAmount,
		Status: row.Status, ExpireAt: formatTime(row.ExpireAt), UsedAt: formatTimePtr(row.UsedAt),
	}
	if row.OrderID != nil {
		dto.OrderID = formatInt64(*row.OrderID)
	}
	return dto
}
