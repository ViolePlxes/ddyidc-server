package readmodel

import (
	"encoding/json"
	"regexp"
	"strconv"
	"time"
)

const SnapshotKey = "supplierReadSnapshot"

// This is a display snapshot, never a payment/renewal authorization.
type SupplierSnapshot struct {
	CustomerID       int64  `json:"customerId"`
	OwnershipVersion int64  `json:"ownershipVersion"`
	SupplierID       int64  `json:"supplierId"`
	RemoteID         string `json:"remoteId"`
	BillingCycle     string `json:"billingCycle"`
	Amount           string `json:"amount,omitempty"`
	PriceSource      string `json:"priceSource,omitempty"`
	PriceState       string `json:"priceState"`
	VerifiedAt       int64  `json:"verifiedAt,omitempty"`
	ObservedAt       int64  `json:"observedAt"`
	ObservedStatus   string `json:"observedStatus,omitempty"`
	ObservedPower    string `json:"observedPower,omitempty"`
}

func Decode(raw string) (SupplierSnapshot, bool) {
	var outer map[string]json.RawMessage
	var v SupplierSnapshot
	if json.Unmarshal([]byte(raw), &outer) != nil || json.Unmarshal(outer[SnapshotKey], &v) != nil {
		return v, false
	}
	return v, true
}
func ValidPrice(v SupplierSnapshot, customer, owner, supplier int64, remote, cycle string) bool {
	if customer <= 0 || supplier <= 0 || remote == "" || cycle == "" {
		return false
	}
	if v.CustomerID != customer || v.OwnershipVersion != owner || v.SupplierID != supplier || v.RemoteID != remote || v.BillingCycle != cycle || v.PriceSource != "upstream_original" || v.VerifiedAt <= 0 {
		return false
	}
	if v.PriceState != "verified" && v.PriceState != "last_known" {
		return false
	}
	if !regexp.MustCompile(`^\d+(?:\.\d{1,2})?$`).MatchString(v.Amount) {
		return false
	}
	n, e := strconv.ParseFloat(v.Amount, 64)
	return e == nil && n > 0 && n < 1e12
}
func (v SupplierSnapshot) Stale(now time.Time) bool {
	return v.PriceState != "verified" || now.Unix()-v.VerifiedAt > 4*3600
}
