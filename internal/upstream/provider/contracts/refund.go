package contracts

type SupplierRefund struct {
	ID        string `json:"id"`
	HostID    string `json:"hostId"`
	Amount    string `json:"amount"`
	Status    string `json:"status"`
	Reason    string `json:"reason"`
	UpdatedAt string `json:"updatedAt"`
}
type RefundProvider interface {
	RefundEligible(hostID int64) (bool, error)
	SubmitRefund(hostID int64, reason string) error
	RefundRecords(hostID int64) ([]SupplierRefund, error)
}
