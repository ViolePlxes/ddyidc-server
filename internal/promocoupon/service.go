package promocoupon

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	balance "ddyidc/internal/billing/balance"
	md "ddyidc/internal/marketing/domain"
	mr "ddyidc/internal/marketing/repository"
	od "ddyidc/internal/order/domain"
	or "ddyidc/internal/order/repository"
	"ddyidc/internal/points"
	"ddyidc/internal/shared/snowflake"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

var ErrForbidden = errors.New("无权访问该优惠券记录")
var ErrDisabled = errors.New("推广发券暂未开放")
var requestPattern = regexp.MustCompile(`^[A-Za-z0-9-]{16,64}$`)

type Service struct {
	db          *gorm.DB
	points      *points.Service
	key         []byte
	now         func() time.Time
	applyCoupon func(context.Context, *gorm.DB, int64, int64, int64, int64) (int64, error)
}

func (s *Service) SetCouponApply(fn func(context.Context, *gorm.DB, int64, int64, int64, int64) (int64, error)) {
	s.applyCoupon = fn
}
func New(db *gorm.DB, p *points.Service, key []byte) *Service {
	return &Service{db: db, points: p, key: key, now: func() time.Time { return time.Now().In(time.FixedZone("Asia/Shanghai", 28800)) }}
}
func (s *Service) Config(ctx context.Context) (*Config, error) {
	var c Config
	e := s.db.WithContext(ctx).Take(&c, "id=1").Error
	return &c, e
}
func (s *Service) Templates(ctx context.Context, admin bool) ([]Template, error) {
	out := []Template{}
	q := s.db.WithContext(ctx)
	if !admin {
		q = q.Where("enabled=1")
	}
	e := q.Order("id").Find(&out).Error
	return out, e
}
func active(tx *gorm.DB, id int64) error {
	var n int64
	if e := tx.Table("customer.customers").Where("id=? AND status=1 AND deleted_at IS NULL", id).Count(&n).Error; e != nil {
		return e
	}
	if n != 1 {
		return errors.New("客户账号已停用或不存在")
	}
	return nil
}
func relation(tx *gorm.DB, owner, recipient int64) error {
	if owner == recipient || owner <= 0 || recipient <= 0 {
		return errors.New("仅可给直接邀请客户发券")
	}
	if e := active(tx, recipient); e != nil {
		return e
	}
	var n int64
	e := tx.Table("marketing.invite_relations").Where("inviter_id=? AND invitee_id=? AND level=1 AND status=1", owner, recipient).Count(&n).Error
	if e != nil {
		return e
	}
	if n != 1 {
		return errors.New("客户不属于当前直接邀请关系")
	}
	return nil
}
func normalize(in *Input) error {
	if !requestPattern.MatchString(in.RequestID) {
		return errors.New("请求标识无效")
	}
	ids := []int64{}
	seen := map[int64]bool{}
	for _, v := range in.RecipientIDs {
		id, e := strconv.ParseInt(v, 10, 64)
		if e != nil || id <= 0 || seen[id] {
			return errors.New("客户ID无效或重复")
		}
		ids = append(ids, id)
		seen[id] = true
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	in.Recipients = ids
	in.RecipientIDs = make([]string, len(ids))
	for i, id := range ids {
		in.RecipientIDs[i] = strconv.FormatInt(id, 10)
	}
	if in.Mode != "direct" && in.Mode != "offer" {
		return errors.New("发放方式无效")
	}
	if in.Source != "points" && in.Source != "balance" {
		return errors.New("资产来源无效")
	}
	if in.Mode == "direct" {
		if len(ids) == 0 {
			return errors.New("请选择客户")
		}
		in.Quantity = len(ids)
	} else if len(ids) > 0 {
		return errors.New("专属活动不需要指定客户")
	}
	return nil
}
func fingerprint(in Input) string {
	in.Quote = ""
	b, _ := json.Marshal(in)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

type quoteClaims struct {
	User     int64
	Hash     string
	Version  int64
	Template Template
	Expires  int64
}

func (s *Service) sign(v quoteClaims) string {
	b, _ := json.Marshal(v)
	a := base64.RawURLEncoding.EncodeToString(b)
	m := hmac.New(sha256.New, s.key)
	m.Write([]byte(a))
	return a + "." + hex.EncodeToString(m.Sum(nil))
}
func (s *Service) verify(token string, user int64, in Input) (quoteClaims, error) {
	var v quoteClaims
	parts := strings.Split(token, ".")
	if len(parts) != 2 {
		return v, errors.New("请先获取费用预览")
	}
	m := hmac.New(sha256.New, s.key)
	m.Write([]byte(parts[0]))
	sig, e := hex.DecodeString(parts[1])
	if e != nil || !hmac.Equal(sig, m.Sum(nil)) {
		return v, errors.New("费用预览无效")
	}
	b, e := base64.RawURLEncoding.DecodeString(parts[0])
	if e != nil || json.Unmarshal(b, &v) != nil || v.User != user || v.Hash != fingerprint(in) || v.Expires <= s.now().Unix() {
		return v, errors.New("费用预览已过期或内容改变，请重新计算")
	}
	return v, nil
}
func costs(t Template, c Config, in Input) (int64, balance.Money, error) {
	if in.Quantity < 1 || in.Quantity > c.BatchMax || t.Face <= 0 || t.Face > c.FaceMax || t.Minimum <= t.Face || t.ValidDays < 1 || t.ValidDays > c.ValidDaysMax || c.PointsPerYuan < 1 || c.PointsPerYuan > 1000000 {
		return 0, 0, errors.New("券规则或数量超出平台限制")
	}
	p := (int64(t.Face)*c.PointsPerYuan + 99) / 100
	if in.ClaimPoints < 0 || in.ClaimPoints > p || (in.Mode == "direct" && in.ClaimPoints != 0) || (in.Source == "balance" && in.ClaimPoints != 0) {
		return 0, 0, errors.New("领取积分不符合规则")
	}
	if in.Source == "points" {
		return (p - in.ClaimPoints) * int64(in.Quantity), 0, nil
	}
	return 0, t.Face * balance.Money(in.Quantity), nil
}
func (s *Service) validate(tx *gorm.DB, user int64, in Input) (*Config, *Template, error) {
	c := &Config{}
	e := tx.Take(c, "id=1").Error
	if e != nil {
		return nil, nil, e
	}
	if !c.Enabled || (in.Mode == "direct" && !c.DirectEnabled) || (in.Mode == "offer" && !c.OfferEnabled) || (in.Source == "points" && !c.PointsEnabled) || (in.Source == "balance" && !c.BalanceEnabled) {
		return nil, nil, ErrDisabled
	}
	if e = active(tx, user); e != nil {
		return nil, nil, e
	}
	var t Template
	if e = tx.Take(&t, "id=? AND enabled=1", in.TemplateID).Error; e != nil {
		return nil, nil, errors.New("模板不可用")
	}
	if _, _, e = costs(t, *c, in); e != nil {
		return nil, nil, e
	}
	for _, id := range in.Recipients {
		if e = relation(tx, user, id); e != nil {
			return nil, nil, e
		}
	}
	var products []string
	if json.Unmarshal(c.ProductIDs, &products) != nil || len(products) == 0 {
		return nil, nil, errors.New("平台尚未配置优惠券适用商品")
	}
	return c, &t, nil
}
func (s *Service) Quote(ctx context.Context, user int64, in Input) (*Quote, error) {
	if e := normalize(&in); e != nil {
		return nil, e
	}
	c, t, e := s.validate(s.db.WithContext(ctx), user, in)
	if e != nil {
		return nil, e
	}
	p, b, e := costs(*t, *c, in)
	if e != nil {
		return nil, e
	}
	expires := s.now().Add(5 * time.Minute)
	return &Quote{Token: s.sign(quoteClaims{user, fingerprint(in), c.Version, *t, expires.Unix()}), ExpiresAt: expires, PointCost: p, BalanceCost: b, Quantity: in.Quantity, Face: t.Face, Minimum: t.Minimum, ClaimPoints: in.ClaimPoints, Name: t.Name, ReviewRequired: c.ReviewRequired}, nil
}
func (s *Service) event(tx *gorm.DB, batch, unit int64, a Actor, name, key string, amount balance.Money, p int64) error {
	if len([]rune(a.Notes)) > 500 || len(a.IP) > 64 {
		return errors.New("审计说明过长")
	}
	return tx.Create(&Event{ID: snowflake.GenID(), BatchID: batch, UnitID: unit, ActorID: a.ID, ActorRole: a.Role, OperatorIP: a.IP, Event: name, RequestID: key, Amount: amount, Points: p, Notes: a.Notes, CreatedAt: s.now()}).Error
}
func (s *Service) Create(ctx context.Context, a Actor, in Input) (*Batch, error) {
	if e := normalize(&in); e != nil {
		return nil, e
	}
	var result Batch
	e := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if e := lockOwner(tx, a.ID); e != nil {
			return e
		}
		prior := tx.Where("owner_id=? AND request_id=?", a.ID, in.RequestID).Take(&result).Error
		if prior == nil {
			if result.RequestHash != fingerprint(in) {
				return errors.New("请求标识已用于其他内容")
			}
			return nil
		}
		if prior != gorm.ErrRecordNotFound {
			return prior
		}
		q, e := s.verify(in.Quote, a.ID, in)
		if e != nil {
			return e
		}
		c, t, e := s.validate(tx, a.ID, in)
		if e != nil {
			return e
		}
		if q.Version != c.Version || !sameTemplate(q.Template, *t) {
			return errors.New("平台规则已更新，请重新预览")
		}
		p, b, e := costs(*t, *c, in)
		if e != nil {
			return e
		}
		if e = s.daily(tx, a.ID, *c, in.Quantity, t.Face); e != nil {
			return e
		}
		snap, _ := json.Marshal(c)
		ids, _ := json.Marshal(in.Recipients)
		now := s.now()
		status := "awaiting_payment"
		if c.ReviewRequired {
			status = "pending_review"
		}
		result = Batch{ID: snowflake.GenID(), OwnerID: a.ID, RequestID: in.RequestID, RequestHash: fingerprint(in), OrderID: snowflake.GenID(), Name: t.Name, Mode: in.Mode, Source: in.Source, Quantity: in.Quantity, Face: t.Face, Minimum: t.Minimum, PointCost: p, ClaimPoints: in.ClaimPoints, BalanceCost: b, RuleSnapshot: snap, Recipients: ids, Status: status, ExpiresAt: now.AddDate(0, 0, t.ValidDays), CreatedAt: now, UpdatedAt: now}
		// Legacy platform order uses integer cents; module/ledger boundaries use yuan.
		order := od.Order{ID: result.OrderID, OrderNo: fmt.Sprintf("PC%d", result.ID), CustomerID: a.ID, TotalAmount: int64(b), PayAmount: int64(b), Status: od.OrderStatusPending, CreatedAt: now, UpdatedAt: now}
		item := od.OrderItem{ID: snowflake.GenID(), OrderID: order.ID, ProductName: t.Name, ModuleType: "promo_coupon", ConfigSnapshot: fmt.Sprintf(`{"batchId":"%d","asset":"%s"}`, result.ID, in.Source), Qty: 1, UnitPrice: int64(b), Subtotal: int64(b), CreatedAt: now, UpdatedAt: now}
		if e = tx.Create(&result).Error; e != nil {
			return e
		}
		if e = tx.Create(&order).Error; e != nil {
			return e
		}
		if e = tx.Create(&item).Error; e != nil {
			return e
		}
		return s.event(tx, result.ID, 0, a, "created", fmt.Sprintf("create:%d", result.ID), b, p)
	})
	return &result, e
}
func lockOwner(tx *gorm.DB, id int64) error {
	var owner struct{ ID int64 }
	if e := tx.Table("customer.customers").Select("id").Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=? AND status=1 AND deleted_at IS NULL", id).Take(&owner).Error; e != nil {
		return errors.New("账号不可操作")
	}
	return nil
}
func (s *Service) daily(tx *gorm.DB, owner int64, c Config, quantity int, face balance.Money) error {
	now := s.now()
	start := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	var v struct {
		Count  int64
		Budget balance.Money
	}
	if e := tx.Model(&Batch{}).Where("owner_id=? AND created_at>=? AND created_at<? AND status NOT IN ('rejected','cancelled')", owner, start, start.AddDate(0, 0, 1)).Select("COALESCE(SUM(quantity),0) AS count,COALESCE(SUM(face*quantity),0) AS budget").Scan(&v).Error; e != nil {
		return e
	}
	if v.Count+int64(quantity) > int64(c.DailyCount) || v.Budget+face*balance.Money(quantity) > c.DailyBudget {
		return errors.New("今日发券数量或预算已达上限")
	}
	return nil
}
func (s *Service) Fund(ctx context.Context, a Actor, id int64) (*Batch, error) {
	var b Batch
	e := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if e := lockOwner(tx, a.ID); e != nil {
			return e
		}
		if e := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Take(&b, "id=? AND owner_id=?", id, a.ID).Error; e != nil {
			return ErrForbidden
		}
		if b.Status == "active" || b.Status == "closed" {
			return nil
		}
		if b.Status != "awaiting_payment" {
			return errors.New("该批次尚未通过审核或不可支付")
		}
		c := &Config{}
		e := tx.Take(c, "id=1").Error
		if e != nil {
			return e
		}
		if !c.Enabled || (b.Mode == "direct" && !c.DirectEnabled) || (b.Mode == "offer" && !c.OfferEnabled) || (b.Source == "points" && !c.PointsEnabled) || (b.Source == "balance" && !c.BalanceEnabled) {
			return ErrDisabled
		}
		if !b.ExpiresAt.After(s.now()) {
			return errors.New("该批次已到期，请新建批次")
		}
		var ids []int64
		if json.Unmarshal(b.Recipients, &ids) != nil {
			return errors.New("客户快照无效")
		}
		for _, recipient := range ids {
			if e = relation(tx, b.OwnerID, recipient); e != nil {
				return e
			}
		}
		var order od.Order
		if e = tx.Clauses(clause.Locking{Strength: "UPDATE"}).Take(&order, "id=? AND customer_id=? AND status=1", b.OrderID, b.OwnerID).Error; e != nil {
			return errors.New("订单当前不可支付")
		}
		ref := fmt.Sprintf("batch-%d", b.ID)
		if b.Source == "balance" {
			if _, e = balance.New(tx).Change(ctx, balance.ChangeRequest{CustomerID: b.OwnerID, Type: "promo_coupon_pay", Amount: (-b.BalanceCost).String(), RefType: "promo_coupon_batch", RefID: b.ID, Channel: "promo_coupon", RequestID: ref, Notes: "推广优惠券余额出资"}); e != nil {
				return e
			}
		} else if b.PointCost > 0 {
			if e = s.points.SpendModuleTx(ctx, tx, b.OwnerID, b.PointCost, ref); e != nil {
				return e
			}
		}
		template := md.Coupon{Name: "推广专属券 · " + b.Name, Type: 1, FaceValue: int64(b.Face), MinAmount: int64(b.Minimum), TotalCount: int32(b.Quantity), PerUserLimit: 1, ValidDays: 1, Status: 1, EndAt: &b.ExpiresAt, ClaimCode: md.StrPtr(fmt.Sprintf("PROMO-PRIVATE-%d", b.ID))}
		if e = tx.Create(&template).Error; e != nil {
			return e
		}
		b.CouponID = template.ID
		for i := 0; i < b.Quantity; i++ {
			unit := Unit{ID: snowflake.GenID(), BatchID: b.ID, Status: "stock", OwnerPoints: b.PointCost / int64(b.Quantity), OwnerBalance: b.BalanceCost / balance.Money(b.Quantity), DebitRef: ref, CreatedAt: s.now(), UpdatedAt: s.now()}
			if b.Mode == "direct" {
				unit.RecipientID = ids[i]
				if e = s.issue(tx, &b, &unit); e != nil {
					return e
				}
			}
			if e = tx.Create(&unit).Error; e != nil {
				return e
			}
		}
		payType := uint8(3)
		if b.Source == "points" {
			payType = 5
		}
		now := s.now()
		var accepted bool
		if b.Source == "balance" {
			accepted, e = or.AcceptBalancePaymentTx(tx, order.ID, b.OwnerID, int64(b.BalanceCost), now)
		} else {
			accepted, e = or.AcceptPaymentTx(tx, order.ID, b.OwnerID, 0, payType, now)
		}
		if e != nil {
			return e
		}
		if !accepted {
			return errors.New("订单状态已变化")
		}
		accepted, e = or.CompletePaidTx(tx, order.ID, b.OwnerID, now)
		if e != nil {
			return e
		}
		if !accepted {
			return errors.New("订单完成状态已变化")
		}
		if e = tx.Model(&od.OrderItem{}).Where("order_id=?", b.OrderID).Updates(map[string]any{"delivery_status": 1, "updated_at": now}).Error; e != nil {
			return e
		}
		b.Status = "active"
		b.UpdatedAt = now
		if e = tx.Save(&b).Error; e != nil {
			return e
		}
		if e = s.event(tx, b.ID, 0, a, "funded", fmt.Sprintf("fund:%d", b.ID), b.BalanceCost, b.PointCost); e != nil {
			return e
		}
		return s.notice(tx, b.ID, b.OwnerID, "推广发券已完成", fmt.Sprintf("已创建%d张%s，可在推广客户与收益查看。", b.Quantity, b.Name))
	})
	return &b, e
}
func (s *Service) issue(tx *gorm.DB, b *Batch, u *Unit) error {
	c := md.UserCoupon{ID: snowflake.GenID(), UserID: u.RecipientID, CouponID: b.CouponID, Code: fmt.Sprintf("PC%d", u.ID), Source: 4, Status: 0, ExpireAt: b.ExpiresAt, CreatedAt: s.now(), UpdatedAt: s.now()}
	if e := tx.Create(&c).Error; e != nil {
		return e
	}
	u.UserCouponID = &c.ID
	u.Status = "claimed"
	if e := tx.Model(&md.Coupon{}).Where("id=?", b.CouponID).UpdateColumn("claimed_count", gorm.Expr("claimed_count+1")).Error; e != nil {
		return e
	}
	if e := tx.Create(&md.CouponLog{CouponID: b.CouponID, UserCouponID: &c.ID, UserID: &u.RecipientID, Action: 2, Operator: md.StrPtr(fmt.Sprintf("promo:%d", b.OwnerID)), Remark: md.StrPtr("推广专属券发放")}).Error; e != nil {
		return e
	}
	return s.notice(tx, b.ID, u.RecipientID, "收到推广专属优惠券", fmt.Sprintf("您已收到%s，满%s元减%s元。", b.Name, b.Minimum.String(), b.Face.String()))
}
func (s *Service) notice(tx *gorm.DB, batch, user int64, title, content string) error {
	return tx.Table("marketing.promo_coupon_notifications").Clauses(clause.Insert{Modifier: "IGNORE"}).Create(map[string]any{"id": snowflake.GenID(), "batch_id": batch, "customer_id": user, "title": title, "content": content, "delivered": false, "created_at": s.now()}).Error
}
func (s *Service) PayBalance(ctx context.Context, user, order int64) error {
	var b Batch
	if e := s.db.WithContext(ctx).Take(&b, "order_id=? AND owner_id=? AND source='balance'", order, user).Error; e != nil {
		return ErrForbidden
	}
	_, e := s.Fund(ctx, Actor{ID: user, Role: "customer", Notes: "平台订单余额支付"}, b.ID)
	return e
}

// Keep imported repository behavior as the sole coupon release authority.
func release(ctx context.Context, tx *gorm.DB, order int64) error {
	return mr.NewRepository(tx).ReleaseUserCouponTx(ctx, tx, order)
}

func sameTemplate(a, b Template) bool {
	return a.ID == b.ID && a.Name == b.Name && a.Face == b.Face && a.Minimum == b.Minimum && a.ValidDays == b.ValidDays && a.Enabled == b.Enabled && a.UpdatedAt.Equal(b.UpdatedAt)
}
