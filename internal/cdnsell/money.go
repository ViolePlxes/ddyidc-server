package cdnsell

import (
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// Yuan stores integer cents internally. SQL and JSON boundaries are decimal yuan.
type Yuan int64

func (v Yuan) String() string {
	n := int64(v)
	sign := ""
	if n < 0 {
		sign = "-"
		n = -n
	}
	return fmt.Sprintf("%s%d.%02d", sign, n/100, n%100)
}
func (v Yuan) Value() (driver.Value, error) {
	if v > 99999999999999 || v < -99999999999999 {
		return nil, errors.New("金额超出范围")
	}
	return v.String(), nil
}
func (v Yuan) GormDataType() string         { return "decimal(14,2)" }
func (v Yuan) MarshalJSON() ([]byte, error) { return json.Marshal(v.String()) }
func (v *Yuan) UnmarshalJSON(b []byte) error {
	value := string(b)
	if len(b) > 0 && b[0] == '"' {
		if e := json.Unmarshal(b, &value); e != nil {
			return e
		}
	}
	return v.parse(value)
}
func (v *Yuan) Scan(value any) error {
	switch n := value.(type) {
	case []byte:
		return v.parse(string(n))
	case string:
		return v.parse(n)
	case int64:
		return v.parse(strconv.FormatInt(n, 10))
	default:
		return errors.New("金额数据库类型无效")
	}
}
func (v *Yuan) parse(raw string) error {
	sign := int64(1)
	if strings.HasPrefix(raw, "-") {
		sign = -1
		raw = raw[1:]
	}
	parts := strings.Split(raw, ".")
	if len(parts) > 2 || parts[0] == "" {
		return errors.New("金额格式无效")
	}
	for _, p := range parts {
		if p == "" {
			return errors.New("金额格式无效")
		}
		for _, c := range p {
			if c < '0' || c > '9' {
				return errors.New("金额格式无效")
			}
		}
	}
	whole, e := strconv.ParseInt(parts[0], 10, 64)
	if e != nil || whole > 999999999999 {
		return errors.New("金额超出范围")
	}
	fraction := int64(0)
	if len(parts) == 2 {
		if len(parts[1]) > 2 {
			if strings.TrimRight(parts[1][2:], "0") != "" {
				return errors.New("金额最多两位小数")
			}
			parts[1] = parts[1][:2]
		}
		f := parts[1]
		if len(f) == 1 {
			f += "0"
		}
		fraction, _ = strconv.ParseInt(f, 10, 64)
	}
	*v = Yuan(sign * (whole*100 + fraction))
	return nil
}
