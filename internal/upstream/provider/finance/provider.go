// Package finance owns the adapter selected for suppliers of type=finance.
// The legacy finance protocol shares transport primitives with zjmf, while the
// concrete wrapper preserves a distinct, pluggable factory registration.
package finance

import (
	"ddyidc/internal/upstream/provider/contracts"
	"ddyidc/internal/upstream/provider/zjmf"
)

type Provider struct{ *zjmf.Provider }

func New(s contracts.Supplier) contracts.Provider {
	return &Provider{Provider: zjmf.New(s)}
}
