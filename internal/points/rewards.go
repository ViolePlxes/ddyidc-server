package points

import (
	"context"
	"fmt"
	"gorm.io/gorm"
	"time"
)

func signinBonus(ms []Milestone, streak int, progressive bool) int64 {
	var bonus int64
	for _, m := range ms {
		if progressive {
			if m.Days <= streak && m.Points > bonus {
				bonus = m.Points
			}
		} else if m.Days == streak {
			bonus += m.Points
		}
	}
	return bonus
}

// Registration's auth event precedes invitation linking. Run this additive
// reward hook only after the existing marketing hook has attached the relation.
func (s *Service) AfterInviteAttached(ctx context.Context, userID int64) error {
	return s.referralEvent(ctx, EventRegister, userID, EventMeta{})
}

// Activity accepts no client supplied event or amount. All achievements are
// proven from platform data; stable references make retries safe.
func (s *Service) Activity(ctx context.Context, userID int64) error {
	var user struct {
		Phone, Nickname, Email string
		LastLoginAt            *time.Time
	}
	if err := s.db.WithContext(ctx).Table("customer.customers").Select("COALESCE(phone,'') phone, COALESCE(nickname,'') nickname, COALESCE(email,'') email,last_login_at").Where("id=? AND status=1 AND deleted_at IS NULL", userID).Take(&user).Error; err != nil {
		return err
	}
	events := []string{"console_visit"}
	if user.LastLoginAt != nil && user.LastLoginAt.Format("20060102") == s.now().Format("20060102") {
		events = append(events, EventLogin)
	}
	if user.Phone != "" {
		events = append(events, "bind_phone")
	}
	if user.Nickname != "" && user.Email != "" {
		events = append(events, EventProfileComplete)
	}
	var certified int64
	if err := s.db.WithContext(ctx).Table("customer.customer_realname").Where("customer_id=? AND status=2", userID).Count(&certified).Error; err != nil {
		return err
	}
	if certified > 0 {
		events = append(events, EventCertification)
	}
	for _, event := range events {
		ref := fmt.Sprintf("profile:%d", userID)
		if event == "console_visit" || event == EventLogin {
			ref = s.now().Format("20060102")
		}
		if _, err := s.GrantByEvent(ctx, event, userID, EventMeta{RefID: ref}); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) referralEvent(ctx context.Context, event string, userID int64, meta EventMeta) error {
	if event != EventRegister && event != EventOrder && event != EventRecharge {
		return nil
	}
	var rel struct{ InviterID int64 }
	err := s.db.WithContext(ctx).Table("marketing.invite_relations").Select("inviter_id").Where("invitee_id=? AND level=1 AND status=1 AND inviter_id<>?", userID, userID).Take(&rel).Error
	if err == gorm.ErrRecordNotFound {
		return nil
	}
	if err != nil {
		return err
	}
	switch event {
	case EventRegister:
		_, err = s.GrantByEvent(ctx, EventInvite, rel.InviterID, EventMeta{RefID: fmt.Sprintf("invite:%d", userID), Remark: "邀请好友注册奖励"})
	case EventOrder:
		_, err = s.GrantByEvent(ctx, "invite_order", rel.InviterID, EventMeta{RefID: meta.RefID, AmountCents: meta.AmountCents, Remark: "好友消费返积分"})
	case EventRecharge:
		var first string
		if err = s.db.WithContext(ctx).Table("payment.recharge_orders").Select("recharge_no").Where("customer_id=? AND status=2", userID).Order("paid_at,id").Limit(1).Scan(&first).Error; err != nil {
			return err
		}
		if first == meta.RefID {
			_, err = s.GrantByEvent(ctx, "invite_first_recharge", rel.InviterID, EventMeta{RefID: fmt.Sprintf("invite:%d", userID), Remark: "好友首充奖励"})
		}
	}
	return err
}

type ReferralChild struct {
	UserID       int64     `json:"userId,string"`
	RegisteredAt time.Time `json:"registeredAt"`
	SpendYuan    string    `json:"spendYuan"`
	RechargeYuan string    `json:"rechargeYuan"`
}

func (s *Service) ReferralChildren(ctx context.Context, userID int64, p, z int) ([]ReferralChild, int64, error) {
	p, z = page(p, z)
	q := s.db.WithContext(ctx).Table("marketing.invite_relations r").Where("r.inviter_id=? AND r.level=1 AND r.status=1", userID)
	var total int64
	if err := q.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	rows := []ReferralChild{}
	err := q.Select(`r.invitee_id user_id,r.created_at registered_at,
 CAST(COALESCE((SELECT SUM(o.pay_amount) FROM ` + "`order`.orders" + ` o WHERE o.customer_id=r.invitee_id AND o.status IN (2,6)),0)/100 AS DECIMAL(14,2)) spend_yuan,
 CAST(COALESCE((SELECT SUM(b.amount) FROM payment.recharge_orders b WHERE b.customer_id=r.invitee_id AND b.status=2),0)/100 AS DECIMAL(14,2)) recharge_yuan`).Order("r.created_at DESC,r.id DESC").Offset((p - 1) * z).Limit(z).Scan(&rows).Error
	return rows, total, err
}
