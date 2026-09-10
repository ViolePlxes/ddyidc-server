package provider

import (
	"ddyidc/internal/upstream/provider/contracts"
	"ddyidc/internal/upstream/provider/custom"
	"ddyidc/internal/upstream/provider/finance"
	"ddyidc/internal/upstream/provider/whmcs"
	"ddyidc/internal/upstream/provider/zjmf"
	"fmt"
	"sync"
)

type Constructor func(contracts.Supplier) contracts.Provider
type ProviderFactory struct {
	mu        sync.RWMutex
	providers map[string]Constructor
}

func NewProviderFactory() *ProviderFactory {
	f := &ProviderFactory{providers: map[string]Constructor{}}
	f.Register("default", func(s contracts.Supplier) contracts.Provider { return zjmf.New(s) })
	f.Register("finance", finance.New)
	f.Register("whmcs", whmcs.New)
	f.Register("custom", custom.New)
	return f
}
func (f *ProviderFactory) Register(kind string, constructor Constructor) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.providers[kind] = constructor
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
