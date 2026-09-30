package points

import (
	"context"
	marketingdom "ddyidc/internal/marketing/domain"
	marketingrepo "ddyidc/internal/marketing/repository"
	"errors"
	"fmt"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"net/http"
	"regexp"
)

type CheckoutCoupon struct {
	UserID       int64  `json:"-"`
	RequestID    string `json:"-"`
	Points       int64  `json:"points"`
	UserCouponID int64  `json:"userCouponId,string"`
	AmountYuan   string `json:"amountYuan"`
}

var checkoutRequestPattern = regexp.MustCompile(`^[a-zA-Z0-9-]{16,64}$`)

// Redeem into a private, single-use coupon. Existing order coupon validation,
// cancellation and discount calculation remain the sole order authority.
func (s *Service) CheckoutCoupon(ctx context.Context, userID, points int64, requestID string) (*CheckoutCoupon, error) {
	if !checkoutRequestPattern.MatchString(requestID) {
		return nil, errors.New("兑换请求标识无效")
	}
	settings, e := s.Settings(ctx)
	if e != nil {
		return nil, e
	}
	if !settings.Enabled || !settings.CheckoutEnabled {
		return nil, errors.New("积分抵扣暂未开放")
	}
	rate := settings.CheckoutPointsPerYuan
	if rate < 1 || points < rate || points > 1000000000 || points%rate != 0 {
		return nil, errors.New("请按整元对应积分数量兑换")
	}
	out := CheckoutCoupon{UserID: userID, RequestID: requestID, Points: points, AmountYuan: fmt.Sprintf("%d.00", points/rate)}
	e = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if e := s.ensureAccountTx(ctx, tx, userID); e != nil {
			return e
		}
		var a Account
		if e := tx.Table(accountsTable).Clauses(clause.Locking{Strength: "UPDATE"}).Where("user_id=?", userID).Take(&a).Error; e != nil {
			return e
		}
		var prior CheckoutCoupon
		err := tx.Table("system.points_checkout_coupons").Where("user_id=? AND request_id=?", userID, requestID).Take(&prior).Error
		if err == nil {
			if prior.Points != points {
				return errors.New("兑换请求内容已改变")
			}
			out = prior
			return nil
		}
		if err != gorm.ErrRecordNotFound {
			return err
		}
		if _, e := s.debitTx(ctx, tx, userID, points, "checkout", "checkout", requestID, "积分兑换订单抵扣券", false); e != nil {
			return e
		}
		coupon := marketingdom.Coupon{Name: "积分抵扣券", Type: 1, FaceValue: (points / rate) * 100, TotalCount: 1, PerUserLimit: 1, ValidDays: uint32(settings.CheckoutCouponDays), Status: 1, ClaimCode: marketingdom.StrPtr("POINTS-" + randomCode())}
		repo := marketingrepo.NewRepository(tx)
		if e := repo.CreateCoupon(ctx, &coupon); e != nil {
			return e
		}
		issued, e := repo.ClaimCouponTx(ctx, tx, userID, &coupon, 2)
		if e != nil {
			return e
		}
		out.UserCouponID = issued.ID
		if e = repo.InsertCouponLog(ctx, tx, &marketingdom.CouponLog{CouponID: coupon.ID, UserCouponID: &issued.ID, UserID: &userID, Action: 3, Remark: marketingdom.StrPtr("积分兑换订单抵扣券")}); e != nil {
			return e
		}
		return tx.Table("system.points_checkout_coupons").Create(&out).Error
	})
	return &out, e
}

func (h *Handler) checkoutCoupon(w http.ResponseWriter, r *http.Request) {
	id, ok := h.user(w, r)
	if !ok {
		return
	}
	var input struct {
		Points    int64  `json:"points"`
		RequestID string `json:"requestId"`
	}
	if !decode(w, r, &input) {
		return
	}
	result, err := h.service.CheckoutCoupon(r.Context(), id, input.Points, input.RequestID)
	writeResult(w, result, err)
}
