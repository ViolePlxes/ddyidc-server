package zjmf

import (
	"bytes"
	"encoding/json"
	"errors"
)

// Finance returns false when no customer discount applies. Discounted accounts
// return an object. Do not treat missing fields or arbitrary scalars as zero.
type financeQuoteDiscount struct {
	Type     int             `json:"type"`
	Discount json.RawMessage `json:"bates"`
}

func (v *financeQuoteDiscount) UnmarshalJSON(raw []byte) error {
	raw = bytes.TrimSpace(raw)
	if bytes.Equal(raw, []byte("false")) {
		*v = financeQuoteDiscount{Discount: json.RawMessage(`"0.00"`)}
		return nil
	}
	if len(raw) == 0 || raw[0] != '{' {
		return errors.New("unsupported finance discount structure")
	}
	type object financeQuoteDiscount
	var value object
	if err := json.Unmarshal(raw, &value); err != nil {
		return err
	}
	if value.Type != 0 && value.Type != 1 {
		return errors.New("unsupported finance discount type")
	}
	*v = financeQuoteDiscount(value)
	return nil
}
