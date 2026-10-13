package refundworkflow

import (
	balance "ddyidc/internal/billing/balance"
	"encoding/json"
	"errors"
	"time"
)

var ErrInput = errors.New("退款参数不完整或无效")
var ErrConflict = errors.New("退款对象、计费依据或业务状态已变化，请刷新后核查")
var ErrEntitlement = errors.New("请先核查并终止退款服务；运行中、开通中或结果未知的服务不能完成退款")
var ErrCalculation = errors.New("计费周期或费用明细不完整，请先核查原始账单，不能估算退款")
var ErrPolicy = errors.New("退款只允许后台审核后退入账户余额，不允许自动退款或登记外部退款完成")

type Period struct {
	BillingDays      int64         `json:"billingDays,omitempty"`
	BillingCycle     string        `json:"billingCycle,omitempty"`
	InstanceID       int64         `json:"instanceId,string"`
	OwnershipVersion int64         `json:"ownershipVersion"`
	RenewalID        int64         `json:"renewalId,string,omitempty"`
	EntitlementStart *time.Time    `json:"entitlementStartAt,omitempty"`
	EntitlementEnd   *time.Time    `json:"entitlementEndAt,omitempty"`
	Paid             balance.Money `json:"paidAmount"`
	Start            *time.Time    `json:"startAt,omitempty"`
	End              *time.Time    `json:"endAt,omitempty"`
	Unstarted        bool          `json:"unstarted"`
}
type Snapshot struct {
	Paid            balance.Money `json:"paidAmount"`
	BalancePaid     balance.Money `json:"balancePaid"`
	MinimumSetup    balance.Money `json:"minimumSetupAmount"`
	MinimumHandling balance.Money `json:"minimumHandlingAmount"`
	Periods         []Period      `json:"periods"`
	Warnings        []string      `json:"warnings"`
}
type Request struct {
	ApplicationID int64           `gorm:"primaryKey" json:"applicationId,string"`
	Source        string          `json:"source"`
	ObjectID      int64           `json:"objectId,string"`
	CustomerID    int64           `json:"customerId,string"`
	RequestID     string          `json:"-"`
	Snapshot      json.RawMessage `gorm:"type:json" json:"snapshot"`
	CreatedAt     time.Time       `json:"createdAt"`
}

func (Request) TableName() string { return "payment.todo_refund_requests" }

// Every field is required: the reviewing administrator confirms zero explicitly
// when the original bill has no such charge. Gift credit is never refundable.
type Fees struct {
	Setup        string `json:"setup"`
	Installation string `json:"installation"`
	Handling     string `json:"handling"`
	Gift         string `json:"gift"`
	Traffic      string `json:"traffic"`
	ValueAdded   string `json:"valueAdded"`
	Evidence     string `json:"evidence"`
}
type Calculation struct {
	Policy    string        `json:"policy"`
	AppliedAt time.Time     `json:"appliedAt"`
	Paid      balance.Money `json:"paidAmount"`
	Fees      Fees          `json:"fees"`
	Periods   []PeriodQuote `json:"periods"`
	Unused    balance.Money `json:"unusedServiceAmount"`
	Refund    balance.Money `json:"refundAmount"`
}
type PeriodQuote struct {
	Period
	TotalDays               int64         `json:"totalDays"`
	UsedDays                int64         `json:"usedDays"`
	RemainingDays           int64         `json:"remainingDays"`
	PartialDayDeductionDays int64         `json:"partialDayDeductionDays"`
	Service                 balance.Money `json:"serviceAmount"`
	Refund                  balance.Money `json:"unusedServiceAmount"`
}
