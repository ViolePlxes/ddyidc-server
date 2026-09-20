package remoteconsole

import (
	"encoding/base64"
	"net/url"
	"strings"
	"testing"
)

func TestFinanceNoVNCUnwrap(t *testing.T) {
	target := "wss://console.example/socket?token=test"
	raw := "https://supplier.example/dcim/novnc?" + url.Values{"url": {base64.StdEncoding.EncodeToString([]byte(target))}, "password": {"example-pass"}}.Encode()
	got, pass, e := unwrapNoVNC(raw, "")
	if e != nil || got != target || pass != "example-pass" {
		t.Fatal("finance wrapper not decoded")
	}
	if strings.Contains(got, "password=") {
		t.Fatal("password leaked into socket URL")
	}
	if _, _, e = unwrapNoVNC("https://supplier.example/dcim/novnc?url=bad", ""); e == nil {
		t.Fatal("invalid encoding accepted")
	}
}
