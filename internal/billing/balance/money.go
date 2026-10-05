package billing

import (
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// Money stores cents in memory and exact decimal yuan at the database boundary.
// It deliberately rejects floating point database values.
type Money int64

const MaxMoney Money = 99999999999999

var moneyPattern = regexp.MustCompile(`^-?(0|[1-9][0-9]{0,11})(\.[0-9]{1,2})?$`)

func ParseMoney(value string) (Money, error) {
	if !moneyPattern.MatchString(value) {
		return 0, errors.New("金额必须为最多两位小数的十进制字符串")
	}
	negative := strings.HasPrefix(value, "-")
	value = strings.TrimPrefix(value, "-")
	parts := strings.SplitN(value, ".", 2)
	whole, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		return 0, err
	}
	fraction := int64(0)
	if len(parts) == 2 {
		fraction, _ = strconv.ParseInt(parts[1]+strings.Repeat("0", 2-len(parts[1])), 10, 64)
	}
	amount := Money(whole*100 + fraction)
	if amount > MaxMoney {
		return 0, errors.New("金额超出账户上限")
	}
	if negative {
		amount = -amount
	}
	return amount, nil
}
func (m Money) String() string {
	sign := ""
	if m < 0 {
		sign = "-"
		m = -m
	}
	return fmt.Sprintf("%s%d.%02d", sign, m/100, m%100)
}
func (m Money) Value() (driver.Value, error) {
	if m > MaxMoney || m < -MaxMoney {
		return nil, errors.New("金额超出账户上限")
	}
	return m.String(), nil
}
func (m *Money) Scan(value any) error {
	var text string
	switch v := value.(type) {
	case []byte:
		text = string(v)
	case string:
		text = v
	case int64:
		text = strconv.FormatInt(v, 10)
	default:
		return fmt.Errorf("unsupported exact money source %T", value)
	}
	parsed, err := ParseMoney(text)
	if err == nil {
		*m = parsed
	}
	return err
}
func (m Money) MarshalJSON() ([]byte, error) { return []byte(strconv.Quote(m.String())), nil }

// JSON boundaries use exact decimal yuan strings, matching database values.
// Reject numeric JSON to avoid interpreting cents or floating point as yuan.
func (m *Money) UnmarshalJSON(raw []byte) error {
	var text string
	if err := json.Unmarshal(raw, &text); err != nil {
		return errors.New("金额必须为十进制元字符串")
	}
	value, err := ParseMoney(text)
	if err != nil {
		return err
	}
	*m = value
	return nil
}
