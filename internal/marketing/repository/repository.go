// Package marketing repository: all table access for the marketing suite.
// Cross-domain reads go through explicit schema-qualified table names.
package marketing

import (
	"context"
	"errors"
	"strings"
	"time"

	dom "ddyidc/internal/marketing/domain"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Repository 数据访问接口
type Repository interface {
	// 配置（system.system_configs，marketing.* 前缀键）
	ListConfigsByPrefix(ctx context.Context, prefix string) (map[string]string, error)
	SaveConfig(ctx context.Context, key, value, description string) error

	// 优惠码（M1）
	FindInviteByUser(ctx context.Context, userID int64) (*dom.Invite, error)
	FindInviteByCode(ctx context.Context, code string) (*dom.Invite, error)
	FindInviteByID(ctx context.Context, id int64) (*dom.Invite, error)
	CreateInvite(ctx context.Context, v *dom.Invite) error
	UpdateInvite(ctx context.Context, v *dom.Invite) error
	ListInvites(ctx context.Context, page, pageSize int, status *uint8, keyword string) ([]*InviteRow, int64, error)

	// 推广关系树（M1）
	CreateRelation(ctx context.Context, tx *gorm.DB, v *dom.InviteRelation) error
	FindActiveRelation(ctx context.Context, inviteeID int64, level uint8) (*dom.InviteRelation, error)
	ListRelationsByInviter(ctx context.Context, inviterID int64, level uint8) ([]*dom.InviteRelation, error)
	CountRelations(ctx context.Context, inviterID int64, level uint8) (int64, error)
	IncreaseInviteCount(ctx context.Context, userID int64, delta int) error

	// 分销奖励（M2）
	CreateRewards(ctx context.Context, tx *gorm.DB, list []*dom.Reward) error
	ListRewards(ctx context.Context, page, pageSize int, status *uint8, level *uint8, keyword string, rewardUserID int64) ([]*RewardRow, int64, error)
	FindReward(ctx context.Context, id int64) (*dom.Reward, error)
	UpdateRewardStatus(ctx context.Context, id int64, from, to uint8, remark string) error
	ReclaimRewardsByOrder(ctx context.Context, orderID int64, remark string) (int64, error)
	SumRewardAmountByStatus(ctx context.Context, rewardUserID int64) (map[uint8]int64, error)
	SumDailySettledRewards(ctx context.Context, rewardUserID int64, yesterday, today, tomorrow time.Time) (int64, int64, error)

	// 优惠券（M4）
	CreateCoupon(ctx context.Context, v *dom.Coupon) error
	UpdateCoupon(ctx context.Context, v *dom.Coupon) error
	FindCoupon(ctx context.Context, id int64) (*dom.Coupon, error)
	FindCouponByClaimCode(ctx context.Context, code string) (*dom.Coupon, error)
	ListCoupons(ctx context.Context, page, pageSize int, status *uint8, keyword string) ([]*dom.Coupon, int64, error)
	CountUserCoupons(ctx context.Context, userID, couponID int64) (int64, error)
	ClaimCouponTx(ctx context.Context, tx *gorm.DB, userID int64, coupon *dom.Coupon, source uint8) (*dom.UserCoupon, error)
	ListUserCoupons(ctx context.Context, userID int64, status *uint8, page, pageSize int) ([]*UserCouponRow, int64, error)
	FindUserCoupon(ctx context.Context, id int64) (*dom.UserCoupon, error)
	ListClaimableCoupons(ctx context.Context, userID int64) ([]*ClaimableCouponRow, error)
	ListUsableCoupons(ctx context.Context, userID int64, amountCents int64) ([]*UserCouponRow, error)
	MarkUserCouponUsedTx(ctx context.Context, tx *gorm.DB, userCouponID, userID, orderID int64) error
	ReleaseUserCouponTx(ctx context.Context, tx *gorm.DB, orderID int64) error
	ExpireOverdueCoupons(ctx context.Context) error
	InsertCouponLog(ctx context.Context, tx *gorm.DB, l *dom.CouponLog) error
	IssueCouponTx(ctx context.Context, tx *gorm.DB, coupon *dom.Coupon, userIDs []int64, source uint8, operator string) (int, error)

	// 销售客服（M5）
	CreateSalesUser(ctx context.Context, v *dom.SalesUser) error
	UpdateSalesUser(ctx context.Context, v *dom.SalesUser) error
	FindSalesUser(ctx context.Context, id int64) (*dom.SalesUser, error)
	FindSalesUserByUsername(ctx context.Context, username string) (*dom.SalesUser, error)
	FindSalesUserByStaffNo(ctx context.Context, staffNo string) (*dom.SalesUser, error)
	ListSalesUsers(ctx context.Context, page, pageSize int, keyword string) ([]*dom.SalesUser, int64, error)
	FindVisibilityRule(ctx context.Context, salesID int64) (*dom.SalesVisibilityRule, error)
	SaveVisibilityRule(ctx context.Context, v *dom.SalesVisibilityRule) error
	BindSalesCustomer(ctx context.Context, v *dom.SalesCustomer) error
	UnbindSalesCustomer(ctx context.Context, salesID, customerID int64) error
	FindSalesOwner(ctx context.Context, customerID int64) (int64, error)
	GetSalesCustomer(ctx context.Context, salesID, customerID int64) (*dom.SalesCustomerRow, error)
	ListSalesCustomers(ctx context.Context, salesID int64, page, pageSize int, keyword string) ([]*dom.SalesCustomerRow, int64, error)
	CreateFollowup(ctx context.Context, v *dom.SalesFollowup) error
	ListFollowups(ctx context.Context, salesID, customerID int64, page, pageSize int) ([]*dom.SalesFollowup, int64, error)

	// 跨域只读（客户/订单/充值/账户）
	FindCustomerBasic(ctx context.Context, id int64) (*CustomerBasic, error)
	FindCustomerByUsernameOrEmail(ctx context.Context, account string) (*CustomerBasic, error)
	ListCustomerOrders(ctx context.Context, customerID int64, page, pageSize int) ([]*dom.CustomerOrderRow, int64, error)
	SumInviteePaidOrders(ctx context.Context, inviteeIDs []int64) (spendCents int64, orderCount int64, err error)
	SumInviteeRecharges(ctx context.Context, inviteeIDs []int64) (rechargeCents int64, err error)
}

type GormRepository struct{ db *gorm.DB }

func NewRepository(database *gorm.DB) Repository { return &GormRepository{db: database} }

// CustomerBasic is the minimal cross-domain customer projection.
type CustomerBasic struct {
	ID        int64     `gorm:"column:id"`
	Username  string    `gorm:"column:username"`
	Email     *string   `gorm:"column:email"`
	Phone     *string   `gorm:"column:phone"`
	RealName  *string   `gorm:"column:real_name"`
	Status    uint8     `gorm:"column:status"`
	CreatedAt time.Time `gorm:"column:created_at"`
}

func (r *GormRepository) ListConfigsByPrefix(c context.Context, prefix string) (map[string]string, error) {
	var rows []struct {
		Key   string  `gorm:"column:key"`
		Value *string `gorm:"column:value"`
	}
	e := r.db.WithContext(c).Table("system.system_configs").Where("`key` LIKE ?", prefix+"%").Find(&rows).Error
	if e != nil {
		return nil, e
	}
	out := make(map[string]string, len(rows))
	for _, row := range rows {
		if row.Value != nil {
			out[row.Key] = *row.Value
		}
	}
	return out, nil
}

func (r *GormRepository) SaveConfig(c context.Context, key, value, description string) error {
	var count int64
	if e := r.db.WithContext(c).Table("system.system_configs").Where("`key` = ?", key).Count(&count).Error; e != nil {
		return e
	}
	if count > 0 {
		return r.db.WithContext(c).Table("system.system_configs").Where("`key` = ?", key).Update("value", value).Error
	}
	return r.db.WithContext(c).Exec(
		"INSERT INTO system.system_configs (id, `key`, value, description) VALUES (?, ?, ?, ?)",
		dom.NewID(), key, value, description).Error
}

// InviteRow is the admin list projection joined with the owner's username.
type InviteRow struct {
	dom.Invite
	Username string  `json:"username"`
	Email    *string `json:"email"`
}

func (r *GormRepository) FindInviteByUser(c context.Context, userID int64) (*dom.Invite, error) {
	var v dom.Invite
	e := r.db.WithContext(c).Table(dom.Invite{}.TableName()).Where("user_id = ?", userID).First(&v).Error
	if errors.Is(e, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	return &v, e
}

func (r *GormRepository) FindInviteByCode(c context.Context, code string) (*dom.Invite, error) {
	var v dom.Invite
	e := r.db.WithContext(c).Table(dom.Invite{}.TableName()).Where("code = ?", code).First(&v).Error
	if errors.Is(e, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	return &v, e
}

func (r *GormRepository) FindInviteByID(c context.Context, id int64) (*dom.Invite, error) {
	var v dom.Invite
	e := r.db.WithContext(c).Table(dom.Invite{}.TableName()).Where("id = ?", id).First(&v).Error
	if errors.Is(e, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	return &v, e
}

func (r *GormRepository) CreateInvite(c context.Context, v *dom.Invite) error {
	return r.db.WithContext(c).Table(dom.Invite{}.TableName()).Create(v).Error
}

func (r *GormRepository) UpdateInvite(c context.Context, v *dom.Invite) error {
	return r.db.WithContext(c).Table(dom.Invite{}.TableName()).Save(v).Error
}

func (r *GormRepository) ListInvites(c context.Context, page, pageSize int, status *uint8, keyword string) ([]*InviteRow, int64, error) {
	if page < 1 {
		page = 1
	}
	if pageSize < 1 {
		pageSize = 20
	}
	q := r.db.WithContext(c).Table(dom.Invite{}.TableName() + " AS i").
		Joins("LEFT JOIN customer.customers cu ON cu.id = i.user_id").
		Select("i.*, cu.username AS username, cu.email AS email")
	if status != nil {
		q = q.Where("i.status = ?", *status)
	}
	if kw := strings.TrimSpace(keyword); kw != "" {
		q = q.Where("i.code LIKE ? OR cu.username LIKE ?", "%"+kw+"%", "%"+kw+"%")
	}
	var total int64
	if e := q.Count(&total).Error; e != nil {
		return nil, 0, e
	}
	var out []*InviteRow
	e := q.Order("i.created_at DESC").Offset((page - 1) * pageSize).Limit(pageSize).Scan(&out).Error
	return out, total, e
}

func (r *GormRepository) CreateRelation(c context.Context, tx *gorm.DB, v *dom.InviteRelation) error {
	db := r.db.WithContext(c)
	if tx != nil {
		db = tx
	}
	return db.Table(dom.InviteRelation{}.TableName()).Create(v).Error
}

func (r *GormRepository) FindActiveRelation(c context.Context, inviteeID int64, level uint8) (*dom.InviteRelation, error) {
	var v dom.InviteRelation
	e := r.db.WithContext(c).Table(dom.InviteRelation{}.TableName()).
		Where("invitee_id = ? AND level = ? AND status = 1", inviteeID, level).First(&v).Error
	if errors.Is(e, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	return &v, e
}

func (r *GormRepository) ListRelationsByInviter(c context.Context, inviterID int64, level uint8) ([]*dom.InviteRelation, error) {
	var out []*dom.InviteRelation
	e := r.db.WithContext(c).Table(dom.InviteRelation{}.TableName()).
		Where("inviter_id = ? AND level = ? AND status = 1", inviterID, level).Find(&out).Error
	return out, e
}

func (r *GormRepository) CountRelations(c context.Context, inviterID int64, level uint8) (int64, error) {
	var n int64
	e := r.db.WithContext(c).Table(dom.InviteRelation{}.TableName()).
		Where("inviter_id = ? AND level = ? AND status = 1", inviterID, level).Count(&n).Error
	return n, e
}

func (r *GormRepository) IncreaseInviteCount(c context.Context, userID int64, delta int) error {
	return r.db.WithContext(c).Exec(
		"UPDATE marketing.invites SET invite_count = invite_count + ? WHERE user_id = ?", delta, userID).Error
}

// RewardRow is the admin/user list projection with both parties' usernames.
type RewardRow struct {
	dom.Reward
	Username       string `json:"username"`
	RewardUserName string `json:"rewardUserName"`
}

func (r *GormRepository) CreateRewards(c context.Context, tx *gorm.DB, list []*dom.Reward) error {
	if len(list) == 0 {
		return nil
	}
	db := r.db.WithContext(c)
	if tx != nil {
		db = tx
	}
	return db.Table(dom.Reward{}.TableName()).Create(&list).Error
}

func (r *GormRepository) ListRewards(c context.Context, page, pageSize int, status *uint8, level *uint8, keyword string, rewardUserID int64) ([]*RewardRow, int64, error) {
	if page < 1 {
		page = 1
	}
	if pageSize < 1 {
		pageSize = 20
	}
	q := r.db.WithContext(c).Table(dom.Reward{}.TableName() + " AS rw").
		Joins("LEFT JOIN customer.customers bu ON bu.id = rw.user_id").
		Joins("LEFT JOIN customer.customers ru ON ru.id = rw.reward_user_id").
		Select("rw.*, COALESCE(bu.username, '') AS username, COALESCE(ru.username, '') AS reward_user_name")
	if status != nil {
		q = q.Where("rw.status = ?", *status)
	}
	if level != nil {
		q = q.Where("rw.level = ?", *level)
	}
	if rewardUserID > 0 {
		q = q.Where("rw.reward_user_id = ?", rewardUserID)
	}
	if kw := strings.TrimSpace(keyword); kw != "" {
		q = q.Where("rw.order_no LIKE ? OR bu.username LIKE ? OR ru.username LIKE ?", "%"+kw+"%", "%"+kw+"%", "%"+kw+"%")
	}
	var total int64
	if e := q.Count(&total).Error; e != nil {
		return nil, 0, e
	}
	var out []*RewardRow
	e := q.Order("rw.created_at DESC").Offset((page - 1) * pageSize).Limit(pageSize).Scan(&out).Error
	return out, total, e
}

func (r *GormRepository) FindReward(c context.Context, id int64) (*dom.Reward, error) {
	var v dom.Reward
	e := r.db.WithContext(c).Table(dom.Reward{}.TableName()).Where("id = ?", id).First(&v).Error
	if errors.Is(e, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	return &v, e
}

func (r *GormRepository) UpdateRewardStatus(c context.Context, id int64, from, to uint8, remark string) error {
	q := r.db.WithContext(c).Table(dom.Reward{}.TableName()).Where("id = ? AND status = ?", id, from)
	updates := map[string]any{"status": to}
	if to == dom.RewardSettled {
		updates["settled_at"] = time.Now()
	}
	if strings.TrimSpace(remark) != "" {
		updates["remark"] = remark
	}
	res := q.Updates(updates)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return errors.New("奖励状态已变化，请刷新后重试")
	}
	return nil
}

func (r *GormRepository) ReclaimRewardsByOrder(c context.Context, orderID int64, remark string) (int64, error) {
	res := r.db.WithContext(c).Exec(
		"UPDATE marketing.rewards SET status = ?, remark = ? WHERE order_id = ? AND status IN (?, ?, ?)",
		dom.RewardReclaimed, remark, orderID, dom.RewardPending, dom.RewardSettled, dom.RewardFrozen)
	return res.RowsAffected, res.Error
}

func (r *GormRepository) SumRewardAmountByStatus(c context.Context, rewardUserID int64) (map[uint8]int64, error) {
	var rows []struct {
		Status uint8 `gorm:"column:status"`
		Total  int64 `gorm:"column:total"`
	}
	e := r.db.WithContext(c).Table(dom.Reward{}.TableName()).
		Select("status, COALESCE(SUM(amount), 0) AS total").
		Where("reward_user_id = ? AND status <> ?", rewardUserID, dom.RewardReclaimed).
		Group("status").Scan(&rows).Error
	if e != nil {
		return nil, e
	}
	out := make(map[uint8]int64, len(rows))
	for _, row := range rows {
		out[row.Status] = row.Total
	}
	return out, nil
}

func (r *GormRepository) CreateCoupon(c context.Context, v *dom.Coupon) error {
	return r.db.WithContext(c).Table(dom.Coupon{}.TableName()).Create(v).Error
}

// SumDailySettledRewards counts actual settlements, scoped to their recipient.
// Half-open business-day bounds exclude future, pending, frozen and reclaimed rewards.
func (r *GormRepository) SumDailySettledRewards(c context.Context, userID int64, yesterday, today, tomorrow time.Time) (int64, int64, error) {
	var row struct {
		Today     int64
		Yesterday int64
	}
	err := r.db.WithContext(c).Table(dom.Reward{}.TableName()).
		Select("COALESCE(SUM(CASE WHEN settled_at >= ? THEN amount ELSE 0 END), 0) AS today, COALESCE(SUM(CASE WHEN settled_at < ? THEN amount ELSE 0 END), 0) AS yesterday", today, today).
		Where("reward_user_id = ? AND status = ? AND settled_at >= ? AND settled_at < ?", userID, dom.RewardSettled, yesterday, tomorrow).
		Scan(&row).Error
	return row.Today, row.Yesterday, err
}

func (r *GormRepository) UpdateCoupon(c context.Context, v *dom.Coupon) error {
	return r.db.WithContext(c).Table(dom.Coupon{}.TableName()).Save(v).Error
}

func (r *GormRepository) FindCoupon(c context.Context, id int64) (*dom.Coupon, error) {
	var v dom.Coupon
	e := r.db.WithContext(c).Table(dom.Coupon{}.TableName()).Where("id = ?", id).First(&v).Error
	if errors.Is(e, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	return &v, e
}

func (r *GormRepository) FindCouponByClaimCode(c context.Context, code string) (*dom.Coupon, error) {
	var v dom.Coupon
	e := r.db.WithContext(c).Table(dom.Coupon{}.TableName()).Where("claim_code = ?", code).First(&v).Error
	if errors.Is(e, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	return &v, e
}

func (r *GormRepository) ListCoupons(c context.Context, page, pageSize int, status *uint8, keyword string) ([]*dom.Coupon, int64, error) {
	if page < 1 {
		page = 1
	}
	if pageSize < 1 {
		pageSize = 20
	}
	q := r.db.WithContext(c).Table(dom.Coupon{}.TableName())
	if status != nil {
		q = q.Where("status = ?", *status)
	}
	if kw := strings.TrimSpace(keyword); kw != "" {
		q = q.Where("name LIKE ? OR claim_code LIKE ?", "%"+kw+"%", "%"+kw+"%")
	}
	var total int64
	if e := q.Count(&total).Error; e != nil {
		return nil, 0, e
	}
	var out []*dom.Coupon
	e := q.Order("created_at DESC").Offset((page - 1) * pageSize).Limit(pageSize).Scan(&out).Error
	return out, total, e
}

func (r *GormRepository) CountUserCoupons(c context.Context, userID, couponID int64) (int64, error) {
	var n int64
	e := r.db.WithContext(c).Table(dom.UserCoupon{}.TableName()).
		Where("user_id = ? AND coupon_id = ? AND status <> ?", userID, couponID, dom.UserCouponDiscarded).Count(&n).Error
	return n, e
}

// ClaimCouponTx atomically claims one coupon instance. The conditional
// UPDATE on claimed_count prevents over-issue under concurrency.
func (r *GormRepository) ClaimCouponTx(c context.Context, tx *gorm.DB, userID int64, coupon *dom.Coupon, source uint8) (*dom.UserCoupon, error) {
	db := tx
	if db == nil {
		db = r.db.WithContext(c)
	}
	res := db.Exec(
		"UPDATE marketing.coupons SET claimed_count = claimed_count + 1 WHERE id = ? AND status = 1 AND (total_count = -1 OR claimed_count < total_count)",
		coupon.ID)
	if res.Error != nil {
		return nil, res.Error
	}
	if res.RowsAffected == 0 {
		return nil, errors.New("该优惠券已领完或已停用")
	}
	now := time.Now()
	v := &dom.UserCoupon{
		UserID:   userID,
		CouponID: coupon.ID,
		Code:     "CPN" + strings.ToUpper(dom.RandToken(10)),
		Source:   source,
		Status:   dom.UserCouponUnused,
		ExpireAt: now.AddDate(0, 0, int(coupon.ValidDays)),
	}
	if e := db.Table(dom.UserCoupon{}.TableName()).Create(v).Error; e != nil {
		return nil, e
	}
	return v, nil
}

// UserCouponRow joins the template for display.
type UserCouponRow struct {
	dom.UserCoupon
	Name         string  `json:"name"`
	Type         uint8   `json:"type"`
	FaceValue    int64   `json:"faceValue"`
	DiscountRate float64 `json:"discountRate"`
	MaxDiscount  int64   `json:"maxDiscount"`
	MinAmount    int64   `json:"minAmount"`
}

func (r *GormRepository) ListUserCoupons(c context.Context, userID int64, status *uint8, page, pageSize int) ([]*UserCouponRow, int64, error) {
	if page < 1 {
		page = 1
	}
	if pageSize < 1 {
		pageSize = 20
	}
	q := r.db.WithContext(c).Table(dom.UserCoupon{}.TableName()+" AS uc").
		Joins("JOIN marketing.coupons cp ON cp.id = uc.coupon_id").
		Select("uc.*, cp.name AS name, cp.type AS type, cp.face_value AS face_value, cp.discount_rate AS discount_rate, cp.max_discount AS max_discount, cp.min_amount AS min_amount").
		Where("uc.user_id = ?", userID)
	if status != nil {
		q = q.Where("uc.status = ?", *status)
	}
	var total int64
	if e := q.Count(&total).Error; e != nil {
		return nil, 0, e
	}
	var out []*UserCouponRow
	e := q.Order("uc.created_at DESC").Offset((page - 1) * pageSize).Limit(pageSize).Scan(&out).Error
	return out, total, e
}

func (r *GormRepository) FindUserCoupon(c context.Context, id int64) (*dom.UserCoupon, error) {
	var v dom.UserCoupon
	e := r.db.WithContext(c).Table(dom.UserCoupon{}.TableName()).Where("id = ?", id).First(&v).Error
	if errors.Is(e, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	return &v, e
}

// ClaimableCouponRow is the claim-center projection with the user's claimed count.
type ClaimableCouponRow struct {
	dom.Coupon
	Remaining   int64 `json:"remaining"`
	UserClaimed int64 `json:"userClaimed"`
}

func (r *GormRepository) ListClaimableCoupons(c context.Context, userID int64) ([]*ClaimableCouponRow, error) {
	var rows []*ClaimableCouponRow
	e := r.db.WithContext(c).Table(dom.Coupon{}.TableName()+" AS cp").
		Select("cp.*, (CASE WHEN cp.total_count = -1 THEN 999999 ELSE cp.total_count - cp.claimed_count END) AS remaining, "+
			"(SELECT COUNT(*) FROM marketing.user_coupons uc WHERE uc.coupon_id = cp.id AND uc.user_id = ? AND uc.status <> 3) AS user_claimed", userID).
		Where("cp.status = 1 AND (cp.end_at IS NULL OR cp.end_at > NOW()) AND (cp.claim_code IS NULL OR cp.claim_code NOT LIKE 'PROMO-PRIVATE-%')").
		Order("cp.created_at DESC").Scan(&rows).Error
	return rows, e
}

func (r *GormRepository) ListUsableCoupons(c context.Context, userID int64, amountCents int64) ([]*UserCouponRow, error) {
	var out []*UserCouponRow
	q := r.db.WithContext(c).Table(dom.UserCoupon{}.TableName()+" AS uc").
		Joins("JOIN marketing.coupons cp ON cp.id = uc.coupon_id").
		Select("uc.*, cp.name AS name, cp.type AS type, cp.face_value AS face_value, cp.discount_rate AS discount_rate, cp.max_discount AS max_discount, cp.min_amount AS min_amount").
		Where("uc.user_id = ? AND uc.status = 0 AND uc.expire_at > NOW() AND cp.status = 1", userID)
	if amountCents > 0 {
		q = q.Where("cp.min_amount <= ?", amountCents)
	}
	e := q.Order("uc.expire_at ASC").Scan(&out).Error
	return out, e
}

// MarkUserCouponUsedTx conditionally flips the instance to used inside the
// caller's transaction; RowsAffected = 0 means it was already used/expired.
func (r *GormRepository) MarkUserCouponUsedTx(c context.Context, tx *gorm.DB, userCouponID, userID, orderID int64) error {
	res := tx.WithContext(c).Exec(
		"UPDATE marketing.user_coupons SET status = 1, order_id = ?, used_at = NOW() WHERE id = ? AND user_id = ? AND status = 0 AND expire_at > NOW()",
		orderID, userCouponID, userID)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return errors.New("优惠券不可用（已使用或已过期）")
	}
	return nil
}

// ReleaseUserCouponTx returns the coupon bound to a cancelled order to unused.
func (r *GormRepository) ReleaseUserCouponTx(c context.Context, tx *gorm.DB, orderID int64) error {
	if tx == nil {
		return errors.New("释放优惠券需要事务")
	}
	// Capture and lock the association before clearing order_id. A repeated
	// release sees no used rows and cannot decrement the aggregate twice.
	var bound []dom.UserCoupon
	if err := tx.WithContext(c).Table(dom.UserCoupon{}.TableName()).
		Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("order_id = ? AND status = 1", orderID).Order("coupon_id, id").Find(&bound).Error; err != nil {
		return err
	}
	for _, coupon := range bound {
		res := tx.WithContext(c).Exec(
			"UPDATE marketing.user_coupons SET status = 0, order_id = NULL, used_at = NULL WHERE id = ? AND order_id = ? AND status = 1", coupon.ID, orderID)
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected != 1 {
			return errors.New("优惠券状态已变更")
		}
		counter := tx.WithContext(c).Exec(
			"UPDATE marketing.coupons SET used_count = used_count - 1 WHERE id = ? AND used_count > 0", coupon.CouponID)
		if counter.Error != nil {
			return counter.Error
		}
		if counter.RowsAffected != 1 {
			return errors.New("优惠券核销统计不一致，请联系管理员")
		}
	}
	return nil
}

func (r *GormRepository) ExpireOverdueCoupons(c context.Context) error {
	return r.db.WithContext(c).Exec(
		"UPDATE marketing.user_coupons SET status = 2 WHERE status = 0 AND expire_at <= NOW()").Error
}

func (r *GormRepository) InsertCouponLog(c context.Context, tx *gorm.DB, l *dom.CouponLog) error {
	db := r.db.WithContext(c)
	if tx != nil {
		db = tx
	}
	return db.Table(dom.CouponLog{}.TableName()).Create(l).Error
}

// IssueCouponTx issues the coupon to the given users inside one transaction.
func (r *GormRepository) IssueCouponTx(c context.Context, tx *gorm.DB, coupon *dom.Coupon, userIDs []int64, source uint8, operator string) (int, error) {
	db := tx
	if db == nil {
		db = r.db.WithContext(c)
	}
	issued := 0
	for _, userID := range userIDs {
		var held int64
		if e := db.Table(dom.UserCoupon{}.TableName()).
			Where("user_id = ? AND coupon_id = ? AND status <> ?", userID, coupon.ID, dom.UserCouponDiscarded).Count(&held).Error; e != nil {
			return issued, e
		}
		if coupon.PerUserLimit > 0 && held >= int64(coupon.PerUserLimit) {
			continue
		}
		created, e := r.ClaimCouponTx(c, db, userID, coupon, source)
		if e != nil {
			return issued, e
		}
		uid := userID
		logRow := &dom.CouponLog{CouponID: coupon.ID, UserCouponID: &created.ID, UserID: &uid, Action: 2, Operator: dom.StrPtr(operator), Remark: dom.StrPtr("定向发放")}
		if e := db.Table(dom.CouponLog{}.TableName()).Create(logRow).Error; e != nil {
			return issued, e
		}
		issued++
	}
	return issued, nil
}

func (r *GormRepository) CreateSalesUser(c context.Context, v *dom.SalesUser) error {
	return r.db.WithContext(c).Table(dom.SalesUser{}.TableName()).Create(v).Error
}

func (r *GormRepository) UpdateSalesUser(c context.Context, v *dom.SalesUser) error {
	return r.db.WithContext(c).Table(dom.SalesUser{}.TableName()).Save(v).Error
}

func (r *GormRepository) FindSalesUser(c context.Context, id int64) (*dom.SalesUser, error) {
	var v dom.SalesUser
	e := r.db.WithContext(c).Table(dom.SalesUser{}.TableName()).Where("id = ?", id).First(&v).Error
	if errors.Is(e, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	return &v, e
}

func (r *GormRepository) FindSalesUserByUsername(c context.Context, username string) (*dom.SalesUser, error) {
	var v dom.SalesUser
	e := r.db.WithContext(c).Table(dom.SalesUser{}.TableName()).Where("username = ?", username).First(&v).Error
	if errors.Is(e, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	return &v, e
}

func (r *GormRepository) FindSalesUserByStaffNo(c context.Context, staffNo string) (*dom.SalesUser, error) {
	var v dom.SalesUser
	e := r.db.WithContext(c).Table(dom.SalesUser{}.TableName()).Where("staff_no = ?", staffNo).First(&v).Error
	if errors.Is(e, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	return &v, e
}

func (r *GormRepository) ListSalesUsers(c context.Context, page, pageSize int, keyword string) ([]*dom.SalesUser, int64, error) {
	if page < 1 {
		page = 1
	}
	if pageSize < 1 {
		pageSize = 20
	}
	q := r.db.WithContext(c).Table(dom.SalesUser{}.TableName())
	if kw := strings.TrimSpace(keyword); kw != "" {
		q = q.Where("staff_no LIKE ? OR username LIKE ? OR name LIKE ?", "%"+kw+"%", "%"+kw+"%", "%"+kw+"%")
	}
	var total int64
	if e := q.Count(&total).Error; e != nil {
		return nil, 0, e
	}
	var out []*dom.SalesUser
	e := q.Order("created_at DESC").Offset((page - 1) * pageSize).Limit(pageSize).Scan(&out).Error
	return out, total, e
}

func (r *GormRepository) FindVisibilityRule(c context.Context, salesID int64) (*dom.SalesVisibilityRule, error) {
	var v dom.SalesVisibilityRule
	e := r.db.WithContext(c).Table(dom.SalesVisibilityRule{}.TableName()).Where("sales_id = ?", salesID).First(&v).Error
	if errors.Is(e, gorm.ErrRecordNotFound) {
		if salesID == 0 {
			return nil, nil
		}
		// 回退到全局默认规则
		return r.FindVisibilityRule(c, 0)
	}
	if e != nil {
		return nil, e
	}
	return &v, nil
}

func (r *GormRepository) SaveVisibilityRule(c context.Context, v *dom.SalesVisibilityRule) error {
	var count int64
	if e := r.db.WithContext(c).Table(dom.SalesVisibilityRule{}.TableName()).Where("sales_id = ?", v.SalesID).Count(&count).Error; e != nil {
		return e
	}
	if count > 0 {
		return r.db.WithContext(c).Table(dom.SalesVisibilityRule{}.TableName()).Where("sales_id = ?", v.SalesID).Updates(map[string]any{
			"can_view_mobile": v.CanViewMobile, "can_view_email": v.CanViewEmail,
			"can_view_orders": v.CanViewOrders, "can_view_spend": v.CanViewSpend,
			"can_view_balance": v.CanViewBalance, "can_view_realname": v.CanViewRealname,
			"can_followup": v.CanFollowup,
		}).Error
	}
	if v.ID == 0 {
		v.ID = dom.NewID()
	}
	return r.db.WithContext(c).Table(dom.SalesVisibilityRule{}.TableName()).Create(v).Error
}

func (r *GormRepository) BindSalesCustomer(c context.Context, v *dom.SalesCustomer) error {
	var count int64
	if e := r.db.WithContext(c).Table(dom.SalesCustomer{}.TableName()).
		Where("sales_id = ? AND customer_id = ?", v.SalesID, v.CustomerID).Count(&count).Error; e != nil {
		return e
	}
	if count > 0 {
		return r.db.WithContext(c).Exec(
			"UPDATE system.sales_customers SET status = 1, transferred_to = NULL, transferred_at = NULL WHERE sales_id = ? AND customer_id = ?",
			v.SalesID, v.CustomerID).Error
	}
	if v.AssignedAt.IsZero() {
		v.AssignedAt = time.Now()
	}
	return r.db.WithContext(c).Table(dom.SalesCustomer{}.TableName()).Create(v).Error
}

func (r *GormRepository) UnbindSalesCustomer(c context.Context, salesID, customerID int64) error {
	return r.db.WithContext(c).Exec(
		"UPDATE system.sales_customers SET status = 2, transferred_at = NOW() WHERE sales_id = ? AND customer_id = ?",
		salesID, customerID).Error
}

func (r *GormRepository) FindSalesOwner(c context.Context, customerID int64) (int64, error) {
	var row struct {
		SalesID int64 `gorm:"column:sales_id"`
	}
	e := r.db.WithContext(c).Table(dom.SalesCustomer{}.TableName()).
		Select("sales_id").Where("customer_id = ? AND status = 1", customerID).First(&row).Error
	if errors.Is(e, gorm.ErrRecordNotFound) {
		return 0, nil
	}
	if e != nil {
		return 0, e
	}
	return row.SalesID, nil
}

func (r *GormRepository) GetSalesCustomer(c context.Context, salesID, customerID int64) (*dom.SalesCustomerRow, error) {
	var out *dom.SalesCustomerRow
	e := r.salesCustomerQuery(c, salesID).
		Where("sc.customer_id = ?", customerID).Scan(&out).Error
	if e != nil {
		return nil, e
	}
	return out, nil
}

// salesCustomerQuery builds the shared customer-with-aggregates projection.
func (r *GormRepository) salesCustomerQuery(c context.Context, salesID int64) *gorm.DB {
	paidStatuses := "(2, 5, 6)"
	return r.db.WithContext(c).Table("system.sales_customers AS sc").
		Joins("JOIN customer.customers cu ON cu.id = sc.customer_id AND cu.deleted_at IS NULL").
		Select("cu.id AS customer_id, cu.username, cu.email, cu.phone, cu.real_name AS real_name, cu.status, cu.created_at, "+
			"(SELECT COUNT(*) FROM `order`.orders o WHERE o.customer_id = cu.id AND o.status IN "+paidStatuses+") AS order_count, "+
			"(SELECT COALESCE(SUM(o.pay_amount), 0) FROM `order`.orders o WHERE o.customer_id = cu.id AND o.status IN "+paidStatuses+") AS total_spend, "+
			"(SELECT MAX(o.paid_at) FROM `order`.orders o WHERE o.customer_id = cu.id AND o.status IN "+paidStatuses+") AS last_pay_at, "+
			"(SELECT CAST(ba.balance*100 AS SIGNED) FROM billing.accounts ba WHERE ba.customer_id = cu.id LIMIT 1) AS balance").
		Where("sc.sales_id = ? AND sc.status = 1", salesID)
}

func (r *GormRepository) ListSalesCustomers(c context.Context, salesID int64, page, pageSize int, keyword string) ([]*dom.SalesCustomerRow, int64, error) {
	if page < 1 {
		page = 1
	}
	if pageSize < 1 {
		pageSize = 20
	}
	base := r.salesCustomerQuery(c, salesID)
	if kw := strings.TrimSpace(keyword); kw != "" {
		base = base.Where("cu.username LIKE ? OR cu.email LIKE ?", "%"+kw+"%", "%"+kw+"%")
	}
	var total int64
	if e := base.Count(&total).Error; e != nil {
		return nil, 0, e
	}
	var out []*dom.SalesCustomerRow
	e := base.Order("sc.assigned_at DESC").Offset((page - 1) * pageSize).Limit(pageSize).Scan(&out).Error
	return out, total, e
}

func (r *GormRepository) CreateFollowup(c context.Context, v *dom.SalesFollowup) error {
	return r.db.WithContext(c).Table(dom.SalesFollowup{}.TableName()).Create(v).Error
}

func (r *GormRepository) ListFollowups(c context.Context, salesID, customerID int64, page, pageSize int) ([]*dom.SalesFollowup, int64, error) {
	if page < 1 {
		page = 1
	}
	if pageSize < 1 {
		pageSize = 20
	}
	q := r.db.WithContext(c).Table(dom.SalesFollowup{}.TableName()).Where("sales_id = ?", salesID)
	if customerID > 0 {
		q = q.Where("customer_id = ?", customerID)
	}
	var total int64
	if e := q.Count(&total).Error; e != nil {
		return nil, 0, e
	}
	var out []*dom.SalesFollowup
	e := q.Order("created_at DESC").Offset((page - 1) * pageSize).Limit(pageSize).Scan(&out).Error
	return out, total, e
}

func (r *GormRepository) FindCustomerBasic(c context.Context, id int64) (*CustomerBasic, error) {
	var v CustomerBasic
	e := r.db.WithContext(c).Table("customer.customers").Where("id = ? AND deleted_at IS NULL", id).First(&v).Error
	if errors.Is(e, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if e != nil {
		return nil, e
	}
	return &v, nil
}

func (r *GormRepository) FindCustomerByUsernameOrEmail(c context.Context, account string) (*CustomerBasic, error) {
	var v CustomerBasic
	e := r.db.WithContext(c).Table("customer.customers").
		Where("(username = ? OR email = ?) AND deleted_at IS NULL", account, account).First(&v).Error
	if errors.Is(e, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if e != nil {
		return nil, e
	}
	return &v, nil
}

func (r *GormRepository) ListCustomerOrders(c context.Context, customerID int64, page, pageSize int) ([]*dom.CustomerOrderRow, int64, error) {
	if page < 1 {
		page = 1
	}
	if pageSize < 1 {
		pageSize = 20
	}
	q := r.db.WithContext(c).Table("order.orders").Where("customer_id = ?", customerID)
	var total int64
	if e := q.Count(&total).Error; e != nil {
		return nil, 0, e
	}
	var out []*dom.CustomerOrderRow
	e := q.Select("id, order_no, total_amount, discount_amount, coupon_amount, pay_amount, status, created_at, paid_at").
		Order("created_at DESC").Offset((page - 1) * pageSize).Limit(pageSize).Scan(&out).Error
	return out, total, e
}

func (r *GormRepository) SumInviteePaidOrders(c context.Context, inviteeIDs []int64) (int64, int64, error) {
	if len(inviteeIDs) == 0 {
		return 0, 0, nil
	}
	var row struct {
		Spend int64 `gorm:"column:spend"`
		Cnt   int64 `gorm:"column:cnt"`
	}
	e := r.db.WithContext(c).Table("order.orders").
		Select("COALESCE(SUM(pay_amount), 0) AS spend, COUNT(*) AS cnt").
		Where("customer_id IN ? AND status IN (2, 5, 6)", inviteeIDs).Scan(&row).Error
	return row.Spend, row.Cnt, e
}

func (r *GormRepository) SumInviteeRecharges(c context.Context, inviteeIDs []int64) (int64, error) {
	if len(inviteeIDs) == 0 {
		return 0, nil
	}
	var row struct {
		Total int64 `gorm:"column:total"`
	}
	e := r.db.WithContext(c).Table("payment.recharge_orders").
		Select("COALESCE(SUM(amount), 0) AS total").
		Where("customer_id IN ? AND status = 2", inviteeIDs).Scan(&row).Error
	return row.Total, e
}
