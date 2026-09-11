package cdnsell

import (
	"net"
	"reflect"
	"testing"
)

func TestValidateDomainInput(t *testing.T) {
	tests := []struct {
		name    string
		input   DomainInput
		wantErr bool
	}{
		{"valid domain origin", DomainInput{Domain: "CDN.Example.COM.", BusinessType: "web", AccelerateRegion: "mainland", OriginType: "domain", OriginHost: "origin.example.com", OriginProtocol: "follow"}, false},
		{"same origin", DomainInput{Domain: "cdn.example.com", BusinessType: "web", AccelerateRegion: "mainland", OriginType: "domain", OriginHost: "cdn.example.com", OriginProtocol: "https"}, true},
		{"private origin", DomainInput{Domain: "cdn.example.com", BusinessType: "download", AccelerateRegion: "global", OriginType: "ip", OriginHost: "127.0.0.1", OriginProtocol: "http"}, true},
		{"bad region", DomainInput{Domain: "cdn.example.com", BusinessType: "video", AccelerateRegion: "moon", OriginType: "ip", OriginHost: "1.1.1.1", OriginProtocol: "https"}, true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			input := test.input
			err := validateDomainInput(&input)
			if (err != nil) != test.wantErr {
				t.Fatalf("validateDomainInput() error = %v, wantErr %v", err, test.wantErr)
			}
			if err == nil && input.Domain != "cdn.example.com" {
				t.Fatalf("domain was not normalized: %q", input.Domain)
			}
		})
	}
}

func TestPublicIPGuard(t *testing.T) {
	for _, value := range []string{"127.0.0.1", "10.0.0.1", "169.254.1.1", "224.0.0.1", "::1"} {
		if isPublicIP(net.ParseIP(value)) {
			t.Fatalf("%s must not be accepted as a public origin", value)
		}
	}
	if !isPublicIP(net.ParseIP("1.1.1.1")) {
		t.Fatal("public IP was rejected")
	}
}

func TestValidateSensitiveConfig(t *testing.T) {
	valid := map[string]any{"enabled": true, "authType": "1", "primaryKey": "secret-123", "backupKey": "backup-456", "expireSeconds": float64(300)}
	if err := validateConfig("request_auth", valid); err != nil {
		t.Fatalf("valid auth config rejected: %v", err)
	}
	invalid := map[string]any{"enabled": true, "authType": "1", "primaryKey": "short", "expireSeconds": float64(300)}
	if err := validateConfig("request_auth", invalid); err == nil {
		t.Fatal("short authentication key must be rejected")
	}
	sanitized := sanitizeConfig(valid).(map[string]any)
	if sanitized["primaryKey"] != "••••••" || sanitized["backupKey"] != "••••••" {
		t.Fatalf("sensitive values were not masked: %#v", sanitized)
	}
}

func TestConfigDiffIgnoresUnavailableCloudFields(t *testing.T) {
	local := map[string]any{"enabled": true, "nested": map[string]any{"mode": "strict"}, "localOnly": "value"}
	cloud := map[string]any{"enabled": false, "payload": map[string]any{"mode": "strict"}}
	want := []string{"enabled"}
	if got := configDiff(local, cloud); !reflect.DeepEqual(got, want) {
		t.Fatalf("configDiff() = %#v, want %#v", got, want)
	}
}

func TestP2DefinesExactlyEighteenModules(t *testing.T) {
	if got := len(configModuleDefinitions); got != 18 {
		t.Fatalf("config module count = %d, want 18", got)
	}
	seen := map[string]bool{}
	for _, definition := range configModuleDefinitions {
		if seen[definition.Key] {
			t.Fatalf("duplicate module key %q", definition.Key)
		}
		seen[definition.Key] = true
	}
}
