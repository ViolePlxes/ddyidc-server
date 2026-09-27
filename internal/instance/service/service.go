package instance

import (
	"context"
	balance "ddyidc/internal/billing/balance"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"sort"
	"strconv"
	"strings"
	"time"

	dom "ddyidc/internal/instance/domain"
	repo "ddyidc/internal/instance/repository"
	"ddyidc/internal/remoteconsole"
	"ddyidc/internal/upstream/provider"
	"ddyidc/internal/upstream/provider/contracts"

	"gorm.io/gorm"
)

var (
	ErrNotFound      = errors.New("instance not found")
	ErrForbidden     = errors.New("instance access forbidden")
	ErrUnsupported   = errors.New("instance action unsupported")
	ErrTradingLocked = errors.New("实例正在交易市场挂牌或锁定，暂不能执行操作")
)

type ProviderResolver interface {
	ProviderFor(string) (provider.UpstreamProvider, error)
	GetOrderPageByLocalProduct(string) (*contracts.OrderPage, error)
}

type Service struct {
	repo      repo.Repository
	providers ProviderResolver
	remote    *remoteconsole.Registry
	db        *gorm.DB
}

func NewService(repository repo.Repository, providers ProviderResolver) *Service {
	return &Service{repo: repository, providers: providers}
}

func (s *Service) SetRemoteConsole(registry *remoteconsole.Registry) { s.remote = registry }

func (s *Service) EnsureSchema(ctx context.Context) error { return s.repo.EnsureSchema(ctx) }

type ListInput struct {
	Page, PageSize int
	Search         string
	Status         *uint8
}

type ListItem struct {
	Remark       string `json:"remark"`
	ID           string `json:"id"`
	Name         string `json:"name"`
	ProductName  string `json:"productName"`
	PrimaryIP    string `json:"primaryIp,omitempty"`
	Status       uint8  `json:"status"`
	PowerStatus  string `json:"powerStatus,omitempty"`
	BillingCycle string `json:"billingCycle,omitempty"`
	ExpiresAt    string `json:"expiresAt,omitempty"`
	CreatedAt    string `json:"createdAt"`
}

type ListResult struct {
	Items    []ListItem `json:"items"`
	Total    int64      `json:"total"`
	Page     int        `json:"page"`
	PageSize int        `json:"pageSize"`
}

type ConfigItem struct {
	ID           string `json:"id"`
	Field        string `json:"field,omitempty"`
	DisplayName  string `json:"displayName"`
	DisplayValue string `json:"displayValue"`
	Position     int    `json:"position"`
}

type EventView struct {
	ID          string `json:"id"`
	Type        string `json:"type"`
	Status      uint8  `json:"status"`
	Message     string `json:"message,omitempty"`
	CreatedAt   string `json:"createdAt"`
	ProcessedAt string `json:"processedAt,omitempty"`
}

type DetailView struct {
	Username           string                         `json:"username,omitempty"`
	Port               string                         `json:"port,omitempty"`
	PortIsDefault      bool                           `json:"portIsDefault,omitempty"`
	Remark             string                         `json:"remark,omitempty"`
	UpstreamOrderID    string                         `json:"upstreamOrderId,omitempty"`
	UpstreamOrderNo    string                         `json:"upstreamOrderNo,omitempty"`
	InvoiceID          string                         `json:"invoiceId,omitempty"`
	FirstPayment       string                         `json:"firstPayment,omitempty"`
	RenewalAmount      string                         `json:"renewalAmount,omitempty"`
	AutoRenew          bool                           `json:"autoRenew"`
	ID                 string                         `json:"id"`
	Name               string                         `json:"name"`
	OrderID            string                         `json:"orderId"`
	OrderNo            string                         `json:"orderNo,omitempty"`
	ProductID          string                         `json:"productId"`
	ProductName        string                         `json:"productName"`
	ModuleType         string                         `json:"moduleType"`
	UpstreamInstanceID string                         `json:"upstreamInstanceId,omitempty"`
	Hostname           string                         `json:"hostname,omitempty"`
	PrimaryIP          string                         `json:"primaryIp,omitempty"`
	SubnetMask         string                         `json:"subnetMask,omitempty"`
	Gateway            string                         `json:"gateway,omitempty"`
	OperatingSystem    string                         `json:"operatingSystem,omitempty"`
	CurrentImageID     string                         `json:"currentImageId,omitempty"`
	CurrentImageName   string                         `json:"currentImageName,omitempty"`
	CurrentImageGroup  string                         `json:"currentImageGroup,omitempty"`
	Status             uint8                          `json:"status"`
	PowerStatus        string                         `json:"powerStatus,omitempty"`
	BillingCycle       string                         `json:"billingCycle,omitempty"`
	PasswordMasked     string                         `json:"passwordMasked,omitempty"`
	ActivatedAt        string                         `json:"activatedAt,omitempty"`
	ExpiresAt          string                         `json:"expiresAt,omitempty"`
	CreatedAt          string                         `json:"createdAt"`
	Config             []ConfigItem                   `json:"config"`
	IPs                []contracts.InstanceIP         `json:"ips"`
	Capabilities       contracts.InstanceCapabilities `json:"capabilities"`
	Events             []EventView                    `json:"events"`
	Warning            string                         `json:"warning,omitempty"`
}

type ImageItem struct {
	ID      string  `json:"id"`
	Version string  `json:"version"`
	Pricing float64 `json:"pricing"`
}

type ImageGroup struct {
	Name  string      `json:"name"`
	Child []ImageItem `json:"child"`
}

type ImageListView struct {
	CurrentImageID    string       `json:"currentImageId,omitempty"`
	CurrentImageName  string       `json:"currentImageName,omitempty"`
	CurrentImageGroup string       `json:"currentImageGroup,omitempty"`
	Groups            []ImageGroup `json:"groups"`
}

type ActionInput struct {
	Action         string `json:"action"`
	Password       string `json:"password,omitempty"`
	ImageID        string `json:"imageId,omitempty"`
	Port           string `json:"port,omitempty"`
	FormatDataDisk bool   `json:"formatDataDisk,omitempty"`
}

type ActionView struct {
	Status  string                       `json:"status,omitempty"`
	Message string                       `json:"message"`
	Remote  *remoteconsole.RemoteSession `json:"remote,omitempty"`
}

type rawSnapshot struct {
	ConfigOptions map[string]struct {
		Value        string `json:"value"`
		Field        string `json:"field"`
		DisplayName  string `json:"displayName"`
		DisplayValue string `json:"displayValue"`
		Position     int    `json:"position"`
	} `json:"configOptions"`
	Hostname           string `json:"hostname"`
	BillingCycle       string `json:"billingCycle"`
	PasswordCiphertext string `json:"passwordCiphertext"`
}

func (s *Service) List(ctx context.Context, customerID int64, in ListInput) (*ListResult, error) {
	if in.Page < 1 {
		in.Page = 1
	}
	if in.PageSize < 1 || in.PageSize > 100 {
		in.PageSize = 20
	}
	filters := map[string]any{"customer_id": customerID, "search": in.Search}
	if in.Status != nil {
		filters["status"] = *in.Status
	}
	rows, total, err := s.repo.List(ctx, in.Page, in.PageSize, filters)
	if err != nil {
		return nil, err
	}
	items := make([]ListItem, 0, len(rows))
	for _, row := range rows {
		items = append(items, listItem(row))
	}
	return &ListResult{Items: items, Total: total, Page: in.Page, PageSize: in.PageSize}, nil
}

func (s *Service) LegacyList(ctx context.Context, customerID int64) ([]ListItem, error) {
	result, err := s.List(ctx, customerID, ListInput{Page: 1, PageSize: 100})
	if err != nil {
		return nil, err
	}
	return result.Items, nil
}

func (s *Service) Detail(ctx context.Context, customerID, instanceID int64) (*DetailView, error) {
	ctx, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()
	host, err := s.owned(ctx, customerID, instanceID)
	if err != nil {
		return nil, err
	}

	orderNo, _ := s.repo.FindOrderNo(ctx, host.OrderID)
	config, snapshot := parseSnapshot(host.ConfigSnapshot)
	events, _ := s.repo.ListEvents(ctx, host.ID, 50)
	result := &DetailView{
		ID: strconv.FormatInt(host.ID, 10), Name: host.InstanceNo, Remark: host.Remark,
		OrderID: strconv.FormatInt(host.OrderID, 10), OrderNo: orderNo,
		ProductID: strconv.FormatInt(host.ProductID, 10), ProductName: host.ProductName,
		ModuleType: host.ModuleType, UpstreamInstanceID: host.UpstreamInstanceID,
		Hostname: snapshot.Hostname, PrimaryIP: host.PrimaryIP, SubnetMask: host.SubnetMask,
		Gateway: host.Gateway, OperatingSystem: host.OperatingSystem,
		Status: host.Status, PowerStatus: host.PowerStatus, BillingCycle: snapshot.BillingCycle,
		CreatedAt: formatTime(host.CreatedAt), Config: config, Events: eventViews(events),
	}
	result.CurrentImageID, result.CurrentImageName = snapshotImage(snapshot)
	result.CurrentImageGroup = imageGroupName(result.CurrentImageName)
	if result.OperatingSystem == "" {
		result.OperatingSystem = result.CurrentImageName
	}
	if snapshot.PasswordCiphertext != "" {
		result.PasswordMasked = "************"
	}
	if host.ActivatedAt != nil {
		result.ActivatedAt = formatTime(*host.ActivatedAt)
	}
	if host.ExpiresAt != nil {
		result.ExpiresAt = formatTime(*host.ExpiresAt)
	}

	upstream, warning := s.providerContext(ctx, host)
	if warning != "" {
		result.Warning = warning
		return result, nil
	}
	detail, err := upstream.GetInstanceDetail(parseHostID(host.UpstreamInstanceID))
	if err != nil || detail == nil || detail.ID != host.UpstreamInstanceID {
		result.Warning = "实例实时信息暂时不可用，请稍后重试"
		return result, nil
	}
	result.Username, result.Port = detail.Username, detail.Port
	result.PortIsDefault = detail.PortIsDefault
	result.UpstreamOrderID, result.UpstreamOrderNo, result.InvoiceID = detail.UpstreamOrderID, detail.UpstreamOrderNo, detail.InvoiceID
	result.FirstPayment = detail.FirstPayment
	if s.db != nil {
		if cost, e := balance.ParseMoney(detail.RenewalAmount); e == nil && cost > 0 {
			var pricing struct {
				Rate   string
				Profit string
				Kind   int
			}
			s.db.WithContext(ctx).Raw("SELECT s.rate,u.renew_profit_value profit,u.renew_profit_type kind FROM common.suppliers s JOIN product.upstream_products u ON u.supplier_id=s.id WHERE u.product_id=? AND s.id=?", host.ProductID, host.UpstreamProviderID).Scan(&pricing)
			if amount, e := retailRenewal(cost, pricing.Rate, pricing.Profit, pricing.Kind); e == nil {
				result.RenewalAmount = amount.String()
			}
		}
	}
	if detail.Password != "" {
		result.PasswordMasked = "************"
	}
	if len(detail.Config) > 0 {
		raw, _ := json.Marshal(snapshotFromUpstream(detail))
		result.Config, _ = parseSnapshot(string(raw))
	}
	if detail.ExpiresAt != nil {
		result.ExpiresAt = formatTime(*detail.ExpiresAt)
	}
	if s.db != nil {
		var settings RenewalSettings
		s.db.WithContext(ctx).Where("instance_id=? AND customer_id=?", host.ID, customerID).Take(&settings)
		result.AutoRenew = settings.Enabled
	}
	result.Capabilities = detail.Capabilities
	result.IPs = detail.IPs
	if detail.Hostname != "" {
		result.Hostname = detail.Hostname
	}
	if detail.PrimaryIP != "" {
		result.PrimaryIP = detail.PrimaryIP
	}
	if detail.SubnetMask != "" {
		result.SubnetMask = detail.SubnetMask
	}
	if detail.Gateway != "" {
		result.Gateway = detail.Gateway
	}
	if detail.OperatingSystem != "" {
		result.OperatingSystem = detail.OperatingSystem
		if result.CurrentImageName != detail.OperatingSystem {
			result.CurrentImageID = ""
		}
		result.CurrentImageName = detail.OperatingSystem
		result.CurrentImageGroup = imageGroupName(detail.OperatingSystem)
	}
	if detail.PowerStatus != "" {
		result.PowerStatus = detail.PowerStatus
	}
	status := statusCode(detail.PowerStatus, host.Status)
	if lifecycle := unavailableLifecycle(detail, host.UpstreamInstanceID); lifecycle != "" {
		result.PowerStatus = lifecycle
		status = statusCode(lifecycle, host.Status)
		result.Capabilities = contracts.InstanceCapabilities{}
	}
	result.Status = status
	now := time.Now()
	updates := map[string]any{
		"primary_ip": result.PrimaryIP, "subnet_mask": result.SubnetMask, "gateway": result.Gateway,
		"operating_system": result.OperatingSystem, "last_synced_at": now,
	}
	if s.db != nil {
		_ = s.db.WithContext(ctx).Model(&dom.Host{}).Where("id=? AND customer_id=? AND ownership_version=? AND upstream_instance_id=?", host.ID, customerID, host.OwnershipVersion, host.UpstreamInstanceID).Updates(updates).Error
	} else {
		_ = s.repo.UpdateFields(ctx, host.ID, updates)
	}
	return result, nil
}

func (s *Service) Images(ctx context.Context, customerID, instanceID int64) (*ImageListView, error) {
	ctx, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()
	host, err := s.owned(ctx, customerID, instanceID)
	if err != nil {
		return nil, err
	}
	if s.providers == nil {
		return nil, fmt.Errorf("%w: 该实例没有可用的镜像目录", ErrUnsupported)
	}
	var page *contracts.OrderPage
	if scoped, ok := s.providers.(interface {
		GetInstanceOrderPage(context.Context, string) (*contracts.OrderPage, error)
	}); ok {
		page, err = scoped.GetInstanceOrderPage(ctx, strconv.FormatInt(host.ProductID, 10))
	} else {
		page, err = s.providers.GetOrderPageByLocalProduct(strconv.FormatInt(host.ProductID, 10))
	}
	if err != nil {
		return nil, errors.New("读取商品镜像目录失败，请稍后重试")
	}
	_, snapshot := parseSnapshot(host.ConfigSnapshot)
	currentID, currentName := snapshotImage(snapshot)
	if host.OperatingSystem != "" {
		currentName = cleanImageName(host.OperatingSystem)
		currentID = ""
	}
	result := &ImageListView{CurrentImageID: currentID, CurrentImageName: currentName, CurrentImageGroup: imageGroupName(currentName), Groups: []ImageGroup{}}
	groups := page.ImageGroups
	if len(groups) == 0 && len(page.Images) > 0 {
		groups = []contracts.ImageGroup{{Name: "其他", Child: page.Images}}
	}
	for _, group := range groups {
		view := ImageGroup{Name: group.Name, Child: make([]ImageItem, 0, len(group.Child))}
		for _, image := range group.Child {
			view.Child = append(view.Child, ImageItem{ID: strconv.Itoa(image.ID), Version: cleanImageName(image.Name), Pricing: image.Price})
			if (currentID != "" && strconv.Itoa(image.ID) == currentID) || (currentID == "" && strings.EqualFold(cleanImageName(image.Name), currentName)) {
				result.CurrentImageID = strconv.Itoa(image.ID)
				result.CurrentImageName = cleanImageName(image.Name)
				result.CurrentImageGroup = group.Name
			}
		}
		if len(view.Child) > 0 {
			result.Groups = append(result.Groups, view)
		}
	}
	return result, nil
}

func (s *Service) Monitor(ctx context.Context, customerID, instanceID int64, start, end time.Time) (*contracts.InstanceMonitor, error) {
	ctx, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()
	host, err := s.owned(ctx, customerID, instanceID)
	if err != nil {
		return nil, err
	}
	upstream, warning := s.providerContext(ctx, host)
	if warning != "" {
		return &contracts.InstanceMonitor{Available: false, Message: warning}, nil
	}
	monitor, err := upstream.GetInstanceMonitor(parseHostID(host.UpstreamInstanceID), start, end)
	if err != nil {
		return &contracts.InstanceMonitor{Available: false, Message: "监控数据暂时不可用，请稍后重试"}, nil
	}
	return monitor, nil
}

func (s *Service) Action(ctx context.Context, customerID, instanceID int64, in ActionInput) (*ActionView, error) {
	host, err := s.owned(ctx, customerID, instanceID)
	if err != nil {
		return nil, err
	}
	if host.Status == 6 {
		return nil, ErrUnsupported
	}
	locked, err := s.repo.IsMarketplaceLocked(ctx, instanceID)
	if err != nil {
		return nil, err
	}
	if locked {
		return nil, ErrTradingLocked
	}
	in.Action = strings.TrimSpace(strings.ToLower(in.Action))
	if in.Action == "destroy" {
		return nil, ErrUnsupported
	}
	if in.Action == "reset_password" && !validPassword(in.Password) {
		return nil, errors.New("新密码至少 12 位，且必须包含大小写字母和数字")
	}
	if in.Action == "reinstall" && strings.TrimSpace(in.ImageID) == "" {
		return nil, errors.New("请选择重装镜像")
	}
	if in.Action == "reinstall" {
		imageID, parseErr := strconv.Atoi(strings.TrimSpace(in.ImageID))
		if parseErr != nil || imageID <= 0 {
			return nil, errors.New("镜像 ID 格式错误")
		}
		if strings.TrimSpace(in.Port) == "" {
			return nil, errors.New("请填写重装后的 SSH / RDP 端口")
		}
		port, portErr := strconv.Atoi(strings.TrimSpace(in.Port))
		if portErr != nil || port < 1 || port > 65535 {
			return nil, errors.New("端口必须是 1 到 65535 之间的整数")
		}
		images, imageErr := s.Images(ctx, customerID, instanceID)
		if imageErr != nil {
			return nil, imageErr
		}
		allowed := false
		for _, group := range images.Groups {
			for _, image := range group.Child {
				if image.ID == strconv.Itoa(imageID) {
					allowed = true
					break
				}
			}
		}
		if !allowed {
			return nil, errors.New("image_not_found: 所选镜像不属于该商品或已停用")
		}
		in.ImageID = strconv.Itoa(imageID)
		in.Port = strconv.Itoa(port)
	}
	if in.Action == "remote" {
		return s.createRemoteSession(ctx, host, customerID)
	}
	ctx, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()
	upstream, warning := s.providerContext(ctx, host)
	if warning != "" {
		return nil, fmt.Errorf("%w: %s", ErrUnsupported, warning)
	}
	request, _ := json.Marshal(map[string]any{
		"action": in.Action, "imageId": in.ImageID, "port": in.Port, "formatDataDisk": in.FormatDataDisk,
	})
	operatorType, operatorID := actionActor(ctx, customerID)
	event := &dom.Event{InstanceID: host.ID, EventType: in.Action, FromStatus: &host.Status, OperatorType: operatorType, OperatorID: &operatorID, Status: 1, UpstreamRequest: string(request), UpstreamResponse: "{}"}
	if err := s.repo.CreateEvent(ctx, event); err != nil {
		return nil, err
	}
	result, err := upstream.ExecuteInstanceAction(parseHostID(host.UpstreamInstanceID), contracts.InstanceActionInput{
		Action: in.Action, Password: in.Password, ImageID: in.ImageID, Port: in.Port, FormatDataDisk: in.FormatDataDisk,
	})
	if err != nil {
		message := "实例操作暂时不可用，请稍后重试"
		_ = s.repo.FinishEvent(ctx, event.ID, 3, nil, "", message)
		return nil, errors.New(message)
	}
	power := normalizePower(result.Status)
	if power == "" || power == "operating" {
		if v := actionPower(in.Action); v != "" {
			power = v
		}
	}
	toStatus := statusCode(power, host.Status)
	response, _ := json.Marshal(result)
	_ = s.repo.FinishEvent(ctx, event.ID, 2, &toStatus, string(response), "")
	_ = s.repo.UpdateFields(ctx, host.ID, map[string]any{"status": toStatus, "power_status": power})
	return &ActionView{Status: power, Message: "操作已受理，正在同步实际状态"}, nil
}

func (s *Service) createRemoteSession(ctx context.Context, host *dom.Host, customerID int64) (*ActionView, error) {
	if s.remote == nil {
		return nil, remoteconsole.ErrEngineUnavailable
	}
	operatorType, operatorID := actionActor(ctx, customerID)
	event := &dom.Event{
		InstanceID: host.ID, EventType: "remote", FromStatus: &host.Status,
		OperatorType: operatorType, OperatorID: &operatorID, Status: 1,
		UpstreamRequest: `{"action":"remote"}`, UpstreamResponse: "{}",
	}
	if err := s.repo.CreateEvent(ctx, event); err != nil {
		return nil, err
	}
	session, err := s.remote.CreateSession(ctx, host.ID, customerID)
	if err != nil {
		_ = s.repo.FinishEvent(ctx, event.ID, 3, nil, "", "远程控制暂时不可用")
		return nil, err
	}
	safeResponse, _ := json.Marshal(map[string]any{
		"type": session.Type, "expiresAt": session.ExpiresAt,
	})
	_ = s.repo.FinishEvent(ctx, event.ID, 2, nil, string(safeResponse), "")
	return &ActionView{Status: "ready", Message: "远程会话已创建", Remote: session}, nil
}

// CreateRemoteUpstream is called only by a server-side console provider. The
// returned endpoint and password never cross an HTTP serialization boundary.
func (s *Service) CreateRemoteUpstream(ctx context.Context, instanceID, customerID int64) (*remoteconsole.RemoteUpstream, error) {
	ctx, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()
	host, err := s.owned(ctx, customerID, instanceID)
	if err != nil {
		return nil, err
	}
	upstream, warning := s.providerContext(ctx, host)
	if warning != "" {
		return nil, remoteconsole.ErrUpstreamUnavailable
	}
	result, err := upstream.ExecuteInstanceAction(parseHostID(host.UpstreamInstanceID), contracts.InstanceActionInput{Action: "remote"})
	if err != nil || result == nil || strings.TrimSpace(result.RemoteEndpoint) == "" {
		return nil, remoteconsole.ErrUpstreamUnavailable
	}
	return &remoteconsole.RemoteUpstream{URL: result.RemoteEndpoint, Password: result.RemotePassword}, nil
}

func (s *Service) ClaimRemoteSession(ctx context.Context, customerID, instanceID int64, token string) (*remoteconsole.RemoteSession, error) {
	if _, err := s.owned(ctx, customerID, instanceID); err != nil {
		return nil, err
	}
	if s.remote == nil {
		return nil, remoteconsole.ErrEngineUnavailable
	}
	return s.remote.Claim(instanceID, customerID, strings.TrimSpace(token))
}

func (s *Service) ProxyRemoteSession(ctx context.Context, session *remoteconsole.RemoteSession, client net.Conn) error {
	if s.remote == nil {
		return remoteconsole.ErrEngineUnavailable
	}
	return s.remote.Proxy(ctx, session, client)
}

func (s *Service) DiscardRemoteSession(session *remoteconsole.RemoteSession) {
	if s.remote != nil {
		s.remote.Discard(session)
	}
}

func (s *Service) RemoteOwnership(ctx context.Context, instanceID, customerID int64) (int64, error) {
	host, err := s.owned(ctx, customerID, instanceID)
	if err != nil {
		return 0, err
	}
	return host.OwnershipVersion, nil
}

func (s *Service) owned(ctx context.Context, customerID, instanceID int64) (*dom.Host, error) {
	host, err := s.repo.FindByID(ctx, instanceID)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if host.CustomerID != customerID {
		return nil, ErrForbidden
	}
	return host, nil
}

func (s *Service) provider(host *dom.Host) (contracts.InstanceProvider, string) {
	if s.providers == nil || host.UpstreamProviderID == nil || strings.TrimSpace(host.UpstreamInstanceID) == "" {
		return nil, "该实例没有可用的上游操作通道"
	}
	resolved, err := s.providers.ProviderFor(strconv.FormatInt(*host.UpstreamProviderID, 10))
	if err != nil {
		return nil, "实例操作通道暂时不可用"
	}
	instanceProvider, ok := resolved.(contracts.InstanceProvider)
	if !ok {
		return nil, "当前供应商未提供已验证的实例操作协议"
	}
	return instanceProvider, ""
}

func listItem(host *dom.Host) ListItem {
	_, snapshot := parseSnapshot(host.ConfigSnapshot)
	result := ListItem{ID: strconv.FormatInt(host.ID, 10), Name: host.InstanceNo, Remark: host.Remark, ProductName: host.ProductName, PrimaryIP: host.PrimaryIP, Status: host.Status, PowerStatus: host.PowerStatus, BillingCycle: snapshot.BillingCycle, CreatedAt: formatTime(host.CreatedAt)}
	if host.ExpiresAt != nil {
		result.ExpiresAt = formatTime(*host.ExpiresAt)
	}
	return result
}

func parseSnapshot(raw string) ([]ConfigItem, rawSnapshot) {
	var snapshot rawSnapshot
	if json.Unmarshal([]byte(raw), &snapshot) != nil {
		return []ConfigItem{}, rawSnapshot{}
	}
	items := make([]ConfigItem, 0, len(snapshot.ConfigOptions))
	for id, option := range snapshot.ConfigOptions {
		items = append(items, ConfigItem{ID: id, Field: option.Field, DisplayName: option.DisplayName, DisplayValue: option.DisplayValue, Position: option.Position})
	}
	sort.SliceStable(items, func(i, j int) bool {
		if items[i].Position == items[j].Position {
			return items[i].ID < items[j].ID
		}
		return items[i].Position < items[j].Position
	})
	return items, snapshot
}

func snapshotImage(snapshot rawSnapshot) (string, string) {
	for _, option := range snapshot.ConfigOptions {
		if option.Field == "image" || strings.Contains(option.DisplayName, "操作系统") {
			return strings.TrimSpace(option.Value), cleanImageName(option.DisplayValue)
		}
	}
	return "", ""
}

func cleanImageName(value string) string {
	value = strings.TrimSpace(value)
	if index := strings.Index(value, " ¥"); index >= 0 {
		value = value[:index]
	}
	return strings.TrimSpace(value)
}

func imageGroupName(value string) string {
	value = cleanImageName(value)
	if value == "" {
		return ""
	}
	for _, separator := range []string{"-", " ", "_"} {
		if index := strings.Index(value, separator); index > 0 {
			return value[:index]
		}
	}
	return value
}

func eventViews(events []*dom.Event) []EventView {
	out := make([]EventView, 0, len(events))
	for _, event := range events {
		message := event.ErrorMessage
		if message == "" && event.Status == 2 {
			message = "操作已提交至上游"
		}
		view := EventView{ID: strconv.FormatInt(event.ID, 10), Type: event.EventType, Status: event.Status, Message: message, CreatedAt: formatTime(event.CreatedAt)}
		if event.ProcessedAt != nil {
			view.ProcessedAt = formatTime(*event.ProcessedAt)
		}
		out = append(out, view)
	}
	return out
}

func parseHostID(value string) int64 {
	id, _ := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
	return id
}
func formatTime(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return value.Format(time.RFC3339)
}

func statusCode(value string, fallback uint8) uint8 {
	switch normalizePower(value) {
	case "running":
		return 3
	case "stopped":
		return 4
	case "provisioning", "starting", "stopping", "rebooting", "reinstalling", "operating", "migrating":
		return 2
	case "expired":
		return 5
	case "deleted":
		return 6
	}
	return fallback
}

func normalizePower(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "on", "running", "active", "waiting", "1":
		return "running"
	case "off", "stopped", "0":
		return "stopped"
	case "start", "starting", "powering_on", "booting":
		return "starting"
	case "stop", "stopping", "powering_off", "shutting_down":
		return "stopping"
	case "reboot", "rebooting", "restarting":
		return "rebooting"
	case "reinstall", "reinstalling", "installing":
		return "reinstalling"
	case "pending", "creating", "provisioning":
		return "provisioning"
	case "task", "process", "operating":
		return "operating"
	case "cold_migrate", "hot_migrate", "migrating":
		return "migrating"
	case "fault", "error", "failed":
		return "fault"
	case "suspended":
		return "suspended"
	case "expired":
		return "expired"
	case "deleted", "terminated", "cancelled":
		return "deleted"
	case "":
		return ""
	default:
		return "unknown"
	}
}

func validPassword(value string) bool {
	if len(value) < 12 {
		return false
	}
	var upper, lower, digit bool
	for _, r := range value {
		switch {
		case r >= 'A' && r <= 'Z':
			upper = true
		case r >= 'a' && r <= 'z':
			lower = true
		case r >= '0' && r <= '9':
			digit = true
		}
	}
	return upper && lower && digit
}
