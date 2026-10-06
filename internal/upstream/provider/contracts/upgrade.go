package contracts

type UpgradePlan struct {
	ProductID string         `json:"productId"`
	Name      string         `json:"name"`
	Cycles    []UpgradeCycle `json:"cycles"`
}
type UpgradeCycle struct {
	ID    string `json:"id"`
	Label string `json:"label"`
}
type UpgradeQuote struct{ HostID, ProductID, Cycle, Cost, OriginalAmount string }
type UpgradeProvider interface {
	UpgradePlans(int64) ([]UpgradePlan, error)
	QuoteUpgrade(int64, string, string) (UpgradeQuote, error)
	CreateUpgradeInvoice(int64, string, string, string) (string, string, error)
	PayUpgradeInvoice(string) error
}
