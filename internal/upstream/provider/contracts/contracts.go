package contracts

import (
	"context"
	"encoding/json"
	"errors"
	"time"
)

var ErrProductUnavailable = errors.New("上游商品已下架或不可代理")

type Supplier struct {
	ID, Name, Type, URL, Username, Token, Secret string
	Rate                                         float64
	CurrencyCode                                 string
	Status                                       int
	// Catalogue imports opt in; user machine operations stay unthrottled.
	LimitRequests bool
	// AuthorizeRequest rechecks module and account state before every external attempt.
	AuthorizeRequest func(method, path string) error `json:"-"`
}
type UpstreamProduct struct {
	ID                 int             `json:"id,string"`
	Name               string          `json:"name"`
	Description        string          `json:"description"`
	Price              float64         `json:"price"`
	Cycle              string          `json:"cycle"`
	PayType            string          `json:"payType"`
	Type               string          `json:"type"`
	GroupID            int             `json:"groupId,string"`
	CurrencyCode       string          `json:"currencyCode"`
	SetupFee           float64         `json:"setupFee"`
	UpstreamPriceType  string          `json:"upstreamPriceType"`
	UpstreamPriceValue float64         `json:"upstreamPriceValue"`
	Stock              int             `json:"stock"`
	StockValue         json.RawMessage `json:"stockValue,omitempty"`
	TrialEnabled       bool            `json:"trialEnabled"`
	TrialPrice         float64         `json:"trialPrice"`
}
type UpstreamProductDetail struct {
	UpstreamProduct
	Config           map[string]string `json:"config"`
	HasConfigOptions bool              `json:"hasConfigOptions"`
	// ConfigOptions is the supplier product-detail configoptions field. It is
	// a provider resource schema; finance providers can additionally expose
	// resource choices through the order-page schema.
	ConfigOptions []ConfigOption `json:"configOptions"`
}

// ConfigOption describes a selectable or quantity based upstream purchase
// option. It deliberately represents the upstream shape rather than a UI
// component, so every provider can expose its native ordering constraints.
type ConfigCondition struct {
	OptionID int      `json:"optionId,string"`
	Values   []string `json:"values"`
}
type ConfigOption struct {
	VisibleWhen []ConfigCondition   `json:"visibleWhen,omitempty"`
	SourceType  string              `json:"sourceType,omitempty"`
	ID          int                 `json:"id,string"`
	Name        string              `json:"name"`
	Field       string              `json:"field"`
	OptionType  string              `json:"optionType"`
	Required    bool                `json:"required"`
	Options     []ConfigOptionValue `json:"options"`
	Min         float64             `json:"min"`
	Max         float64             `json:"max"`
	Step        float64             `json:"step"`
	Unit        string              `json:"unit"`
	Default     string              `json:"default"`
}

type ConfigOptionValue struct {
	Children   []ConfigOptionValue `json:"children,omitempty"`
	ID         int                 `json:"id,string"`
	Name       string              `json:"name"`
	Price      float64             `json:"price"`
	PriceKnown *bool               `json:"priceKnown,omitempty"`
	Min        float64             `json:"min,omitempty"`
	Max        float64             `json:"max,omitempty"`
	Step       float64             `json:"step,omitempty"`
}

// ImageGroup preserves the finance order-page operating-system hierarchy.
// The image ID is the value required by provision/default when reinstalling.
type ImageGroup struct {
	Name  string              `json:"name"`
	Child []ConfigOptionValue `json:"child"`
}

type CycleItem struct {
	Billingcycle string  `json:"billingcycle"`
	Label        string  `json:"label"`
	Price        float64 `json:"price"`
	SetupFee     float64 `json:"setupFee"`
}

// OrderPage is a provider-neutral ordering schema. OptionalProvider keeps
// existing providers source-compatible when they cannot offer configuration.
type CustomField struct {
	ID         int      `json:"id,string"`
	Name       string   `json:"name"`
	Type       string   `json:"type"`
	Required   bool     `json:"required"`
	Choices    []string `json:"choices,omitempty"`
	Validation string   `json:"validation,omitempty"`
}
type OrderPage struct {
	CustomFields    []CustomField       `json:"customFields,omitempty"`
	Product         UpstreamProduct     `json:"product"`
	DataCenters     []ConfigOptionValue `json:"dataCenters"`
	Images          []ConfigOptionValue `json:"images"`
	ImageGroups     []ImageGroup        `json:"imageGroups"`
	ConfigOptions   []ConfigOption      `json:"configOptions"`
	Cycles          []CycleItem         `json:"cycles"`
	DefaultPassword string              `json:"defaultPassword"`
	NetworkTypes    []ConfigOptionValue `json:"networkTypes"`
}
type ProductResource struct{ Name, Version, DownloadURL string }
type OrderInfo struct {
	ProcurementCeiling string `json:"-"`
	OrderID            string
	ProductID          int
	Quantity           int
	Cycle              string
	Amount             float64
	CustomerID         string
	ConfigOptions      map[string]string `json:"configOptions"`
	CustomFields       map[string]string `json:"customFields"`
	Hostname           string            `json:"hostname"`
	Password           string            `json:"password"`
	DataCenterID       int               `json:"dataCenterId"`
	ImageID            int               `json:"imageId"`
	NetworkType        string            `json:"networkType"`
	DownstreamURL      string            `json:"downstreamUrl"`
	DownstreamToken    string            `json:"downstreamToken"`
	DownstreamHostID   string            `json:"downstreamHostId"`
	// Checkpoint must persist remote identifiers before the next financial step.
	Checkpoint func(stage, invoiceID, hostID string) error `json:"-"`
}
type UpstreamOrderResult struct {
	OrderID, Status, HostID string
	Cost                    float64
	BalanceBefore           float64
	BalanceAfter            float64
}

// InvoiceHostMatch is a read-only reconciliation fact: the upstream host whose
// paid invoice matches a procurement record whose payment response was lost.
// It intentionally carries no credentials or procurement cost.
type InvoiceHostMatch struct {
	HostID    string `json:"hostId"`
	InvoiceID string `json:"invoiceId"`
	OrderID   string `json:"orderId"`
	ProductID string `json:"productId"`
	Status    string `json:"status"`
	PrimaryIP string `json:"primaryIp,omitempty"`
	Domain    string `json:"domain,omitempty"`
	CreatedAt string `json:"createdAt,omitempty"`
	ExpiresAt string `json:"expiresAt,omitempty"`
}
type RenewResult struct {
	Status, DueTime string
	Cost            float64
}
type UpgradeConfig struct {
	ProductID int
	Config    map[string]string
}
type UpgradeResult struct {
	Status     string
	Difference float64
}
type WebhookEvent struct {
	HostID, Action, Status string
	DueTime                int64
	Raw                    map[string]interface{}
}
type Provider interface {
	Auth() error
	GetProductList() ([]UpstreamProduct, error)
	GetProductDetail(int) (*UpstreamProductDetail, error)
	GetProductResource(int) (*ProductResource, error)
	CreateOrder(OrderInfo) (*UpstreamOrderResult, error)
	CreateHost(int64) error
	SuspendHost(int64, string) error
	UnsuspendHost(int64) error
	TerminateHost(int64) error
	RenewHost(int64, string) (*RenewResult, error)
	UpgradeHost(int64, UpgradeConfig) (*UpgradeResult, error)
	RebootHost(int64) error
	ReinstallHost(int64, string) error
	VerifyWebhook(string, []byte) bool
	ParseWebhook([]byte) (*WebhookEvent, error)
}

// OptionalProvider is implemented only by suppliers that can describe their
// upstream ordering configuration. Services must type-assert this interface.
type OptionalProvider interface {
	Provider
	GetOrderPage(productID int) (*OrderPage, error)
}

// BalanceProvider is implemented by finance suppliers that expose account
// credit. Keeping it optional prevents non-finance adapters from inventing a
// balance result.
type BalanceProvider interface {
	GetBalance() (float64, error)
}

// InstanceCapabilities is returned by adapters only for operations whose
// upstream protocol is known. The customer UI must never infer capabilities
// from a product name or render an unsupported write action as successful.
type InstanceCapabilities struct {
	Start         bool `json:"start"`
	Stop          bool `json:"stop"`
	Reboot        bool `json:"reboot"`
	HardStop      bool `json:"hardStop"`
	HardReboot    bool `json:"hardReboot"`
	Rescue        bool `json:"rescue"`
	ExitRescue    bool `json:"exitRescue"`
	Reinstall     bool `json:"reinstall"`
	ResetPassword bool `json:"resetPassword"`
	Traffic       bool `json:"traffic"`
	Remote        bool `json:"remote"`
	NAT           bool `json:"nat"`
	Destroy       bool `json:"destroy"`
}

type InstanceIP struct {
	Address    string `json:"address"`
	SubnetMask string `json:"subnetMask,omitempty"`
	Gateway    string `json:"gateway,omitempty"`
}

type InstanceDetail struct {
	Username        string               `json:"username,omitempty"`
	Password        string               `json:"-"`
	Port            string               `json:"port,omitempty"`
	PortIsDefault   bool                 `json:"portIsDefault,omitempty"`
	Remark          string               `json:"remark,omitempty"`
	UpstreamOrderID string               `json:"upstreamOrderId,omitempty"`
	UpstreamOrderNo string               `json:"upstreamOrderNo,omitempty"`
	InvoiceID       string               `json:"invoiceId,omitempty"`
	FirstPayment    string               `json:"firstPayment,omitempty"`
	RenewalAmount   string               `json:"renewalAmount,omitempty"`
	ID              string               `json:"id,omitempty"`
	ProductID       string               `json:"productId,omitempty"`
	BillingCycle    string               `json:"billingCycle,omitempty"`
	ActivatedAt     *time.Time           `json:"activatedAt,omitempty"`
	ExpiresAt       *time.Time           `json:"expiresAt,omitempty"`
	Config          []InstanceConfig     `json:"config,omitempty"`
	Hostname        string               `json:"hostname,omitempty"`
	PrimaryIP       string               `json:"primaryIp,omitempty"`
	SubnetMask      string               `json:"subnetMask,omitempty"`
	Gateway         string               `json:"gateway,omitempty"`
	OperatingSystem string               `json:"operatingSystem,omitempty"`
	Status          string               `json:"status,omitempty"`
	PowerStatus     string               `json:"powerStatus,omitempty"`
	IPs             []InstanceIP         `json:"ips"`
	Capabilities    InstanceCapabilities `json:"capabilities"`
}

// InstanceConfig contains actual provisioned values, never catalogue defaults.
type InstanceConfig struct {
	ID    string `json:"id"`
	Field string `json:"field"`
	Name  string `json:"name"`
	Value string `json:"value"`
}

type InstanceActionInput struct {
	Action         string `json:"action"`
	Password       string `json:"password,omitempty"`
	ImageID        string `json:"imageId,omitempty"`
	Port           string `json:"port,omitempty"`
	FormatDataDisk bool   `json:"formatDataDisk,omitempty"`
	RescueSystem   string `json:"rescueSystem,omitempty"`
	Force          bool   `json:"force,omitempty"`
}

type InstanceActionResult struct {
	Status         string `json:"status,omitempty"`
	Message        string `json:"message"`
	RemoteEndpoint string `json:"-"`
	RemotePassword string `json:"-"`
}

type MonitorPoint struct {
	Timestamp int64   `json:"timestamp"`
	Value     float64 `json:"value"`
}

type InstanceMonitor struct {
	CPU        []MonitorPoint `json:"cpu"`
	Memory     []MonitorPoint `json:"memory"`
	DiskIO     []MonitorPoint `json:"diskIo"`
	DiskRead   []MonitorPoint `json:"diskRead"`
	DiskWrite  []MonitorPoint `json:"diskWrite"`
	NetworkIn  []MonitorPoint `json:"networkIn"`
	NetworkOut []MonitorPoint `json:"networkOut"`
	Available  bool           `json:"available"`
	Message    string         `json:"message,omitempty"`
}

// InstanceProvider is optional because not every supplier exposes a verified
// customer-facing lifecycle protocol.
type InstanceProvider interface {
	GetInstanceDetail(hostID int64) (*InstanceDetail, error)
	GetInstanceStatus(hostID int64) (string, error)
	ExecuteInstanceAction(hostID int64, input InstanceActionInput) (*InstanceActionResult, error)
	GetInstanceMonitor(hostID int64, start, end time.Time) (*InstanceMonitor, error)
}

type NATRule struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	External string `json:"external"`
	Internal string `json:"internal"`
	Protocol string `json:"protocol"`
}
type NATRuleInput struct {
	Name     string `json:"name"`
	External string `json:"external"`
	Internal string `json:"internal"`
	Protocol string `json:"protocol"`
}
type NATProvider interface {
	ListNATRules(hostID int64) ([]NATRule, error)
	CreateNATRule(hostID int64, input NATRuleInput) (*NATRule, error)
	DeleteNATRule(hostID int64, ruleID string) error
}

// RenewalProvider separates quote, invoice creation and payment so uncertain writes are never repeated.
type RenewalQuote struct {
	// OriginalAmount is the undiscounted customer price; Amount remains procurement cost.
	OriginalAmount string `json:"originalAmount"`
	Cycle          string `json:"cycle"`
	Label          string `json:"label"`
	Amount         string `json:"amount"`
	Duration       int64  `json:"duration"`
}
type RenewalProvider interface {
	RenewalQuotes(int64) ([]RenewalQuote, error)
	CreateRenewalInvoice(int64, string, int64) (string, string, error)
	PayRenewalInvoice(string) error
}

// QuoteProvider has no procurement/cart mutation side effects. Decimal strings are yuan.
type QuoteRequest struct {
	ProductID     int
	Cycle         string
	Quantity      int
	ConfigOptions map[string]string
	CustomFields  map[string]string
}
type QuoteResult struct {
	Total       string
	RenewalUnit string
	SetupUnit   string
	Currency    string
}
type QuoteProvider interface {
	Quote(context.Context, QuoteRequest) (*QuoteResult, error)
}
