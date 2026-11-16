package west

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	"ddyidc/internal/upstream/provider/contracts"
)

// West has no payable renewal invoice: calling `renew` charges the agent
// balance immediately and extends the machine. The platform charges the
// customer first and only then asks the provider to pay, so the "invoice" here
// is a synthetic identity that remembers which machine and cycle to renew.
// PrepareRenewalInvoice is deliberately not implemented: an upstream charge
// must never happen before the customer has paid.
const renewalInvoicePrefix = "west:"

func renewalInvoiceID(hostID int64, cycle string, duration int64) string {
	return fmt.Sprintf("%s%d:%s:%d", renewalInvoicePrefix, hostID, cycle, duration)
}

func parseRenewalInvoiceID(id string) (hostID int64, cycle string, duration int64, err error) {
	value, ok := strings.CutPrefix(strings.TrimSpace(id), renewalInvoicePrefix)
	if !ok {
		return 0, "", 0, errors.New("续费账单标识无效")
	}
	parts := strings.Split(value, ":")
	if len(parts) != 3 {
		return 0, "", 0, errors.New("续费账单标识无效")
	}
	hostID, hostErr := strconv.ParseInt(parts[0], 10, 64)
	duration, durationErr := strconv.ParseInt(parts[2], 10, 64)
	if hostErr != nil || durationErr != nil || hostID <= 0 || duration <= 0 {
		return 0, "", 0, errors.New("续费账单标识无效")
	}
	if _, _, ok := cycleOf(parts[1]); !ok {
		return 0, "", 0, errors.New("续费账单周期无效")
	}
	return hostID, parts[1], duration, nil
}

// RenewalQuotes reads the four supported cycles straight from the vendor. The
// customer price is the vendor list price; the agent cost stays internal.
func (p *Provider) RenewalQuotes(hostID int64) ([]contracts.RenewalQuote, error) {
	if hostID <= 0 {
		return nil, errors.New("上游实例编号无效")
	}
	ctx, cancel := p.withTimeout()
	defer cancel()
	ip, err := p.ipOf(ctx, hostID)
	if err != nil {
		return nil, err
	}
	quotes := make([]contracts.RenewalQuote, 0, len(billingCycles))
	for _, cycle := range billingCycles {
		data, err := p.renewPrice(ctx, ip, 1, cycle.Paymath)
		if err != nil {
			return nil, err
		}
		cost, listPrice := dec(data.Price), dec(data.FullPrice)
		// One period per quote and never offer a cycle whose list price would
		// undercut our own cost.
		if cost <= 0 || listPrice <= 0 || listPrice < cost {
			continue
		}
		quotes = append(quotes, contracts.RenewalQuote{
			Cycle:          cycle.Key,
			Label:          cycle.Label,
			Amount:         strconv.FormatFloat(cost, 'f', 2, 64),
			OriginalAmount: strconv.FormatFloat(listPrice, 'f', 2, 64),
			Duration:       int64(cycle.Months),
		})
	}
	if len(quotes) == 0 {
		return nil, errors.New("西部数码未返回可用续费报价")
	}
	return quotes, nil
}

// CreateRenewalInvoice prices the cycle again and returns the synthetic identity.
// It never charges: the money only moves in PayRenewalInvoice, after the
// platform has collected the customer's payment.
func (p *Provider) CreateRenewalInvoice(hostID int64, cycle string, duration int64) (string, string, error) {
	_, payMethod, ok := cycleOf(cycle)
	if !ok {
		return "", "", errors.New("西部数码暂不支持该计费周期")
	}
	if duration <= 0 {
		return "", "", errors.New("续费周期数无效")
	}
	ctx, cancel := p.withTimeout()
	defer cancel()
	ip, err := p.ipOf(ctx, hostID)
	if err != nil {
		return "", "", err
	}
	data, err := p.renewPrice(ctx, ip, 1, payMethod)
	if err != nil {
		return "", "", err
	}
	cost := dec(data.Price)
	if cost <= 0 {
		return "", "", errors.New("西部数码未返回有效续费成本")
	}
	return renewalInvoiceID(hostID, cycle, duration), strconv.FormatFloat(cost, 'f', 2, 64), nil
}

// PayRenewalInvoice performs the single charged renewal call. The platform
// claims the renewal row before calling, so this runs at most once per renewal.
func (p *Provider) PayRenewalInvoice(invoiceID string) error {
	hostID, cycle, _, err := parseRenewalInvoiceID(invoiceID)
	if err != nil {
		return err
	}
	_, payMethod, ok := cycleOf(cycle)
	if !ok {
		return errors.New("西部数码暂不支持该计费周期")
	}
	ctx, cancel := p.withTimeout()
	defer cancel()
	ip, err := p.ipOf(ctx, hostID)
	if err != nil {
		return err
	}
	return p.renew(ctx, ip, payMethod)
}
