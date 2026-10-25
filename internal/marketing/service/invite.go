// M1 invite codes + M3 promo overview business logic.
package marketing

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"time"

	dom "ddyidc/internal/marketing/domain"
	repo "ddyidc/internal/marketing/repository"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const maxRelationLevel = 3

// ApplyInvite 用户申请优惠码；customCode 为空则按规则自动生成。
func (s *Service) ApplyInvite(ctx context.Context, userID int64, customCode string) (*InviteDTO, error) {
	values, err := s.configMap(ctx)
	if err != nil {
		return nil, err
	}
	if !configBool(values, CfgInviteEnabled) {
		return nil, errors.New("优惠码注册未开启，暂不能申请优惠码")
	}
	return s.applyGovernedCode(ctx, userID, customCode)
}

func validateCustomCode(code string, length int, charset string) error {
	if !codeFormat.MatchString(normalizedCode(code)) {
		return errors.New("自定义优惠码仅支持4～12位英文字母和数字")
	}
	return nil
}

func (s *Service) generateUniqueCode(ctx context.Context, length int, charset string) (string, error) {
	for i := 0; i < 20; i++ {
		code := randFromCharset(length, charset)
		owner, err := s.repo.FindInviteByCode(ctx, code)
		if err != nil {
			return "", fmt.Errorf("校验优惠码失败: %w", err)
		}
		if owner == nil {
			return code, nil
		}
	}
	return "", errors.New("生成优惠码失败，请稍后重试")
}

func randFromCharset(n int, charset string) string {
	out := make([]byte, n)
	max := big.NewInt(int64(len(charset)))
	for i := range out {
		v, err := rand.Int(rand.Reader, max)
		if err != nil {
			return ""
		}
		out[i] = charset[v.Int64()]
	}
	return string(out)
}

// MyInvite 我的优惠码与推广统计。
func (s *Service) MyInvite(ctx context.Context, userID int64) (*MyInviteResponse, error) {
	values, err := s.configMap(ctx)
	if err != nil {
		return nil, err
	}
	if configBool(values, CfgInviteEnabled) {
		if err = s.ensureSystemCode(ctx, userID); err != nil {
			return nil, err
		}
	}
	invite, err := s.repo.FindInviteByUser(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("查询优惠码失败: %w", err)
	}
	resp := &MyInviteResponse{}
	if invite != nil {
		resp.Invite = inviteToDTO(invite)
	}
	for level := uint8(1); level <= maxRelationLevel; level++ {
		n, err := s.repo.CountRelations(ctx, userID, level)
		if err != nil {
			return nil, fmt.Errorf("统计推广关系失败: %w", err)
		}
		switch level {
		case 1:
			resp.Stats.Level1 = n
		case 2:
			resp.Stats.Level2 = n
		case 3:
			resp.Stats.Level3 = n
		}
	}
	return resp, nil
}

// VerifyInviteCode 注册页校验优惠码，返回推广人名称。
func (s *Service) VerifyInviteCode(ctx context.Context, code string) (string, error) {
	values, err := s.configMap(ctx)
	if err != nil {
		return "", err
	}
	if !configBool(values, CfgInviteEnabled) {
		return "", errors.New("优惠码注册未开启")
	}
	policy, err := s.CodePolicy(ctx)
	if err != nil {
		return "", err
	}
	if codeIssue(code, policy) != "" {
		return "", errors.New("优惠码无效或已调整，请使用新的优惠链接")
	}
	invite, err := s.repo.FindInviteByCode(ctx, strings.ToUpper(strings.TrimSpace(code)))
	if err != nil {
		return "", fmt.Errorf("查询优惠码失败: %w", err)
	}
	if invite == nil || invite.Status != dom.InviteActive {
		return "", errors.New("优惠码无效或未启用")
	}
	if e := codeClaimUsable(s.db.WithContext(ctx), invite); e != nil {
		return "", e
	}
	customer, err := s.repo.FindCustomerBasic(ctx, invite.UserID)
	if err != nil {
		return "", fmt.Errorf("查询推广人失败: %w", err)
	}
	if customer == nil {
		return "", errors.New("推广人不存在")
	}
	return customer.Username, nil
}

// VerifySalesCode 注册页校验客服工号。
func (s *Service) VerifySalesCode(ctx context.Context, staffNo string) (string, error) {
	sales, err := s.repo.FindSalesUserByStaffNo(ctx, strings.TrimSpace(staffNo))
	if err != nil {
		return "", fmt.Errorf("查询销售客服失败: %w", err)
	}
	if sales == nil || sales.Status != 1 {
		return "", errors.New("客服工号无效或已停用")
	}
	return sales.Name, nil
}

// AfterRegister 注册成功后的营销钩子：建立推广关系树 + 绑定销售客服。
func (s *Service) AfterRegister(ctx context.Context, customerID int64, inviteCode, salesCode string) error {
	if strings.TrimSpace(inviteCode) != "" {
		if err := s.buildInviteTree(ctx, customerID, strings.ToUpper(strings.TrimSpace(inviteCode))); err != nil {
			return err
		}
	}
	if strings.TrimSpace(salesCode) != "" {
		if err := s.bindSalesOnRegister(ctx, customerID, strings.TrimSpace(salesCode)); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) buildInviteTree(ctx context.Context, inviteeID int64, code string) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if e := LockCodePolicy(ctx, tx); e != nil {
			return e
		}
		existing, e := NewService(tx, repo.NewRepository(tx)).repo.FindActiveRelation(ctx, inviteeID, 1)
		if e != nil {
			return e
		}
		if existing != nil {
			return nil
		}
		p, e := policyFrom(tx)
		if e != nil {
			return e
		}
		if codeIssue(code, p) != "" {
			return errors.New("优惠码无效或已调整，请使用新的优惠链接")
		}
		return NewService(tx, repo.NewRepository(tx)).buildGovernedTree(ctx, inviteeID, code)
	})
}
func (s *Service) buildGovernedTree(ctx context.Context, inviteeID int64, code string) error {
	values, err := s.configMap(ctx)
	if err != nil {
		return err
	}
	if !configBool(values, CfgInviteEnabled) {
		return errors.New("优惠码注册未开启，优惠码无效")
	}
	invite, err := s.repo.FindInviteByCode(ctx, code)
	if err != nil {
		return fmt.Errorf("查询优惠码失败: %w", err)
	}
	if invite == nil || invite.Status != dom.InviteActive {
		return errors.New("优惠码无效或未启用")
	}
	if e := codeClaimUsable(s.db.WithContext(ctx), invite); e != nil {
		return e
	}
	if invite.UserID == inviteeID {
		return errors.New("不能使用自己的优惠码")
	}
	// 一级关系
	if err := s.createRelationIfAbsent(ctx, invite.UserID, inviteeID, 1, code); err != nil {
		return err
	}
	if err := s.repo.IncreaseInviteCount(ctx, invite.UserID, 1); err != nil {
		return fmt.Errorf("更新推广统计失败: %w", err)
	}
	// 二级/三级沿链路上溯
	current := invite.UserID
	for level := uint8(2); level <= maxRelationLevel; level++ {
		upstream, err := s.repo.FindActiveRelation(ctx, current, 1)
		if err != nil {
			return fmt.Errorf("查询上级推广关系失败: %w", err)
		}
		if upstream == nil {
			break
		}
		if err := s.createRelationIfAbsent(ctx, upstream.InviterID, inviteeID, level, code); err != nil {
			return err
		}
		current = upstream.InviterID
	}
	return nil
}

func (s *Service) createRelationIfAbsent(ctx context.Context, inviterID, inviteeID int64, level uint8, code string) error {
	existing, err := s.repo.FindActiveRelation(ctx, inviteeID, level)
	if err != nil {
		return fmt.Errorf("查询推广关系失败: %w", err)
	}
	if existing != nil {
		return nil
	}
	rel := &dom.InviteRelation{
		InviterID: inviterID, InviteeID: inviteeID, Level: level, InviteCode: code,
		Status: dom.InviteRelationStatusActive,
	}
	if err := s.repo.CreateRelation(ctx, nil, rel); err != nil {
		if strings.Contains(err.Error(), "Duplicate entry") || strings.Contains(err.Error(), "1062") {
			return nil // 唯一键兜底：关系已存在视为成功
		}
		return fmt.Errorf("建立推广关系失败: %w", err)
	}
	return nil
}

func (s *Service) bindSalesOnRegister(ctx context.Context, customerID int64, staffNo string) error {
	sales, err := s.repo.FindSalesUserByStaffNo(ctx, staffNo)
	if err != nil {
		return fmt.Errorf("查询销售客服失败: %w", err)
	}
	if sales == nil || sales.Status != 1 {
		return errors.New("客服工号无效或已停用")
	}
	binding := &dom.SalesCustomer{
		SalesID: sales.ID, CustomerID: customerID, Source: "register",
		RegisterSalesCode: dom.StrPtr(sales.StaffNo), Status: 1,
	}
	if err := s.repo.BindSalesCustomer(ctx, binding); err != nil {
		return fmt.Errorf("绑定销售客服失败: %w", err)
	}
	return nil
}

// PromoOverview M3 用户推广看板（注册数/充值额/消费额/收益）。
func (s *Service) PromoOverview(ctx context.Context, userID int64) (*PromoOverviewResponse, error) {
	my, err := s.MyInvite(ctx, userID)
	if err != nil {
		return nil, err
	}
	resp := &PromoOverviewResponse{
		Registrations: my.Stats.Level1,
		Earnings:      EarningsResponse{},
	}
	if my.Invite != nil {
		resp.InviteCode = my.Invite.Code
		resp.PromoLinkPath = "/login?invite=" + my.Invite.Code
	}
	values, err := s.configMap(ctx)
	if err != nil {
		return nil, err
	}
	resp.PosterEnabled = configBool(values, CfgPromoPosterEnabled)
	inviteeIDs, err := s.directInviteeIDs(ctx, userID)
	if err != nil {
		return nil, err
	}
	spend, _, err := s.repo.SumInviteePaidOrders(ctx, inviteeIDs)
	if err != nil {
		return nil, fmt.Errorf("统计推广客户消费失败: %w", err)
	}
	resp.SpendTotal = spend
	recharge, err := s.repo.SumInviteeRecharges(ctx, inviteeIDs)
	if err != nil {
		return nil, fmt.Errorf("统计推广客户充值失败: %w", err)
	}
	resp.RechargeTotal = recharge
	earnings, err := s.MyEarnings(ctx, userID)
	if err != nil {
		return nil, err
	}
	resp.Earnings = *earnings
	return resp, nil
}

// directInviteeIDs 返回我直接推广（一级）的客户 ID 列表。
func (s *Service) directInviteeIDs(ctx context.Context, userID int64) ([]int64, error) {
	rels, err := s.repo.ListRelationsByInviter(ctx, userID, 1)
	if err != nil {
		return nil, fmt.Errorf("查询推广关系失败: %w", err)
	}
	ids := make([]int64, 0, len(rels))
	for _, rel := range rels {
		ids = append(ids, rel.InviteeID)
	}
	return ids, nil
}

// AdminInviteList 管理端优惠码列表。
func (s *Service) AdminInviteList(ctx context.Context, page, pageSize int, status *uint8, keyword string) ([]*InviteDTO, int64, error) {
	rows, total, err := s.repo.ListInvites(ctx, page, pageSize, status, keyword)
	if err != nil {
		return nil, 0, fmt.Errorf("查询优惠码列表失败: %w", err)
	}
	out := make([]*InviteDTO, 0, len(rows))
	for _, row := range rows {
		dto := inviteToDTO(&row.Invite)
		dto.Username = row.Username
		out = append(out, dto)
	}
	return out, total, nil
}

// AdminReviewInvite 管理端审核自定义优惠码。
func (s *Service) AdminReviewInvite(ctx context.Context, id int64, approve bool, reason string) error {
	if len([]rune(reason)) > 250 {
		return errors.New("审核说明最多250字")
	}
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if e := LockCodePolicy(ctx, tx); e != nil {
			return e
		}
		p, e := policyFrom(tx)
		if e != nil {
			return e
		}
		var v dom.Invite
		if e = tx.Clauses(clause.Locking{Strength: "UPDATE"}).Take(&v, "id=?", id).Error; e != nil {
			return e
		}
		if v.Status != dom.InvitePending && v.PendingCode == nil {
			return errors.New("该优惠码不在待审核状态")
		}
		candidate := v.Code
		if v.PendingCode != nil {
			candidate = *v.PendingCode
		}
		if approve {
			if issue := codeIssue(candidate, p); issue != "" {
				return errors.New(issue)
			}
			var claim struct {
				OwnerID int64
				Kind    string
			}
			e = tx.Table("marketing.code_claims").Where("code=?", candidate).Take(&claim).Error
			if errors.Is(e, gorm.ErrRecordNotFound) {
				if e = claimCode(tx, candidate, v.UserID, "pending"); e != nil {
					return e
				}
				claim.OwnerID = v.UserID
				claim.Kind = "pending"
			} else if e != nil {
				return e
			}
			if claim.OwnerID != v.UserID || claim.Kind == "retired" {
				return errors.New("优惠码归属已变化或已封存")
			}
			if v.PendingCode != nil {
				if e = retireCode(tx, v.Code, v.UserID); e != nil {
					return e
				}
				v.Code = candidate
				v.PendingCode = nil
			}
			v.Status = dom.InviteActive
			v.RejectReason = nil
			if e = tx.Table("marketing.code_claims").Where("code=? AND owner_id=?", candidate, v.UserID).Update("kind", "issued").Error; e != nil {
				return e
			}
		} else {
			if e = retireCode(tx, candidate, v.UserID); e != nil {
				return e
			}
			if v.PendingCode != nil {
				v.PendingCode = nil
			} else {
				v.Status = dom.InviteRejected
			}
			if strings.TrimSpace(reason) == "" {
				reason = "不符合优惠码规则"
			}
			v.RejectReason = dom.StrPtr(reason)
		}
		now := time.Now().UTC()
		v.AuditedAt = &now
		return tx.Save(&v).Error
	})
}
func (s *Service) AdminSetInviteStatus(ctx context.Context, id int64, status uint8) error {
	if status != dom.InviteActive && status != dom.InviteDisabled {
		return errors.New("优惠码状态无效")
	}
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if e := LockCodePolicy(ctx, tx); e != nil {
			return e
		}
		var v dom.Invite
		if e := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Take(&v, "id=?", id).Error; e != nil {
			return e
		}
		if v.Status == dom.InvitePending {
			return errors.New("请先审核优惠码")
		}
		if status == dom.InviteActive {
			p, e := policyFrom(tx)
			if e != nil {
				return e
			}
			if issue := codeIssue(v.Code, p); issue != "" {
				return errors.New(issue)
			}
			var n int64
			if e = tx.Table("marketing.code_claims").Where("code=? AND kind='retired'", v.Code).Count(&n).Error; e != nil {
				return e
			}
			if n > 0 {
				return errors.New("该优惠码已封存，不能重新启用")
			}
		}
		v.Status = status
		return tx.Save(&v).Error
	})
}

func inviteToDTO(v *dom.Invite) *InviteDTO {
	return &InviteDTO{
		ID: fmt.Sprintf("%d", v.ID), UserID: fmt.Sprintf("%d", v.UserID),
		Code: v.Code, PendingCode: v.PendingCode, Status: v.Status, RejectReason: v.RejectReason,
		InviteCount: int64(v.InviteCount), CreatedAt: formatTime(v.CreatedAt),
	}
}
