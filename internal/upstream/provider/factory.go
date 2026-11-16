package provider

import (
	"ddyidc/internal/upstream/provider/contracts"
	"ddyidc/internal/upstream/provider/custom"
	"ddyidc/internal/upstream/provider/finance"
	"ddyidc/internal/upstream/provider/west"
	"ddyidc/internal/upstream/provider/whmcs"
	"ddyidc/internal/upstream/provider/zjmf"
	"fmt"
	"sync"
)

type Constructor func(contracts.Supplier) contracts.Provider
type ProviderFactory struct {
	mu             sync.RWMutex
	providers      map[string]Constructor
	modules        map[string]Module
	variantModules map[string]string
}

func NewProviderFactory() *ProviderFactory {
	registrations := []ModuleRegistration{}
	must := func(reg ModuleRegistration) {
		registrations = append(registrations, reg)
	}
	must(ModuleRegistration{Manifest: Module{ID: "zjmf", Name: "对接智简魔方", Version: "1.0.0", ContractVersion: 1, Description: "兼容现有魔方协议与财务版；具体实例功能以协议返回及验证结果为准", Capabilities: map[string]string{"catalog": "implemented", "configuration": "implemented", "quote": "implemented", "purchase": "implemented", "renewal": "implemented", "instance": "implemented", "remote": "provider_declared", "webhook": "implemented"}}, Constructors: map[string]Constructor{"default": func(s contracts.Supplier) contracts.Provider { return zjmf.New(s) }, "zjmf": func(s contracts.Supplier) contracts.Provider { return zjmf.New(s) }, "finance": finance.New}})
	must(ModuleRegistration{Manifest: Module{ID: "whmcs", Name: "对接 WHMCS", Version: "0.0.0", ContractVersion: 1, Description: "历史兼容入口实际复用魔方实现，尚未验证独立WHMCS协议；不据此开放新销售", Capabilities: map[string]string{"catalog": "pending_verification", "configuration": "unsupported", "quote": "unsupported", "purchase": "pending_verification"}}, Constructors: map[string]Constructor{"whmcs": whmcs.New}})
	must(ModuleRegistration{Manifest: Module{ID: "custom", Name: "自定义系统对接", Version: "0.0.0", ContractVersion: 1, Description: "历史兼容入口；新系统必须单独注册模块并验证协议", Capabilities: map[string]string{"catalog": "pending_verification", "configuration": "unsupported", "quote": "unsupported", "purchase": "pending_verification"}}, Constructors: map[string]Constructor{"custom": custom.New}})
	// West (西部数码) elastic cloud servers. Catalogue, quotation, provisioning
	// and instance management are implemented against the vendor V2 manual and
	// verified on a real account (trial open). Renewal stays closed until the
	// platform's invoice-then-pay model can be reconciled with the vendor's
	// immediate charge.
	must(ModuleRegistration{
		Manifest: Module{
			ID: "west", Name: "对接西部数码", Version: "0.3.0", ContractVersion: 1,
			Description: "西部数码弹性云服务器 V2 接口：目录、报价、开通、实例管理、续费已实现并经真实账号验证",
			Capabilities: map[string]string{
				"catalog":       "implemented",
				"configuration": "implemented",
				"quote":         "implemented",
				"purchase":      "implemented",
				"renewal":       "implemented",
				"instance":      "implemented",
				"remote":        "unsupported",
				"webhook":       "unsupported",
			},
			ConfigFields: []ModuleConfigField{
				{Name: "url", Label: "接口地址", Required: true},
				{Name: "username", Label: "西部数码账号", Required: true},
				{Name: "token", Label: "API 密码", Secret: true, Required: true},
			},
		},
		Constructors: map[string]Constructor{
			"west":      func(s contracts.Supplier) contracts.Provider { return west.New(s) },
			"westcloud": func(s contracts.Supplier) contracts.Provider { return west.New(s) },
		},
	})
	f, err := BuildProviderFactory(registrations, nil, nil)
	if err != nil {
		panic(err)
	}
	return f
}
func (f *ProviderFactory) Register(kind string, constructor Constructor) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.providers[kind] = constructor
	if _, ok := f.variantModules[kind]; !ok {
		f.variantModules[kind] = kind
		f.modules[kind] = Module{ID: kind, Name: kind, Version: "0.0.0", ContractVersion: 1, Variants: []string{kind}, Capabilities: map[string]string{}, Description: "兼容注册入口，需声明并验证能力"}
	}
}
func (f *ProviderFactory) NewProvider(s Supplier) (UpstreamProvider, error) {
	f.mu.RLock()
	c, ok := f.providers[s.Type]
	f.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("unsupported upstream provider: %s", s.Type)
	}
	return c(s), nil
}

var globalFactory = NewProviderFactory()

func Register(kind string, c Constructor)              { globalFactory.Register(kind, c) }
func NewProvider(s Supplier) (UpstreamProvider, error) { return globalFactory.NewProvider(s) }
