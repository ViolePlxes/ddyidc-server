package zjmf

import (
	"ddyidc/internal/upstream/provider/contracts"
	"encoding/json"
	"fmt"
	"golang.org/x/net/html"
	"net/url"
	"strconv"
	"strings"
)

func securityAttribute(n *html.Node, key string) string {
	for _, a := range n.Attr {
		if a.Key == key {
			return a.Val
		}
	}
	return ""
}
func securityText(n *html.Node) string {
	if n.Type == html.TextNode {
		return n.Data
	}
	var s strings.Builder
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		s.WriteString(securityText(c))
	}
	return strings.TrimSpace(s.String())
}
func securityWalk(n *html.Node, f func(*html.Node)) {
	f(n)
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		securityWalk(c, f)
	}
}
func parseSecurityGroups(source string) ([]contracts.SecurityGroup, error) {
	if len(source) > 2<<20 {
		return nil, fmt.Errorf("security response too large")
	}
	root, e := html.Parse(strings.NewReader(source))
	if e != nil {
		return nil, e
	}
	out := []contracts.SecurityGroup{}
	securityWalk(root, func(n *html.Node) {
		if n.Type != html.ElementNode || n.Data != "tr" {
			return
		}
		cells := []string{}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			if c.Data == "td" {
				cells = append(cells, securityText(c))
			}
		}
		if len(cells) < 3 {
			return
		}
		g := contracts.SecurityGroup{Name: cells[0], Description: cells[1]}
		// The finance module marks the bound group in its name and removes the
		// apply button. Preserve the stable ownership marker when reading it.
		for _, prefix := range []string{"(当前)", "（当前）"} {
			if strings.HasPrefix(g.Name, prefix) {
				g.Name = strings.TrimSpace(strings.TrimPrefix(g.Name, prefix))
				g.Applied = true
				break
			}
		}
		securityWalk(n, func(c *html.Node) {
			classes := strings.Fields(securityAttribute(c, "class"))
			for _, cl := range classes {
				if cl == "apply" || cl == "trView" {
					if id := securityAttribute(c, "data-id"); id != "" {
						g.ID = id
					}
				}
			}
			if strings.Contains(securityText(c), "已应用") && c.Data == "button" {
				g.Applied = true
			}
		})
		if id, e := strconv.ParseInt(g.ID, 10, 64); e == nil && id > 0 {
			out = append(out, g)
		}
	})
	return out, nil
}
func (p *Provider) SecurityGroups(id int64) ([]contracts.SecurityGroup, error) {
	if id <= 0 {
		return nil, fmt.Errorf("invalid host")
	}
	b, e := p.authorizedFormPost("/zjmf_api/provision/custom/content", url.Values{"id": {strconv.FormatInt(id, 10)}, "key": {"security_groups"}})
	if e != nil {
		return nil, e
	}
	d, e := businessData(b, "security groups")
	if e != nil {
		return nil, e
	}
	source := firstMapString(d, "html")
	if source == "" {
		return nil, fmt.Errorf("该产品未提供安全组功能")
	}
	return parseSecurityGroups(source)
}
func (p *Provider) SecurityCommand(id int64, command string, fields map[string]string) error {
	if id <= 0 {
		return fmt.Errorf("invalid host")
	}
	allowed := map[string][]string{"createSecurityGroup": {"name", "description"}, "linkSecurityGroup": {"id"}, "delSecurityGroup": {"id"}, "createSecurityRule": {"id", "direction", "protocol", "port", "ip"}, "delSecurityRule": {"id", "group"}}
	keys, ok := allowed[command]
	if !ok {
		return fmt.Errorf("unsupported security command")
	}
	f := url.Values{"func": {command}}
	for _, k := range keys {
		f.Set(k, fields[k])
	}
	q := *p
	q.procurementContext = true
	b, e := q.authorizedFormPost("/provision/custom/"+strconv.FormatInt(id, 10), f)
	if e != nil {
		return fmt.Errorf("安全组操作结果待核对，请刷新后检查，禁止自动重试")
	}
	_, e = businessData(b, "security command")
	return e
}
func (p *Provider) SecurityRules(id int64, group string) ([]contracts.SecurityRule, error) {
	if id <= 0 {
		return nil, fmt.Errorf("invalid host")
	}
	if n, e := strconv.ParseInt(group, 10, 64); e != nil || n <= 0 {
		return nil, fmt.Errorf("invalid group")
	}
	b, e := p.authorizedFormPost("/provision/custom/"+strconv.FormatInt(id, 10), url.Values{"func": {"showSecurityRules"}, "id": {group}})
	if e != nil {
		return nil, e
	}
	if _, e = businessData(b, "security rules"); e != nil {
		return nil, e
	}
	var root map[string]any
	if e = json.Unmarshal(b, &root); e != nil {
		return nil, e
	}
	d := root
	if nested, ok := root["data"].(map[string]any); ok {
		d = nested
	}
	out := []contracts.SecurityRule{}
	rows, ok := d["list"].([]any)
	if !ok {
		if d["list"] == nil {
			return out, nil
		}
		return nil, fmt.Errorf("安全组规则响应格式无效")
	}
	for _, row := range rows {
		r, ok := row.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("安全组规则格式无效")
		}
		port := firstMapString(r, "port")
		if port == "" {
			port = firstMapString(r, "start_port")
			if end := firstMapString(r, "end_port"); end != "" && end != port {
				port += "-" + end
			}
		}
		out = append(out, contracts.SecurityRule{ID: firstMapString(r, "id"), Direction: firstMapString(r, "direction"), Protocol: firstMapString(r, "protocol"), Port: port, IP: firstMapString(r, "ip")})
	}
	return out, nil
}
