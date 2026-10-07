// Package mfa provides identity-independent RFC 6238 factors. HTTP login and
// customer/admin policy belong to callers, not this cryptographic primitive.
package mfa

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"crypto/subtle"
	"encoding/base32"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"
)

func NewSecret() (string, error) {
	b := make([]byte, 20)
	if _, e := rand.Read(b); e != nil {
		return "", e
	}
	return base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(b), nil
}
func Code(secret string, step int64, digits int) (string, error) {
	if step < 0 || (digits != 6 && digits != 8) {
		return "", errors.New("invalid TOTP parameters")
	}
	key, e := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(strings.ToUpper(secret))
	if e != nil || len(key) < 16 {
		return "", errors.New("invalid TOTP secret")
	}
	var counter [8]byte
	binary.BigEndian.PutUint64(counter[:], uint64(step))
	mac := hmac.New(sha1.New, key)
	mac.Write(counter[:])
	sum := mac.Sum(nil)
	offset := sum[len(sum)-1] & 15
	value := binary.BigEndian.Uint32(sum[offset:offset+4]) & 0x7fffffff
	mod := uint32(1000000)
	if digits == 8 {
		mod = 100000000
	}
	return fmt.Sprintf("%0*d", digits, value%mod), nil
}

// Match accepts at most one step of clock drift. Callers atomically persist the
// accepted step, rejecting replay across challenges, nodes, and concurrent calls.
func Match(secret, code string, now time.Time, lastStep int64) (int64, bool) {
	if len(code) != 6 {
		return 0, false
	}
	for _, c := range code {
		if c < '0' || c > '9' {
			return 0, false
		}
	}
	step := now.Unix() / 30
	for _, delta := range []int64{0, -1, 1} {
		s := step + delta
		if s <= lastStep {
			continue
		}
		expected, e := Code(secret, s, 6)
		if e == nil && subtle.ConstantTimeCompare([]byte(code), []byte(expected)) == 1 {
			return s, true
		}
	}
	return 0, false
}
func URI(secret, issuer, account string) string {
	q := url.Values{"secret": {secret}, "issuer": {issuer}, "algorithm": {"SHA1"}, "digits": {"6"}, "period": {"30"}}
	return "otpauth://totp/" + url.PathEscape(issuer+":"+account) + "?" + q.Encode()
}
func RandomToken(bytes int) (string, error) {
	b := make([]byte, bytes)
	if _, e := rand.Read(b); e != nil {
		return "", e
	}
	return hex.EncodeToString(b), nil
}
func RecoveryCodes() ([]string, error) {
	codes := make([]string, 10)
	for i := range codes {
		v, e := RandomToken(10)
		if e != nil {
			return nil, e
		}
		codes[i] = v[:5] + "-" + v[5:10] + "-" + v[10:15] + "-" + v[15:]
	}
	return codes, nil
}
