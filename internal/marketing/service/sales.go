// M5 sales/customer-service business logic. Sales accounts see only their own
// customers, with sensitive fields filtered by visibility rules.
package marketing

import (
	"context"
	"errors"
	"fmt"
	"strings"

	dom "ddyidc/internal/marketing/domain"
	"golang.org/x/crypto/bcrypt"
)

// SalesCreateInput 管理端新增销售。
type SalesCreateInput struct {
	StaffNo  string `json:"staffNo"`
	Username string `json:"username"`
	Password string `json:"password"`
	Name     string `json:"name"`
	Phone    string `json:"phone"`
	Email    string `json:"email"`
}

// AdminSalesCreate 新增销售账号。
func (s *Service) AdminSalesCreate(ctx context.Context, input *SalesCreateInput) (*SalesUserDTO, error) {
	username := strings.TrimSpace(input.Username)
	password := input.Password
	name := strings.TrimSpace(input.Name)
	if username == "" || len(password) < 6 || name == "" {
		return nil, errors.New("登录名、姓名必填，密码至少6位")
	}
	staffNo := strings.TrimSpace(input.StaffNo)
	if staffNo == "" {
		staffNo = "KF" + dom.RandToken(4)
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return nil, fmt.Errorf("加密密码失败: %w", err)
	}
	sales := &dom.SalesUser{
		StaffNo: strings.ToUpper(staffNo), Username: username, PasswordHash: string(hash),
		Name: name, Status: 1,
	}
	if input.Phone != "" {
		sales.Phone = dom.StrPtr(strings.TrimSpace(input.Phone))
	}
	if input.Email != "" {
		sales.Email = dom.StrPtr(strings.TrimSpace(input.Email))
	}
	if err := s.repo.CreateSalesUser(ctx, sales); err != nil {
		if isDuplicateEntry(err) {
			return nil, errors.New("销售工号或登录名已存在")
		}
		return nil, fmt.Errorf("创建销售账号失败: %w", err)
	}
	if _, err := s.repo.FindVisibilityRule(ctx, sales.ID); err != nil {
		return nil, fmt.Errorf("初始化销售可见性规则失败: %w", err)
	}
	return salesToDTO(sales), nil
}

// AdminSalesList 管理端销售列表。
func (s *Service) AdminSalesList(ctx context.Context, page, pageSize int, keyword string) ([]*SalesUserDTO, int64, error) {
	rows, total, err := s.repo.ListSalesUsers(ctx, page, pageSize, keyword)
	if err != nil {
		return nil, 0, fmt.Errorf("查询销售列表失败: %w", err)
	}
	out := make([]*SalesUserDTO, 0, len(rows))
	for _, row := range rows {
		out = append(out, salesToDTO(row))
	}
	return out, total, nil
}

// AdminSalesSetStatus 禁用/启用销售。
func (s *Service) AdminSalesSetStatus(ctx context.Context, id int64, status uint8) error {
	sales, err := s.repo.FindSalesUser(ctx, id)
	if err != nil {
		return fmt.Errorf("查询销售失败: %w", err)
	}
	if sales == nil {
		return errors.New("销售不存在")
	}
	sales.Status = status
	return s.repo.UpdateSalesUser(ctx, sales)
}

// GetVisibility 可见性规则（无专属规则时回退全局默认；无任何规则时采用安全默认）。
func (s *Service) GetVisibility(ctx context.Context, salesID int64) (*VisibilityDTO, error) {
	rule, err := s.repo.FindVisibilityRule(ctx, salesID)
	if err != nil {
		return nil, fmt.Errorf("查询可见性规则失败: %w", err)
	}
	dto := &VisibilityDTO{SalesID: formatInt64(salesID)}
	if rule != nil {
		dto.CanViewMobile = rule.CanViewMobile == 1
		dto.CanViewEmail = rule.CanViewEmail == 1
		dto.CanViewOrders = rule.CanViewOrders == 1
		dto.CanViewSpend = rule.CanViewSpend == 1
		dto.CanViewBalance = rule.CanViewBalance == 1
		dto.CanViewRealname = rule.CanViewRealname == 1
		dto.CanFollowup = rule.CanFollowup == 1
	} else {
		// 兜底默认：手机号/邮箱/订单/跟进可见（与全局默认规则一致）
		dto.CanViewMobile = true
		dto.CanViewEmail = true
		dto.CanViewOrders = true
		dto.CanFollowup = true
	}
	return dto, nil
}

// SaveVisibility 保存可见性规则。
func (s *Service) SaveVisibility(ctx context.Context, salesID int64, dto *VisibilityDTO) error {
	toUint8 := func(b bool) uint8 {
		if b {
			return 1
		}
		return 0
	}
	rule := &dom.SalesVisibilityRule{
		SalesID: salesID,
		CanViewMobile: toUint8(dto.CanViewMobile), CanViewEmail: toUint8(dto.CanViewEmail),
		CanViewOrders: toUint8(dto.CanViewOrders), CanViewSpend: toUint8(dto.CanViewSpend),
		CanViewBalance: toUint8(dto.CanViewBalance), CanViewRealname: toUint8(dto.CanViewRealname),
		CanFollowup: toUint8(dto.CanFollowup),
	}
	return s.repo.SaveVisibilityRule(ctx, rule)
}

// SalesLogin 销售后台登录校验。
func (s *Service) SalesLogin(ctx context.Context, username, password string) (*dom.SalesUser, error) {
	sales, err := s.repo.FindSalesUserByUsername(ctx, strings.TrimSpace(username))
	if err != nil {
		return nil, fmt.Errorf("查询销售账号失败: %w", err)
	}
	if sales == nil || bcrypt.CompareHashAndPassword([]byte(sales.PasswordHash), []byte(password)) != nil {
		return nil, errors.New("账号或密码错误")
	}
	if sales.Status != 1 {
		return nil, errors.New("账号已被禁用，请联系管理员")
	}
	return sales, nil
}

// SalesCustomers 销售名下客户列表（按可见性规则脱敏）。
func (s *Service) SalesCustomers(ctx context.Context, salesID int64, page, pageSize int, keyword string) ([]*SalesCustomerDTO, int64, error) {
	rule, err := s.GetVisibility(ctx, salesID)
	if err != nil {
		return nil, 0, err
	}
	rows, total, err := s.repo.ListSalesCustomers(ctx, salesID, page, pageSize, keyword)
	if err != nil {
		return nil, 0, fmt.Errorf("查询名下客户失败: %w", err)
	}
	out := make([]*SalesCustomerDTO, 0, len(rows))
	for _, row := range rows {
		out = append(out, maskCustomerRow(row, rule))
	}
	return out, total, nil
}

// SalesCustomerDetail 单个名下客户（含可选订单摘要）。
func (s *Service) SalesCustomerDetail(ctx context.Context, salesID, customerID int64) (*SalesCustomerDTO, []*dom.CustomerOrderRow, error) {
	owner, err := s.repo.FindSalesOwner(ctx, customerID)
	if err != nil {
		return nil, nil, fmt.Errorf("查询客户归属失败: %w", err)
	}
	if owner != salesID {
		return nil, nil, errors.New("客户不在您的名下")
	}
	rule, err := s.GetVisibility(ctx, salesID)
	if err != nil {
		return nil, nil, err
	}
	row, err := s.repo.GetSalesCustomer(ctx, salesID, customerID)
	if err != nil {
		return nil, nil, fmt.Errorf("查询客户失败: %w", err)
	}
	if row == nil {
		return nil, nil, errors.New("客户不在您的名下")
	}
	dto := maskCustomerRow(row, rule)
	var orders []*dom.CustomerOrderRow
	if rule.CanViewOrders {
		orders, _, err = s.repo.ListCustomerOrders(ctx, customerID, 1, 20)
		if err != nil {
			return nil, nil, fmt.Errorf("查询客户订单失败: %w", err)
		}
	}
	return dto, orders, nil
}

// maskCustomerRow 按规则隐藏字段：手机号脱敏，其余字段不可见时置空。
func maskCustomerRow(row *dom.SalesCustomerRow, rule *VisibilityDTO) *SalesCustomerDTO {
	dto := &SalesCustomerDTO{
		CustomerID: formatInt64(row.CustomerID),
		Username:   row.Username,
		Status:     row.Status,
		CreatedAt:  formatTime(row.CreatedAt),
	}
	if rule.CanViewMobile && row.Phone != nil {
		dto.Mobile = *row.Phone
	} else if row.Phone != nil && *row.Phone != "" {
		dto.Mobile = dom.MaskMobile(*row.Phone)
	}
	if rule.CanViewEmail && row.Email != nil {
		dto.Email = *row.Email
	}
	if rule.CanViewRealname && row.RealName != nil {
		dto.RealName = *row.RealName
	}
	if rule.CanViewOrders {
		dto.OrderCount = &row.OrderCount
		dto.LastPayAt = formatTimePtr(row.LastPayAt)
	}
	if rule.CanViewSpend {
		dto.TotalSpend = &row.TotalSpend
	}
	if rule.CanViewBalance && row.Balance != nil {
		dto.Balance = row.Balance
	}
	return dto
}

// CreateFollowup 跟进记录（占位模块）。
func (s *Service) CreateFollowup(ctx context.Context, salesID, customerID int64, followupType, content string) error {
	if strings.TrimSpace(content) == "" {
		return errors.New("请填写跟进内容")
	}
	owner, err := s.repo.FindSalesOwner(ctx, customerID)
	if err != nil {
		return fmt.Errorf("查询客户归属失败: %w", err)
	}
	if owner != salesID {
		return errors.New("客户不在您的名下")
	}
	rule, err := s.GetVisibility(ctx, salesID)
	if err != nil {
		return err
	}
	if !rule.CanFollowup {
		return errors.New("当前角色未开通跟进权限")
	}
	if strings.TrimSpace(followupType) == "" {
		followupType = "follow"
	}
	return s.repo.CreateFollowup(ctx, &dom.SalesFollowup{
		SalesID: salesID, CustomerID: customerID, FollowupType: followupType, Content: content,
	})
}

// ListFollowups 跟进记录列表。
func (s *Service) ListFollowups(ctx context.Context, salesID, customerID int64, page, pageSize int) ([]*FollowupDTO, int64, error) {
	rows, total, err := s.repo.ListFollowups(ctx, salesID, customerID, page, pageSize)
	if err != nil {
		return nil, 0, fmt.Errorf("查询跟进记录失败: %w", err)
	}
	out := make([]*FollowupDTO, 0, len(rows))
	for _, row := range rows {
		out = append(out, &FollowupDTO{
			ID: formatInt64(row.ID), SalesID: formatInt64(row.SalesID), CustomerID: formatInt64(row.CustomerID),
			Type: row.FollowupType, Content: row.Content, CreatedAt: formatTime(row.CreatedAt),
		})
	}
	return out, total, nil
}

// AdminBindCustomer 管理端绑定客户到销售。
func (s *Service) AdminBindCustomer(ctx context.Context, salesID, customerID int64) error {
	if customerID <= 0 {
		return errors.New("请填写有效的客户ID")
	}
	customer, err := s.repo.FindCustomerBasic(ctx, customerID)
	if err != nil {
		return fmt.Errorf("查询客户失败: %w", err)
	}
	if customer == nil {
		return errors.New("客户不存在")
	}
	sales, err := s.repo.FindSalesUser(ctx, salesID)
	if err != nil {
		return fmt.Errorf("查询销售失败: %w", err)
	}
	if sales == nil {
		return errors.New("销售不存在")
	}
	return s.repo.BindSalesCustomer(ctx, &dom.SalesCustomer{
		SalesID: salesID, CustomerID: customerID, Source: "admin", Status: 1,
	})
}

// AdminUnbindCustomer 管理端解绑。
func (s *Service) AdminUnbindCustomer(ctx context.Context, salesID, customerID int64) error {
	return s.repo.UnbindSalesCustomer(ctx, salesID, customerID)
}

// AdminCustomerList 管理端查看销售名下客户（不脱敏）。
func (s *Service) AdminCustomerList(ctx context.Context, salesID int64, page, pageSize int, keyword string) ([]*dom.SalesCustomerRow, int64, error) {
	return s.repo.ListSalesCustomers(ctx, salesID, page, pageSize, keyword)
}

func salesToDTO(v *dom.SalesUser) *SalesUserDTO {
	dto := &SalesUserDTO{
		ID: formatInt64(v.ID), StaffNo: v.StaffNo, Username: v.Username,
		Name: v.Name, Status: v.Status, CreatedAt: formatTime(v.CreatedAt),
	}
	if v.Phone != nil {
		dto.Phone = *v.Phone
	}
	if v.Email != nil {
		dto.Email = *v.Email
	}
	if v.UserID > 0 {
		dto.UserID = formatInt64(v.UserID)
	}
	return dto
}
