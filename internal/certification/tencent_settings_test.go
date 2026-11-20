package certification

import (
	crypt "ddyidc/pkg/crypto"
	"encoding/json"
	"strings"
	"testing"
)

func TestTencentCredentialValidation(t *testing.T) {
	good := tencentCredentials{SecretID: "AKIDtest0123456789", SecretKey: "testSecretKey0123456789", RuleID: "123456"}
	if _, err := newTencentClient(good); err != nil {
		t.Fatal(err)
	}
	// A blank or truncated secret must never be accepted: the channel would go
	// live and fail every request.
	for name, mutate := range map[string]func(*tencentCredentials){
		"no-secret-id":  func(c *tencentCredentials) { c.SecretID = "" },
		"no-secret-key": func(c *tencentCredentials) { c.SecretKey = "" },
		"short-key":     func(c *tencentCredentials) { c.SecretKey = "abc" },
		"bad-charset":   func(c *tencentCredentials) { c.SecretKey = "test secret key with spaces" },
		"no-rule":       func(c *tencentCredentials) { c.RuleID = "" },
		"rule-not-num":  func(c *tencentCredentials) { c.RuleID = "rule-1" },
		"plain-http":    func(c *tencentCredentials) { c.Endpoint = "http://faceid.tencentcloudapi.com" },
		"endpoint-path": func(c *tencentCredentials) { c.Endpoint = "https://faceid.tencentcloudapi.com/api" },
	} {
		t.Run(name, func(t *testing.T) {
			value := good
			mutate(&value)
			if _, err := newTencentClient(value); err == nil {
				t.Fatal("invalid credential accepted")
			}
		})
	}
}

func TestTencentCredentialDefaults(t *testing.T) {
	value := tencentCredentials{SecretID: goodID(), SecretKey: "testSecretKey0123456789", RuleID: "123456"}.withDefaults()
	if value.Endpoint != "https://faceid.tencentcloudapi.com" || value.SimThreshold != tencentDefaultSim || value.TimeoutSeconds != tencentDefaultTimeout {
		t.Fatalf("defaults = %+v", value)
	}
	// An out-of-range threshold falls back to the reviewed default instead of
	// silently passing every comparison.
	for _, threshold := range []float64{-1, 0, 101} {
		value := tencentCredentials{SimThreshold: threshold}.withDefaults()
		if value.SimThreshold != tencentDefaultSim {
			t.Fatalf("threshold %v -> %v", threshold, value.SimThreshold)
		}
	}
}

func goodID() string { return "AKIDtest0123456789" }

// The encrypted bundle must survive the round trip and never be stored as
// readable JSON.
func TestTencentCredentialEncryption(t *testing.T) {
	key := []byte("0123456789abcdef0123456789abcdef")
	bundle := tencentCredentials{SecretID: goodID(), SecretKey: "testSecretKey0123456789", RuleID: "123456", SimThreshold: 82, RedirectBase: "https://console.example.com"}.withDefaults()
	encoded, err := json.Marshal(bundle)
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := crypt.Encrypt(string(encoded), key)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(sealed, bundle.SecretID) || strings.Contains(sealed, bundle.SecretKey) {
		t.Fatal("plaintext credentials persisted")
	}
	plain, err := crypt.Decrypt(sealed, key)
	if err != nil {
		t.Fatal(err)
	}
	var restored tencentCredentials
	if err := json.Unmarshal([]byte(plain), &restored); err != nil {
		t.Fatal(err)
	}
	if restored != bundle {
		t.Fatalf("round trip = %+v", restored)
	}
}

func TestTencentAdminMasking(t *testing.T) {
	got := mask(goodID(), 4, 4)
	if !strings.HasPrefix(got, "AKID") || !strings.HasSuffix(got, "6789") || len(got) != len(goodID()) || strings.Contains(got, "test0123") {
		t.Fatalf("masked secret id = %s", got)
	}
	if got := mask("short", 4, 4); got != "*****" {
		t.Fatalf("short mask = %s", got)
	}
}

func TestTencentRedirectAllowList(t *testing.T) {
	open := tencentCredentials{}
	if got, err := tencentRedirect("https://console.example.com", open); err != nil || got != "https://console.example.com/api/v1/certification/tencent/callback" {
		t.Fatalf("same origin: %s %v", got, err)
	}
	if _, err := tencentRedirect("http://console.example.com", open); err == nil {
		t.Fatal("plain http origin accepted")
	}
	if _, err := tencentRedirect("http://127.0.0.1:13100", open); err != nil {
		t.Fatal("local preview origin rejected")
	}
	if _, err := tencentRedirect("https://console.example.com/evil", open); err == nil {
		t.Fatal("origin with a path accepted")
	}
	allow := tencentCredentials{RedirectBase: "https://console.example.com"}
	if _, err := tencentRedirect("https://evil.example", allow); err == nil {
		t.Fatal("foreign origin accepted despite redirectBase")
	}
	if _, err := tencentRedirect("https://console.example.com.evil.example", allow); err == nil {
		t.Fatal("suffix confusion accepted")
	}
	if _, err := tencentRedirect("https://console.example.com", allow); err != nil {
		t.Fatal(err)
	}
}
