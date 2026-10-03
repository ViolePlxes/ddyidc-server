package auth

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestV10PasswordKnownVectors(t *testing.T) {
	for _, v := range []struct{ password, digest string }{
		{"Original!123", "8892e4e2c6ed323c8369e0350bf43342"},
		{"原密码<&>", "31c8c9b730d99f4a2b7db709e294381b"},
	} {
		hash := V10PasswordPrefix + "###" + v.digest
		if !VerifyV10Password(hash, v.password, "fixture-authcode") {
			t.Fatal("independent v10 vector rejected")
		}
		if VerifyV10Password(hash, v.password+"wrong", "fixture-authcode") || VerifyV10Password(hash, v.password, "wrong-key") || VerifyV10Password(hash, v.password, "") {
			t.Fatal("wrong credential accepted")
		}
		if PasswordMatches(hash, v.password) || VerifyV10Password("###"+v.digest, v.password, "fixture-authcode") {
			t.Fatal("unmarked or ordinary authentication accepted legacy credential")
		}
	}
}

func TestMigratedPasswordPreservesLongUnicode(t *testing.T) {
	for _, password := range []string{"Original!123", strings.Repeat("界", 40)} {
		hash, err := HashMigratedPassword(password)
		if err != nil || !PasswordMatches(hash, password) || PasswordMatches(hash, password+"!") {
			t.Fatal("transparent hash roundtrip failed", err)
		}
		if len([]byte(password)) > 72 && PasswordMatches(hash, password[:72]) {
			t.Fatal("truncated password accepted")
		}
	}
}

func TestV10KeyFileFailsClosed(t *testing.T) {
	t.Setenv("DDYIDC_V10_AUTHCODE_FILE", "")
	if _, err := LoadV10AuthCode(); err == nil {
		t.Fatal("missing key accepted")
	}
	path := filepath.Join(t.TempDir(), "key")
	if err := os.WriteFile(path, []byte("fixture-authcode\n"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DDYIDC_V10_AUTHCODE_FILE", path)
	if key, err := LoadV10AuthCode(); err != nil || key != "fixture-authcode" {
		t.Fatal("key file failed", err)
	}
	if err := os.Chmod(path, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadV10AuthCode(); err == nil {
		t.Fatal("publicly readable key accepted")
	}
}
