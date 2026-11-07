package zjmf

import (
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"net/url"
	"regexp"
	"strings"

	"ddyidc/internal/upstream/provider/contracts"
)

var (
	natRowPattern  = regexp.MustCompile(`(?s)<tr[^>]*>(.*?)</tr>`)
	natCellPattern = regexp.MustCompile(`(?s)<td[^>]*>(.*?)</td>`)
	natIDPattern   = regexp.MustCompile(`data-id="([0-9]+)"`)
	tagPattern     = regexp.MustCompile(`(?s)<[^>]*>`)
)

func hasClientArea(raw any, key string) bool {
	items, ok := raw.([]any)
	if !ok {
		return false
	}
	for _, item := range items {
		row, ok := item.(map[string]any)
		if !ok {
			continue
		}
		if value, ok := row["key"].(string); ok && strings.EqualFold(strings.TrimSpace(value), key) {
			return true
		}
	}
	return false
}

func (p *Provider) ListNATRules(hostID int64) ([]contracts.NATRule, error) {
	if hostID <= 0 {
		return nil, errors.New("upstream host ID must be positive")
	}
	body, err := p.authorizedFormPost("/zjmf_api/provision/custom/content", url.Values{
		"id": {fmt.Sprint(hostID)}, "key": {"nat_acl"},
	})
	if err != nil {
		return nil, err
	}
	var payload struct {
		Status int `json:"status"`
		Data   struct {
			HTML string `json:"html"`
		} `json:"data"`
	}
	if json.Unmarshal(body, &payload) != nil || payload.Status != 200 {
		return nil, errors.New("NAT 规则读取失败")
	}
	rules := make([]contracts.NATRule, 0)
	for _, row := range natRowPattern.FindAllStringSubmatch(payload.Data.HTML, -1) {
		cells := natCellPattern.FindAllStringSubmatch(row[1], -1)
		if len(cells) < 4 {
			continue
		}
		id := ""
		if match := natIDPattern.FindStringSubmatch(row[1]); len(match) == 2 {
			id = match[1]
		}
		rules = append(rules, contracts.NATRule{
			ID:       id,
			Name:     cleanCell(cells[0][1]),
			External: cleanCell(cells[1][1]),
			Internal: cleanCell(cells[2][1]),
			Protocol: cleanCell(cells[3][1]),
		})
	}
	return rules, nil
}

func cleanCell(value string) string {
	return strings.TrimSpace(html.UnescapeString(tagPattern.ReplaceAllString(value, "")))
}

func (p *Provider) CreateNATRule(hostID int64, input contracts.NATRuleInput) (*contracts.NATRule, error) {
	if hostID <= 0 || strings.TrimSpace(input.Name) == "" || strings.TrimSpace(input.Internal) == "" {
		return nil, errors.New("NAT 规则参数不完整")
	}
	protocol := strings.TrimSpace(input.Protocol)
	if protocol == "" {
		protocol = "3"
	}
	form := url.Values{"func": {"addNatAcl"}, "name": {input.Name}, "int_port": {input.Internal}, "select-protocol": {protocol}}
	if input.External != "" {
		form.Set("ext_port", input.External)
	}
	if err := p.natAction(hostID, form); err != nil {
		return nil, err
	}
	return &contracts.NATRule{Name: input.Name, External: input.External, Internal: input.Internal, Protocol: protocol}, nil
}

func (p *Provider) DeleteNATRule(hostID int64, ruleID string) error {
	if hostID <= 0 || strings.TrimSpace(ruleID) == "" {
		return errors.New("NAT 规则编号无效")
	}
	return p.natAction(hostID, url.Values{"func": {"delNatAcl"}, "id": {strings.TrimSpace(ruleID)}})
}

func (p *Provider) natAction(hostID int64, form url.Values) error {
	body, err := p.authorizedFormPost(fmt.Sprintf("/provision/custom/%d", hostID), form)
	if err != nil {
		return err
	}
	var payload struct {
		Status int    `json:"status"`
		Msg    string `json:"msg"`
	}
	if json.Unmarshal(body, &payload) != nil {
		return errors.New("NAT 操作返回格式无效")
	}
	if payload.Status != 200 {
		if payload.Msg == "" {
			payload.Msg = "NAT 操作失败"
		}
		return errors.New(payload.Msg)
	}
	return nil
}
