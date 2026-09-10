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
)

const maxRelationLevel = 3

// ApplyInvite 用户申请邀请码；customCode 为空则按规则自动生成。
func (s *Service) ApplyInvite(ctx context.Context, userID int64, customCode string) (*InviteDTO, error) {
	values, err := s.configMap(ctx)
	if err != nil {
		return nil, err
	}
	if !configBool(values, CfgInviteEnabled) {
		return nil, errors.New("邀请注册未开启，暂不能申请邀请码")
	}
	length := configInt(values, CfgInviteCodeLength, 6)
	if length < 4 {
		length = 4
	}
	if length > 16 {
		length = 16
	}
	charset := strings.TrimSpace(values[CfgInviteCodeCharset])
	if charset == "" {
		charset = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789"
	}

	trimmed := strings.ToUpper(strings.TrimSpace(customCode))
	code := trimmed
	if trimmed != "" {
		if err := validateCustomCode(trimmed, length, charset); err != nil {
			return nil, err
		}
		owner, err := s.repo.FindInviteByCode(ctx, trimmed)
		if err != nil {
			return nil, fmt.Errorf("校验邀请码失败: %w", err)
		}
		if owner != nil && owner.UserID != userID {
			return nil, errors.New("该邀请码已被占用，请换一个")
		}
	} else {
		code, err = s.generateUniqueCode(ctx, length, charset)
		if err != nil {
			return nil, err
		}
	}

	existing, err := s.repo.FindInviteByUser(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("查询邀请码失败: %w", err)
	}
	if existing != nil && existing.Status != dom.InviteDisabled && existing.Status != dom.InviteRejected {
		return nil, errors.New("您已拥有邀请码，无需重复申请")
	}
	// 自定义码且开启审核 → 待审核；其余直接生效
	status := uint8(dom.InviteActive)
	if trimmed != "" && configBool(values, CfgInviteReview) {
		status = dom.InvitePending
	}
	now := time.Now()
	if existing == nil {
		invite := &dom.Invite{UserID: userID, Code: code, Status: status}
		if status == dom.InviteActive {
			invite.AuditedAt = &now
		}
		if err := s.repo.CreateInvite(ctx, invite); err != nil {
			return nil, fmt.Errorf("保存邀请码失败: %w", err)
		}
		return inviteToDTO(invite), nil
	}
	existing.Code = code
	existing.Status = status
	existing.RejectReason = nil
	existing.AuditedAt = nil
	if status == dom.InviteActive {
		existing.AuditedAt = &now
	}
	if err := s.repo.UpdateInvite(ctx, existing); err != nil {
		return nil, fmt.Errorf("更新邀请码失败: %w", err)
	}
	return inviteToDTO(existing), nil
}

func validateCustomCode(code string, length int, charset string) error {
	if len(code) < 4 || len(code) > 16 {
		return errors.New("自定义邀请码长度需为4-16位")
	}
	if len(code) != length {
		return fmt.Errorf("自定义邀请码长度需为%d位", length)
	}
	for _, ch := range code {
		if !strings.ContainsRune(charset, ch) {
			return errors.New("自定义邀请码包含不允许的字符")
		}
	}
	return nil
}

func (s *Service) generateUniqueCode(ctx context.Context, length int, charset string) (string, error) {
	for i := 0; i < 20; i++ {
		code := randFromCharset(length, charset)
		owner, err := s.repo.FindInviteByCode(ctx, code)
		if err != nil {
			return "", fmt.Errorf("校验邀请码失败: %w", err)
		}
		if owner == nil {
			return code, nil
		}
	}
	return "", errors.New("生成邀请码失败，请稍后重试")
}

func randFromCharset(n int, charset string) string {
	out := make([]byte, n)
	max := big.NewInt(int64(len(charset)))
	for i := range out {
		v, err := rand.Int(rand.Reader, max)
		if err != nil {
			out[i] = charset[time.Now().UnixNano()%int64(len(charset))]
			continue
		}
		out[i] = charset[v.Int64()]
	}
	return string(out)
}

// MyInvite 我的邀请码与邀请统计。
func (s *Service) MyInvite(ctx context.Context, userID int64) (*MyInviteResponse, error) {
	invite, err := s.repo.FindInviteByUser(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("查询邀请码失败: %w", err)
	}
	resp := &MyInviteResponse{}
	if invite != nil {
		resp.Invite = inviteToDTO(invite)
	}
	for level := uint8(1); level <= maxRelationLevel; level++ {
		n, err := s.repo.CountRelations(ctx, userID, level)
		if err != nil {
			return nil, fmt.Errorf("统计邀请关系失败: %w", err)
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

// VerifyInviteCode 注册页校验邀请码，返回邀请人名称。
func (s *Service) VerifyInviteCode(ctx context.Context, code string) (string, error) {
	values, err := s.configMap(ctx)
	if err != nil {
		return "", err
	}
	if !configBool(values, CfgInviteEnabled) {
		return "", errors.New("邀请注册未开启")
	}
	invite, err := s.repo.FindInviteByCode(ctx, strings.ToUpper(strings.TrimSpace(code)))
	if err != nil {
		return "", fmt.Errorf("查询邀请码失败: %w", err)
	}
	if invite == nil || invite.Status != dom.InviteActive {
		return "", errors.New("邀请码无效或未启用")
	}
	customer, err := s.repo.FindCustomerBasic(ctx, invite.UserID)
	if err != nil {
		return "", fmt.Errorf("查询邀请人失败: %w", err)
	}
	if customer == nil {
		return "", errors.New("邀请人不存在")
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

// AfterRegister 注册成功后的营销钩子：建立邀请关系树 + 绑定销售客服。
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
	values, err := s.configMap(ctx)
	if err != nil {
		return err
	}
	if !configBool(values, CfgInviteEnabled) {
		return errors.New("邀请注册未开启，邀请码无效")
	}
	invite, err := s.repo.FindInviteByCode(ctx, code)
	if err != nil {
		return fmt.Errorf("查询邀请码失败: %w", err)
	}
	if invite == nil || invite.Status != dom.InviteActive {
		return errors.New("邀请码无效或未启用")
	}
	if invite.UserID == inviteeID {
		return errors.New("不能使用自己的邀请码")
	}
	// 一级关系
	if err := s.createRelationIfAbsent(ctx, invite.UserID, inviteeID, 1, code); err != nil {
		return err
	}
	if err := s.repo.IncreaseInviteCount(ctx, invite.UserID, 1); err != nil {
		return fmt.Errorf("更新邀请统计失败: %w", err)
	}
	// 二级/三级沿链路上溯
	current := invite.UserID
	for level := uint8(2); level <= maxRelationLevel; level++ {
		upstream, err := s.repo.FindActiveRelation(ctx, current, 1)
		if err != nil {
			return fmt.Errorf("查询上级邀请关系失败: %w", err)
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
		return fmt.Errorf("查询邀请关系失败: %w", err)
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
		return fmt.Errorf("建立邀请关系失败: %w", err)
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
		return nil, fmt.Errorf("统计被邀请人消费失败: %w", err)
	}
	resp.SpendTotal = spend
	recharge, err := s.repo.SumInviteeRecharges(ctx, inviteeIDs)
	if err != nil {
		return nil, fmt.Errorf("统计被邀请人充值失败: %w", err)
	}
	resp.RechargeTotal = recharge
	earnings, err := s.MyEarnings(ctx, userID)
	if err != nil {
		return nil, err
	}
	resp.Earnings = *earnings
	return resp, nil
}

// directInviteeIDs 返回我直接邀请（一级）的客户 ID 列表。
func (s *Service) directInviteeIDs(ctx context.Context, userID int64) ([]int64, error) {
	rels, err := s.repo.ListRelationsByInviter(ctx, userID, 1)
	if err != nil {
		return nil, fmt.Errorf("查询邀请关系失败: %w", err)
	}
	ids := make([]int64, 0, len(rels))
	for _, rel := range rels {
		ids = append(ids, rel.InviteeID)
	}
	return ids, nil
}

// AdminInviteList 管理端邀请码列表。
func (s *Service) AdminInviteList(ctx context.Context, page, pageSize int, status *uint8, keyword string) ([]*InviteDTO, int64, error) {
	rows, total, err := s.repo.ListInvites(ctx, page, pageSize, status, keyword)
	if err != nil {
		return nil, 0, fmt.Errorf("查询邀请码列表失败: %w", err)
	}
	out := make([]*InviteDTO, 0, len(rows))
	for _, row := range rows {
		dto := inviteToDTO(&row.Invite)
		dto.Username = row.Username
		out = append(out, dto)
	}
	return out, total, nil
}

// AdminReviewInvite 管理端审核自定义邀请码。
func (s *Service) AdminReviewInvite(ctx context.Context, id int64, approve bool, reason string) error {
	invite, err := s.repo.FindInviteByID(ctx, id)
	if err != nil {
		return fmt.Errorf("查询邀请码失败: %w", err)
	}
	if invite == nil {
		return errors.New("邀请码不存在")
	}
	if invite.Status != dom.InvitePending {
		return errors.New("该邀请码不在待审核状态")
	}
	now := time.Now()
	invite.AuditedAt = &now
	if approve {
		invite.Status = dom.InviteActive
		invite.RejectReason = nil
	} else {
		invite.Status = dom.InviteRejected
		if strings.TrimSpace(reason) == "" {
			reason = "不符合邀请码规则"
		}
		invite.RejectReason = dom.StrPtr(strings.TrimSpace(reason))
	}
	return s.repo.UpdateInvite(ctx, invite)
}

// AdminSetInviteStatus 管理端启用/禁用邀请码。
func (s *Service) AdminSetInviteStatus(ctx context.Context, id int64, status uint8) error {
	invite, err := s.repo.FindInviteByID(ctx, id)
	if err != nil {
		return fmt.Errorf("查询邀请码失败: %w", err)
	}
	if invite == nil {
		return errors.New("邀请码不存在")
	}
	if invite.Status == dom.InvitePending {
		return errors.New("待审核的邀请码请先审核")
	}
	invite.Status = status
	invite.AuditedAt = nil
	return s.repo.UpdateInvite(ctx, invite)
}

func inviteToDTO(v *dom.Invite) *InviteDTO {
	return &InviteDTO{
		ID: fmt.Sprintf("%d", v.ID), UserID: fmt.Sprintf("%d", v.UserID),
		Code: v.Code, Status: v.Status, RejectReason: v.RejectReason,
		InviteCount: int64(v.InviteCount), CreatedAt: formatTime(v.CreatedAt),
	}
}
