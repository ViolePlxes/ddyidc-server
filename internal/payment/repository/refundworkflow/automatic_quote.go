package refundworkflow

import (
	"context"
	"encoding/json"
	"gorm.io/gorm"
)

// MachineQuote uses the immutable application time and original order fees.
// Existing manual fee previews for other modules retain their separate policy.
func MachineQuote(ctx context.Context, db *gorm.DB, id int64) (Detail, Calculation, Fees, error) {
	d, e := Load(ctx, db, id)
	if e != nil {
		return d, Calculation{}, Fees{}, e
	}
	if d.Request != nil && d.Request.Source == "instance" {
		return InstanceQuote(ctx, db, id)
	}
	if d.Status != 0 || d.Request == nil || d.Request.Source != "order" {
		return d, Calculation{}, Fees{}, ErrConflict
	}
	var snapshot Snapshot
	if json.Unmarshal(d.Request.Snapshot, &snapshot) != nil {
		return d, Calculation{}, Fees{}, ErrCalculation
	}
	snapshot, e = ResolvePurchasedPeriod(db.WithContext(ctx), *d.Request, snapshot)
	if e != nil {
		return d, Calculation{}, Fees{}, e
	}
	for _, p := range snapshot.Periods {
		if p.InstanceID <= 0 || p.RenewalID > 0 || p.Unstarted {
			return d, Calculation{}, Fees{}, ErrCalculation
		}
	}
	fees := Fees{Setup: snapshot.MinimumSetup.String(), Installation: "0.00", Handling: snapshot.MinimumHandling.String(), Gift: "0.00", Traffic: "0.00", ValueAdded: "0.00", Evidence: "订单原始计费快照；一次性开通费不退，未使用服务费按申请时间以天折算"}
	calc, e := Calculate(snapshot, d.CreatedAt, fees)
	return d, calc, fees, e
}
