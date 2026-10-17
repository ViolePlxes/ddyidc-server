package provider

import (
	"fmt"
	"sort"
)

// Module is installed code, not a supplier account or an assertion of live verification.
type ModuleConfigField struct {
	Name     string `json:"name"`
	Label    string `json:"label"`
	Secret   bool   `json:"secret"`
	Required bool   `json:"required"`
}
type Module struct {
	ConfigFields    []ModuleConfigField `json:"configFields"`
	ID              string              `json:"id"`
	Name            string              `json:"name"`
	Version         string              `json:"version"`
	ContractVersion int                 `json:"contractVersion"`
	Variants        []string            `json:"variants"`
	Capabilities    map[string]string   `json:"capabilities"`
	Description     string              `json:"description"`
}

type ModuleRegistration struct {
	Manifest     Module
	Constructors map[string]Constructor
}

func (f *ProviderFactory) RegisterModule(reg ModuleRegistration) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if reg.Manifest.ID == "" || reg.Manifest.Version == "" || reg.Manifest.ContractVersion != 1 || len(reg.Constructors) == 0 {
		return fmt.Errorf("invalid adapter module registration")
	}
	for _, field := range reg.Manifest.ConfigFields {
		if field.Name != "url" && field.Name != "username" && field.Name != "token" && field.Name != "secret" {
			return fmt.Errorf("adapter contract v1 does not support credential field %q", field.Name)
		}
	}
	if _, exists := f.modules[reg.Manifest.ID]; exists {
		return fmt.Errorf("adapter module already registered")
	}
	for variant, c := range reg.Constructors {
		if variant == "" || len(variant) > 20 || c == nil {
			return fmt.Errorf("invalid module variant")
		}
		if _, exists := f.providers[variant]; exists {
			return fmt.Errorf("adapter variant already registered")
		}
	}
	manifest := cloneModule(reg.Manifest)
	manifest.Variants = nil
	if len(manifest.ConfigFields) == 0 {
		manifest.ConfigFields = []ModuleConfigField{{Name: "url", Label: "接口地址", Required: true}, {Name: "username", Label: "接口账号", Required: true}, {Name: "token", Label: "接口 Token", Secret: true, Required: true}, {Name: "secret", Label: "接口 Secret", Secret: true}}
	}
	for variant, c := range reg.Constructors {
		f.providers[variant] = c
		f.variantModules[variant] = manifest.ID
		manifest.Variants = append(manifest.Variants, variant)
	}
	sort.Strings(manifest.Variants)
	f.modules[manifest.ID] = manifest
	return nil
}
func cloneModule(m Module) Module {
	m.ConfigFields = append([]ModuleConfigField(nil), m.ConfigFields...)
	m.Variants = append([]string(nil), m.Variants...)
	capabilities := map[string]string{}
	for k, v := range m.Capabilities {
		capabilities[k] = v
	}
	m.Capabilities = capabilities
	return m
}
func (f *ProviderFactory) Modules() []Module {
	f.mu.RLock()
	defer f.mu.RUnlock()
	out := []Module{}
	for _, m := range f.modules {
		out = append(out, cloneModule(m))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}
func (f *ProviderFactory) ModuleForVariant(variant string) (Module, bool) {
	f.mu.RLock()
	defer f.mu.RUnlock()
	m, ok := f.modules[f.variantModules[variant]]
	return cloneModule(m), ok
}
func Modules() []Module                              { return globalFactory.Modules() }
func ModuleForVariant(variant string) (Module, bool) { return globalFactory.ModuleForVariant(variant) }
func RegisterModule(reg ModuleRegistration) error    { return globalFactory.RegisterModule(reg) }

// BuildProviderFactory assembles a static release before publication. Failure never
// mutates the running registry. Dependencies are obtained by the release preflight;
// the function does not delete accounts, mappings, orders, or audit history.
func BuildProviderFactory(registrations []ModuleRegistration, previous *ProviderFactory, dependencies map[string]bool) (*ProviderFactory, error) {
	next := &ProviderFactory{providers: map[string]Constructor{}, modules: map[string]Module{}, variantModules: map[string]string{}}
	for _, reg := range registrations {
		if err := next.RegisterModule(reg); err != nil {
			return nil, err
		}
	}
	if previous != nil {
		for _, old := range previous.Modules() {
			if !dependencies[old.ID] {
				continue
			}
			candidate, ok := next.modules[old.ID]
			if !ok {
				return nil, fmt.Errorf("adapter %s still has retained dependencies", old.ID)
			}
			if candidate.ContractVersion != old.ContractVersion {
				return nil, fmt.Errorf("adapter %s requires an explicit contract migration", old.ID)
			}
			for _, variant := range old.Variants {
				if next.variantModules[variant] != old.ID {
					return nil, fmt.Errorf("adapter %s cannot discard retained variant %s", old.ID, variant)
				}
			}
		}
	}
	return next, nil
}
