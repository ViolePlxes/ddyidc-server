package west

import (
	"context"
	"errors"
	"strconv"
	"time"

	"ddyidc/internal/upstream/provider/contracts"
	"ddyidc/pkg/westapi"
)

// procurementBudget bounds the whole open (price, balance, create). The create
// call itself is a charged single attempt; a timeout is an unknown outcome, not
// a failure to retry.
const procurementBudget = 90 * time.Second

// CreateOrderContext is the platform entry point: the caller's cancellation is
// kept but the procurement gets its own budget.
func (p *Provider) CreateOrderContext(ctx context.Context, order contracts.OrderInfo) (*contracts.UpstreamOrderResult, error) {
	scoped := *p
	scoped.instanceContext = ctx
	return scoped.createOrder(order)
}

// CreateOrder is the compatibility entry point with the same single-write
// policy; it never mutates the shared provider used by readers.
func (p *Provider) CreateOrder(order contracts.OrderInfo) (*contracts.UpstreamOrderResult, error) {
	ctx, cancel := context.WithTimeout(context.Background(), procurementBudget)
	defer cancel()
	scoped := *p
	scoped.instanceContext = ctx
	return scoped.createOrder(order)
}

// createOrder opens exactly one machine.
//
// Order of operations matters for money:
//  1. price the exact configuration and compare it with the customer's signed
//     procurement ceiling;
//  2. refuse to start when the agent balance cannot cover the cost;
//  3. persist the "payment" checkpoint so a lost answer is recorded as a
//     procurement attempt rather than silently retried;
//  4. send create once;
//  5. record the machine identity.
//
// A transport failure after step 4 is reported as an unknown outcome; the
// platform must reconcile with the vendor console and never retry blindly.
func (p *Provider) createOrder(order contracts.OrderInfo) (result *contracts.UpstreamOrderResult, failure error) {
	stage := "validation"
	defer func() {
		if failure != nil {
			failure = contracts.ClassifyProcurementFailure(stage, failure)
		}
	}()
	checkpoint := func(next, invoice, host string) error {
		stage = next
		if order.Checkpoint == nil {
			return nil
		}
		if err := order.Checkpoint(next, invoice, host); err != nil {
			return &contracts.ProcurementFailure{Stage: next, Code: "checkpoint_failed", Cause: err}
		}
		return nil
	}
	if order.ProductID <= 0 || order.Quantity <= 0 || order.Cycle == "" {
		return nil, errors.New("商品、数量与计费周期为必填项")
	}
	if order.Quantity != 1 {
		return nil, errors.New("西部数码按台开通，暂不支持合并开通")
	}
	_, payMethod, ok := cycleOf(order.Cycle)
	if !ok {
		return nil, errors.New("西部数码暂不支持该计费周期")
	}

	ctx := p.baseContext()
	if _, bounded := ctx.Deadline(); !bounded {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, procurementBudget)
		defer cancel()
	}

	if err := checkpoint("quote", "", ""); err != nil {
		return nil, err
	}
	cfg, err := p.config(ctx)
	if err != nil {
		return nil, err
	}
	room, ok := roomOf(order.ProductID, cfg)
	if !ok {
		return nil, contracts.ErrProductUnavailable
	}
	query, osValue, err := p.queryFromOptions(cfg, room, order.ConfigOptions)
	if err != nil {
		return nil, err
	}
	if osValue == "" {
		return nil, errors.New("请选择操作系统")
	}
	query.PayMethod = payMethod

	quoted, _, err := p.createPrice(ctx, query)
	if err != nil {
		return nil, err
	}
	if quoted <= 0 {
		return nil, &contracts.ProcurementFailure{Stage: "quote", Code: "quote_invalid"}
	}
	if ceiling := order.ProcurementCeiling; ceiling != "" {
		limit, err := strconv.ParseFloat(ceiling, 64)
		if err != nil || limit <= 0 || quoted > limit {
			return nil, &contracts.ProcurementFailure{Stage: "quote", Code: "budget_exceeded"}
		}
	}
	before, _, err := p.balance(ctx)
	if err != nil {
		return nil, err
	}
	if before < quoted {
		// Nothing was sent, but the platform records the attempt; an operator
		// tops up the agent account and resolves the unit manually.
		return nil, &contracts.ProcurementFailure{Stage: "quote", Code: "supplier_rejected"}
	}

	if err := checkpoint("payment", "", ""); err != nil {
		return nil, err
	}
	created, err := p.createServerRaw(ctx, query, osValue, 9999999)
	if err != nil {
		return nil, classifyChargedWrite(err)
	}
	hostID := strconv.FormatInt(int64(dec(created.ID)), 10)
	if err := checkpoint("paid", hostID, hostID); err != nil {
		return nil, err
	}

	cost := quoted
	after, _, err := p.balance(ctx)
	if err == nil && before > after {
		cost = float64(int64((before-after)*100+0.5)) / 100
	}
	return &contracts.UpstreamOrderResult{
		// West issues no separate order number for elastic cloud servers; the
		// machine identity is the only durable reference.
		OrderID:       hostID,
		Status:        "active",
		HostID:        hostID,
		Cost:          cost,
		BalanceBefore: before,
		BalanceAfter:  after,
	}, nil
}

// classifyChargedWrite separates a definite vendor refusal from an uncertain
// outcome. Anything uncertain must reach the platform as an unknown result so
// the unit is reconciled by a human instead of being replayed.
func classifyChargedWrite(err error) error {
	var known *contracts.ProcurementFailure
	var rejection *westapi.Failure
	var status *westapi.StatusError
	switch {
	case errors.As(err, &known):
		return known
	case errors.As(err, &rejection):
		code, parseErr := strconv.Atoi(rejection.Code)
		if parseErr != nil {
			code = 0
		}
		return &contracts.ProcurementFailure{Stage: "payment", Code: "supplier_rejected", SupplierStatus: code, RequestWritten: true}
	case errors.As(err, &status):
		return &contracts.ProcurementFailure{Stage: "payment", Code: "http_rejected", HTTPStatus: status.Status, RequestWritten: true, FirstByte: true}
	case errors.Is(err, westapi.ErrConnection):
		return &contracts.ProcurementFailure{Stage: "payment", Code: "timeout", RequestWritten: true, TransportPhase: "response_headers"}
	default:
		return err
	}
}
