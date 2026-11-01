package zjmf

import (
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"ddyidc/internal/upstream/provider/contracts"
)

// The finance client API exposes no invoice lookup: /host/header only accepts
// host_id and /console/v1/order resolves downstream-proxy hosts, not ordinary
// client hosts. A host row does carry invoice_id, so a paid invoice whose
// payment response was lost can still be reconciled read-only by scanning the
// newest host rows of the same product and confirming invoice_id on each.
const (
	invoiceHostScanPages = 3
	invoiceHostScanLimit = 100
	invoiceHostHeaderMax = 12
)

type upstreamHostRow struct {
	ID      int64
	OrderID string
	PID     int
	RegDate int64
	NextDue int64
	Domain  string
	IP      string
	Status  string
}

// FindHostByInvoice returns the upstream host created for a paid invoice, or
// nil when the invoice has no host yet. It only performs GET requests and never
// mutates upstream or local state.
func (p *Provider) FindHostByInvoice(invoiceID int64, productID int, since time.Time) (*contracts.InvoiceHostMatch, error) {
	if invoiceID <= 0 {
		return nil, errors.New("upstream invoice ID must be positive")
	}
	cutoff := int64(0)
	if !since.IsZero() {
		cutoff = since.Add(-30 * time.Minute).Unix()
	}
	candidates := make([]upstreamHostRow, 0, 32)
	for page := 1; page <= invoiceHostScanPages; page++ {
		rows, err := p.listHostRows(page)
		if err != nil {
			return nil, err
		}
		if len(rows) == 0 {
			break
		}
		oldest := int64(0)
		for _, row := range rows {
			if row.RegDate > 0 && (oldest == 0 || row.RegDate < oldest) {
				oldest = row.RegDate
			}
			if productID > 0 && row.PID != productID {
				continue
			}
			if cutoff > 0 && row.RegDate > 0 && row.RegDate < cutoff {
				continue
			}
			candidates = append(candidates, row)
		}
		if cutoff > 0 && oldest > 0 && oldest < cutoff {
			break
		}
	}
	if len(candidates) == 0 {
		return nil, nil
	}
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].ID > candidates[j].ID })
	if len(candidates) > invoiceHostHeaderMax {
		candidates = candidates[:invoiceHostHeaderMax]
	}
	for _, row := range candidates {
		hostInvoice, err := p.hostInvoiceID(row.ID)
		if err != nil || hostInvoice != invoiceID {
			continue
		}
		match := &contracts.InvoiceHostMatch{
			HostID:    strconv.FormatInt(row.ID, 10),
			InvoiceID: strconv.FormatInt(invoiceID, 10),
			OrderID:   row.OrderID,
			ProductID: strconv.Itoa(row.PID),
			Status:    row.Status,
			PrimaryIP: row.IP,
			Domain:    row.Domain,
		}
		if row.RegDate > 0 {
			match.CreatedAt = time.Unix(row.RegDate, 0).Format(time.RFC3339)
		}
		if row.NextDue > 0 {
			match.ExpiresAt = time.Unix(row.NextDue, 0).Format(time.RFC3339)
		}
		return match, nil
	}
	return nil, nil
}

func (p *Provider) listHostRows(page int) ([]upstreamHostRow, error) {
	query := url.Values{
		"page":  {strconv.Itoa(page)},
		"limit": {strconv.Itoa(invoiceHostScanLimit)},
	}
	body, err := p.authorizedGet("/host/list?" + query.Encode())
	if err != nil {
		return nil, fmt.Errorf("read upstream host list: %w", err)
	}
	data, err := businessData(body, "host list")
	if err != nil {
		return nil, err
	}
	rows, _ := data["list"].([]any)
	result := make([]upstreamHostRow, 0, len(rows))
	for _, item := range rows {
		row, ok := item.(map[string]any)
		if !ok {
			continue
		}
		parsed := upstreamHostRow{
			OrderID: firstMapString(row, "orderid", "order_id"),
			Domain:  firstMapString(row, "domain", "hostname"),
			IP:      firstMapString(row, "dedicatedip", "mainip", "ip"),
			Status:  firstMapString(row, "domainstatus", "status"),
		}
		if value, e := strconv.ParseInt(firstMapString(row, "id", "host_id"), 10, 64); e == nil {
			parsed.ID = value
		}
		if value, e := strconv.Atoi(firstMapString(row, "pid", "productid")); e == nil {
			parsed.PID = value
		}
		if value, e := strconv.ParseInt(firstMapString(row, "regdate", "create_time"), 10, 64); e == nil {
			parsed.RegDate = value
		}
		if value, e := strconv.ParseInt(firstMapString(row, "nextduedate", "next_due_date"), 10, 64); e == nil {
			parsed.NextDue = value
		}
		if parsed.ID > 0 {
			result = append(result, parsed)
		}
	}
	return result, nil
}

func (p *Provider) hostInvoiceID(hostID int64) (int64, error) {
	query := url.Values{"host_id": {strconv.FormatInt(hostID, 10)}}
	body, err := p.authorizedGet("/host/header?" + query.Encode())
	if err != nil {
		return 0, err
	}
	data, err := businessData(body, "host header")
	if err != nil {
		return 0, err
	}
	hostData := nestedMap(data, "host_data")
	if len(hostData) == 0 {
		hostData = data
	}
	raw := strings.TrimSpace(firstMapString(hostData, "invoice_id", "invoiceid"))
	if raw == "" {
		return 0, nil
	}
	value, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return 0, nil
	}
	return value, nil
}
