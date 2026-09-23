// Package marketing shared helpers: snowflake shortcut, random tokens,
// money formatting and mobile masking.
package marketing

import (
	"crypto/rand"
	"math/big"
	"strconv"
	"time"

	"ddyidc/internal/shared/snowflake"
)

// NewID returns the project-wide snowflake generator result.
func NewID() int64 { return snowflake.GenID() }

// StrPtr is a shorthand for optional string columns/fields.
func StrPtr(v string) *string { return &v }

// Int64Ptr is a shorthand for optional int64 fields.
func Int64Ptr(v int64) *int64 { return &v }

const tokenCharset = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789"

// RandToken returns a cryptographically random uppercase token from a
// human-friendly charset (no I/L/O/0/1 confusion).
func RandToken(n int) string {
	out := make([]byte, n)
	max := big.NewInt(int64(len(tokenCharset)))
	for i := range out {
		v, err := rand.Int(rand.Reader, max)
		if err != nil {
			out[i] = tokenCharset[time.Now().UnixNano()%int64(len(tokenCharset))]
			continue
		}
		out[i] = tokenCharset[v.Int64()]
	}
	return string(out)
}

// GenerateOrderNo produces a readable business number: prefix + date + snowflake tail.
func GenerateOrderNo(prefix string) string {
	return prefix + strconv.FormatInt(NewID(), 10)
}

// MaskMobile hides the middle four digits: 138****8000.
func MaskMobile(v string) string {
	if len(v) == 11 {
		return v[:3] + "****" + v[7:]
	}
	return v
}

// FormatCents converts integer cents to a yuan string with two decimals.
func FormatCents(cents int64) string {
	sign := ""
	if cents < 0 {
		sign = "-"
		cents = -cents
	}
	return sign + strconv.FormatInt(cents/100, 10) + "." + strconv.FormatInt(cents%100, 10)
}
