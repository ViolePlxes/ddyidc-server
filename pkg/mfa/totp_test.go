package mfa

import (
	"encoding/base32"
	"net/url"
	"testing"
	"time"
)

func TestRFC6238SHA1Vectors(t *testing.T) {
	secret := base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString([]byte("12345678901234567890"))
	for _, v := range []struct {
		ts   int64
		want string
	}{{59, "94287082"}, {1111111109, "07081804"}, {1111111111, "14050471"}, {1234567890, "89005924"}, {2000000000, "69279037"}, {20000000000, "65353130"}} {
		got, e := Code(secret, v.ts/30, 8)
		if e != nil || got != v.want {
			t.Fatalf("%d got %s %v", v.ts, got, e)
		}
	}
}
func TestReplayDriftAndFormat(t *testing.T) {
	secret, _ := NewSecret()
	now := time.Unix(2000000000, 0)
	s := now.Unix() / 30
	for _, delta := range []int64{-1, 0, 1} {
		code, _ := Code(secret, s+delta, 6)
		got, ok := Match(secret, code, now, -1)
		if !ok || got != s+delta {
			t.Fatal("drift rejected")
		}
		if _, ok = Match(secret, code, now, got); ok {
			t.Fatal("replay accepted")
		}
	}
	code, _ := Code(secret, s-2, 6)
	if _, ok := Match(secret, code, now, -1); ok {
		t.Fatal("excess drift")
	}
	if _, ok := Match(secret, "12345x", now, -1); ok {
		t.Fatal("invalid format")
	}
}
func TestProvisioningAndRecovery(t *testing.T) {
	secret, _ := NewSecret()
	u, e := url.Parse(URI(secret, "DDYIDC", "中文:a+b@example.com"))
	if e != nil || u.Scheme != "otpauth" || u.Query().Get("secret") != secret || u.Query().Get("period") != "30" {
		t.Fatal("provisioning URI")
	}
	codes, e := RecoveryCodes()
	if e != nil || len(codes) != 10 {
		t.Fatal(e)
	}
	seen := map[string]bool{}
	for _, c := range codes {
		if seen[c] || len(c) != 23 {
			t.Fatal("recovery entropy")
		}
		seen[c] = true
	}
}
