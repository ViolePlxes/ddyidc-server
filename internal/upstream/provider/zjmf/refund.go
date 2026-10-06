package zjmf

import (
	billing "ddyidc/internal/billing/balance"
	"ddyidc/internal/upstream/provider/contracts"
	"fmt"
	"net/url"
	"strconv"
	"strings"
)

const refundPluginPath = "/addons?_plugin=161&_controller=refund&_action=index"

// The financial refund plugin is separate from /host/cancel, which only cancels service.
func (p *Provider) refundRows(action string, page int) ([]any, int, error) {
	b, e := p.authorizedFormPost(refundPluginPath, url.Values{"action": {action}, "page": {strconv.Itoa(page)}, "pageSize": {"100"}})
	if e != nil {
		return nil, 0, e
	}
	d, e := businessData(b, "supplier refund records")
	if e != nil {
		return nil, 0, e
	}
	rows, ok := d["data"].([]any)
	if !ok {
		return nil, 0, fmt.Errorf("退款模块响应格式无效")
	}
	total, e := strconv.Atoi(firstMapString(d, "total"))
	if e != nil || total < 0 || total > 2000 {
		return nil, 0, fmt.Errorf("退款列表不能完整核对")
	}
	return rows, total, nil
}
func (p *Provider) RefundEligible(id int64) (bool, error) {
	if id <= 0 {
		return false, fmt.Errorf("invalid refund host")
	}
	for page := 1; page <= 20; page++ {
		rows, total, e := p.refundRows("list", page)
		if e != nil {
			return false, e
		}
		for _, raw := range rows {
			r, ok := raw.(map[string]any)
			if !ok {
				return false, fmt.Errorf("退款列表格式无效")
			}
			if firstMapString(r, "id") == strconv.FormatInt(id, 10) {
				return true, nil
			}
		}
		if page*100 >= total {
			return false, nil
		}
	}
	return false, fmt.Errorf("退款资格不能完整核对")
}
func (p *Provider) SubmitRefund(id int64, reason string) error {
	if id <= 0 || reason == "" {
		return fmt.Errorf("invalid refund request")
	}
	q := *p
	q.procurementContext = true
	// Plugin IDs are installation-specific. Prove that this endpoint hosts the
	// verified financial refund plugin before sending any mutating action.
	page, e := q.authorizedGet(refundPluginPath)
	if e != nil {
		return fmt.Errorf("供应商退款模块未确认，未提交申请")
	}
	if !strings.Contains(string(page), "/plugins/addons/qkstack/") {
		return fmt.Errorf("供应商尚未配置已验证的财务退款模块，未提交申请")
	}
	b, e := q.authorizedFormPost(refundPluginPath, url.Values{"action": {"refund"}, "hid": {strconv.FormatInt(id, 10)}, "reason": {reason}})
	if e != nil {
		return fmt.Errorf("退款申请结果待核对，禁止重复提交")
	}
	_, e = businessData(b, "supplier refund submission")
	return e
}
func (p *Provider) RefundRecords(id int64) ([]contracts.SupplierRefund, error) {
	if id <= 0 {
		return nil, fmt.Errorf("invalid refund host")
	}
	result := []contracts.SupplierRefund{}
	for page := 1; page <= 20; page++ {
		rows, total, e := p.refundRows("getRefundList", page)
		if e != nil {
			return nil, e
		}
		for _, raw := range rows {
			r, ok := raw.(map[string]any)
			if !ok {
				return nil, fmt.Errorf("退款记录格式无效")
			}
			if firstMapString(r, "hid") != strconv.FormatInt(id, 10) {
				continue
			}
			amount := firstMapString(r, "amount")
			money, e := billing.ParseMoney(amount)
			if e != nil || money < 0 {
				return nil, fmt.Errorf("退款金额格式无效")
			}
			record := contracts.SupplierRefund{ID: firstMapString(r, "id"), HostID: firstMapString(r, "hid"), Amount: money.String(), Status: firstMapString(r, "status"), Reason: firstMapString(r, "refund_reason"), UpdatedAt: firstMapString(r, "update_time")}
			n, e := strconv.ParseInt(record.ID, 10, 64)
			if e != nil || n <= 0 {
				return nil, fmt.Errorf("退款编号格式无效")
			}
			result = append(result, record)
		}
		if page*100 >= total {
			return result, nil
		}
	}
	return nil, fmt.Errorf("退款记录不能完整核对")
}

var _ contracts.RefundProvider = (*Provider)(nil)
