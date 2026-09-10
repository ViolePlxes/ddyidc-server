package contracts

import "time"

type Supplier struct {
	ID, Name, Type, URL, Username, Token, Secret string
	Rate                                         float64
	CurrencyCode                                 string
	Status                                       int
}
type UpstreamProduct struct {
	ID                 int     `json:"id,string"`
	Name               string  `json:"name"`
	Description        string  `json:"description"`
	Price              float64 `json:"price"`
	Cycle              string  `json:"cycle"`
	PayType            string  `json:"payType"`
	Type               string  `json:"type"`
	GroupID            int     `json:"groupId,string"`
	CurrencyCode       string  `json:"currencyCode"`
	SetupFee           float64 `json:"setupFee"`
	UpstreamPriceType  string  `json:"upstreamPriceType"`
	UpstreamPriceValue float64 `json:"upstreamPriceValue"`
	Stock              int     `json:"stock"`
	TrialEnabled       bool    `json:"trialEnabled"`
	TrialPrice         float64 `json:"trialPrice"`
}
type UpstreamProductDetail struct {
	UpstreamProduct
	Config           map[string]string `json:"config"`
	HasConfigOptions bool              `json:"hasConfigOptions"`
	// ConfigOptions is the supplier product-detail configoptions field. It is
	// the sole authority for whether the mapped SKU is customer-configurable;
	// cart/set_config is a cart presentation schema, not this signal.
	ConfigOptions []ConfigOption `json:"configOptions"`
}

// ConfigOption describes a selectable or quantity based upstream purchase
// option. It deliberately represents the upstream shape rather than a UI
// component, so every provider can expose its native ordering constraints.
type ConfigOption struct {
	ID         int                 `json:"id,string"`
	Name       string              `json:"name"`
	Field      string              `json:"field"`
	OptionType string              `json:"optionType"`
	Required   bool                `json:"required"`
	Options    []ConfigOptionValue `json:"options"`
	Min        float64             `json:"min"`
	Max        float64             `json:"max"`
	Step       float64             `json:"step"`
	Unit       string              `json:"unit"`
	Default    string              `json:"default"`
}

type ConfigOptionValue struct {
	ID    int     `json:"id,string"`
	Name  string  `json:"name"`
	Price float64 `json:"price"`
	Min   float64 `json:"min,omitempty"`
	Max   float64 `json:"max,omitempty"`
	Step  float64 `json:"step,omitempty"`
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
type OrderPage struct {
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
	OrderID          string
	ProductID        int
	Quantity         int
	Cycle            string
	Amount           float64
	CustomerID       string
	ConfigOptions    map[string]string `json:"configOptions"`
	CustomFields     map[string]string `json:"customFields"`
	Hostname         string            `json:"hostname"`
	Password         string            `json:"password"`
	DataCenterID     int               `json:"dataCenterId"`
	ImageID          int               `json:"imageId"`
	NetworkType      string            `json:"networkType"`
	DownstreamURL    string            `json:"downstreamUrl"`
	DownstreamToken  string            `json:"downstreamToken"`
	DownstreamHostID string            `json:"downstreamHostId"`
}
type UpstreamOrderResult struct {
	OrderID, Status, HostID string
	Cost                    float64
	BalanceBefore           float64
	BalanceAfter            float64
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
	Reinstall     bool `json:"reinstall"`
	ResetPassword bool `json:"resetPassword"`
	Traffic       bool `json:"traffic"`
	Remote        bool `json:"remote"`
	Destroy       bool `json:"destroy"`
}

type InstanceIP struct {
	Address    string `json:"address"`
	SubnetMask string `json:"subnetMask,omitempty"`
	Gateway    string `json:"gateway,omitempty"`
}

type InstanceDetail struct {
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

type InstanceActionInput struct {
	Action         string `json:"action"`
	Password       string `json:"password,omitempty"`
	ImageID        string `json:"imageId,omitempty"`
	Port           string `json:"port,omitempty"`
	FormatDataDisk bool   `json:"formatDataDisk,omitempty"`
}

type InstanceActionResult struct {
	Status    string `json:"status,omitempty"`
	Message   string `json:"message"`
	RemoteURL string `json:"remoteUrl,omitempty"`
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
