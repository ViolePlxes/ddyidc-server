package zjmf

import (
	"ddyidc/internal/upstream/provider/contracts"
	"errors"
	"strconv"
	"strings"
	"time"
)

func parseInstanceSnapshot(data, host map[string]any, requested int64, result *contracts.InstanceDetail) error {
	result.ID = firstMapString(host, "id", "host_id")
	if result.ID != "" && result.ID != strconv.FormatInt(requested, 10) {
		return errors.New("upstream instance identity does not match")
	}
	result.ProductID = firstMapString(host, "productid", "product_id", "pid")
	result.BillingCycle = firstMapString(host, "billingcycle", "billing_cycle")
	result.ActivatedAt = instanceTime(host["regdate"])
	result.ExpiresAt = instanceTime(host["nextduedate"])
	rows, _ := data["config_options"].([]any)
	for i, raw := range rows {
		row, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		field := firstMapString(row, "name_k", "field")
		name := firstMapString(row, "name", "option_name")
		value := firstMapString(row, "sub_name", "value")
		// Do not persist credentials even if a supplier puts them in config_options.
		sensitive := strings.ToLower(field + name)
		if strings.Contains(sensitive, "password") || strings.Contains(sensitive, "secret") || strings.Contains(sensitive, "token") || strings.Contains(name, "密码") || value == "" {
			continue
		}
		if name == "" {
			name = field
		}
		if name == "" {
			continue
		}
		switch canonical := configField(name); canonical {
		case "data_center", "image", "cpu", "memory", "system_disk", "data_disk", "bandwidth", "ip_num", "network":
			field = canonical
		}
		id := firstMapString(row, "id", "configid")
		if id == "" {
			id = strconv.Itoa(i + 1)
		}
		result.Config = append(result.Config, contracts.InstanceConfig{ID: id, Field: field, Name: name, Value: value})
		if field == "os" || field == "image" {
			result.OperatingSystem = value
		}
	}
	return nil
}
func instanceTime(value any) *time.Time {
	raw := scalarJSONValue(value)
	if raw == "" || raw == "0" {
		return nil
	}
	if stamp, e := strconv.ParseInt(raw, 10, 64); e == nil && stamp > 0 {
		t := time.Unix(stamp, 0)
		return &t
	}
	for _, layout := range []string{time.RFC3339, "2006-01-02 15:04:05", "2006-01-02"} {
		if t, e := time.ParseInLocation(layout, raw, time.Local); e == nil && !t.IsZero() {
			return &t
		}
	}
	return nil
}
