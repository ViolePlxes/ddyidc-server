package custom
import ("ddyidc/internal/upstream/provider/contracts"; "ddyidc/internal/upstream/provider/zjmf")
type Provider struct { contracts.Provider }; func New(s contracts.Supplier) contracts.Provider { return &Provider{Provider:zjmf.New(s)} }
