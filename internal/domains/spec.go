package domains

import (
	_ "embed"
	"encoding/json"
	"errors"
	"golang.org/x/net/idna"
	"net"
	"regexp"
	"strconv"
	"strings"
)

//go:embed operations.json
var operationJSON []byte

type Field struct {
	Key      string `json:"key"`
	Label    string `json:"label"`
	Required bool   `json:"required"`
	Secret   bool   `json:"secret,omitempty"`
	Numeric  bool   `json:"numeric,omitempty"`
	Hidden   bool   `json:"hidden,omitempty"`
}
type OperationSpec struct {
	Key    string  `json:"key"`
	Path   string  `json:"-"`
	Method string  `json:"-"`
	Write  bool    `json:"write"`
	Paid   bool    `json:"paid"`
	Scope  string  `json:"scope"`
	Fields []Field `json:"fields"`
}

func Specs() []OperationSpec {
	var result []OperationSpec
	_ = json.Unmarshal(operationJSON, &result)
	return result
}

// Wire locations are private, and clients select only reviewed operation IDs.
func operation(key string) (OperationSpec, error) {
	var wire []struct {
		Key, Path, Method, Scope string
		Write, Paid              bool
		Fields                   []Field
	}
	if e := json.Unmarshal(operationJSON, &wire); e != nil {
		return OperationSpec{}, e
	}
	for _, v := range wire {
		if v.Key == key {
			return OperationSpec{v.Key, v.Path, v.Method, v.Write, v.Paid, v.Scope, v.Fields}, nil
		}
	}
	return OperationSpec{}, errors.New("不支持的域名操作")
}
func publicSpecs() []OperationSpec {
	var out []OperationSpec
	for _, s := range Specs() {
		if s.Scope != "admin" && s.Key != "4.9" {
			out = append(out, s)
		}
	}
	return out
}

var asciiDomain = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]*[a-z0-9])?$`)

func normalizeDomain(raw string) (string, error) {
	if strings.TrimSpace(raw) != raw || strings.ContainsAny(raw, "/:@?#,;\\ \r\n\t") {
		return "", errors.New("请输入单个有效域名")
	}
	value, e := idna.Lookup.ToASCII(strings.ToLower(raw))
	if e != nil || len(value) > 253 || net.ParseIP(value) != nil {
		return "", errors.New("域名格式无效")
	}
	parts := strings.Split(value, ".")
	if len(parts) < 2 {
		return "", errors.New("请填写完整域名")
	}
	for _, p := range parts {
		if len(p) > 63 || !asciiDomain.MatchString(p) {
			return "", errors.New("域名格式无效")
		}
	}
	return value, nil
}
func validateInput(op OperationSpec, input map[string]string) (map[string]string, error) {
	result := map[string]string{}
	allowed := map[string]Field{}
	for _, f := range op.Fields {
		allowed[f.Key] = f
		if f.Required && strings.TrimSpace(input[f.Key]) == "" {
			return nil, errors.New("请填写" + f.Label)
		}
	}
	for k, v := range input {
		f, ok := allowed[k]
		if !ok || f.Hidden {
			return nil, errors.New("请求包含不允许的字段")
		}
		limit := 4096
		if k == "file_org" {
			limit = 6 << 20
		}
		if len(v) > limit || strings.ContainsRune(v, 0) {
			return nil, errors.New("填写内容过长或无效")
		}
		if v == "" {
			continue
		}
		if f.Numeric {
			n, e := strconv.Atoi(v)
			if e != nil || n < 0 || n > 1000000 {
				return nil, errors.New(f.Label + "格式无效")
			}
		}
		if k == "year" || k == "regyear" || k == "buyyear" {
			n, _ := strconv.Atoi(v)
			if n < 1 || n > 10 {
				return nil, errors.New("年限须为1至10年")
			}
		}
		if k == "domain" && op.Key != "3.1" {
			value, e := normalizeDomain(v)
			if e != nil {
				return nil, e
			}
			v = value
		}
		result[k] = v
	}
	if op.Key == "3.1" {
		if strings.ContainsAny(result["domain"], "/:@?#,;\\ \r\n\t") {
			return nil, errors.New("查询名称无效")
		}
		suffixes := strings.Split(result["suffix"], ",")
		if len(suffixes) > 20 {
			return nil, errors.New("一次最多查询20个后缀")
		}
		for _, suffix := range suffixes {
			if _, e := normalizeDomain("example" + suffix); e != nil || !strings.HasPrefix(suffix, ".") {
				return nil, errors.New("查询后缀无效")
			}
		}
	}
	if op.Key == "3.5" && len(result["domainpwd"]) < 8 {
		return nil, errors.New("管理密码至少8位")
	}
	if op.Key == "3.12" {
		if result["val"] != "0" && result["val"] != "1" {
			return nil, errors.New("锁定状态无效")
		}
		if v := result["status"]; v != "" && v != "update" && v != "transfer" {
			return nil, errors.New("锁定范围无效")
		}
	}
	if host := result["hostname"]; host != "" {
		if !strings.Contains(host, ".") {
			host += "." + result["domain"]
		}
		normalized, e := normalizeDomain(host)
		if e != nil || !strings.HasSuffix(normalized, "."+result["domain"]) {
			return nil, errors.New("DNS主机必须属于所选域名")
		}
		result["hostname"] = normalized
	}
	if ips := result["dnsip"]; ips != "" {
		values := strings.Split(ips, ",")
		if len(values) > 6 {
			return nil, errors.New("DNS主机最多填写6个IP")
		}
		for _, v := range values {
			if net.ParseIP(v) == nil {
				return nil, errors.New("DNS主机IP格式无效")
			}
		}
	}
	for _, key := range []string{"dns1", "dns2", "dns3", "dns4", "dns5", "dns6", "dns_host1", "dns_host2"} {
		if v := result[key]; v != "" {
			domain, e := normalizeDomain(v)
			if e != nil {
				return nil, errors.New("DNS服务器名称无效")
			}
			result[key] = domain
		}
	}
	if op.Key == "3.7" {
		if !regexp.MustCompile(`^(A|AAAA|CNAME|MX|TXT|SRV)$`).MatchString(result["type"]) {
			return nil, errors.New("解析类型无效")
		}
	}
	if ttl := result["ttl"]; ttl != "" {
		n, _ := strconv.Atoi(ttl)
		if n < 60 || n > 86400 {
			return nil, errors.New("TTL须为60至86400秒")
		}
	}
	if op.Key == "1.1" {
		result["type"] = "domain"
		domain, e := normalizeDomain(result["value"])
		if e != nil {
			return nil, e
		}
		result["value"] = domain
	}
	if op.Key == "3.28" {
		result["comm"] = "submit"
	}
	if op.Key == "3.28.check" {
		result["comm"] = "check"
	}
	return result, nil
}
