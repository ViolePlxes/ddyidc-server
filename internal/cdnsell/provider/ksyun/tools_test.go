package ksyun

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestCertificateScopedResponse(t *testing.T) {
	body := `{"Certificates":[{"CertificateId":"1","CertificateName":"test","IssueDomain":"cdn-test.ddoidc.com","ConfigDomainNames":"cdn-test.ddoidc.com","PrivateKey":"NEVER_EXPOSE_SECRET","CertificateContent":"PUBLIC_PEM"},{"CertificateId":"2","ConfigDomainNames":"other.example.com"}]}`
	rows, e := parseCloudCertificates(body, "cdn-test.ddoidc.com")
	if e != nil || len(rows) != 1 || rows[0].ID != "1" {
		t.Fatal("scoping failed", e)
	}
	raw, _ := json.Marshal(rows)
	if strings.Contains(string(raw), "NEVER_EXPOSE_SECRET") || strings.Contains(string(raw), "PUBLIC_PEM") {
		t.Fatal("raw certificate material exposed")
	}
	if _, e = parseCloudCertificates(`{}`, "cdn-test.ddoidc.com"); e == nil {
		t.Fatal("missing certificate field accepted")
	}
	if rows, e = parseCloudCertificates(`{"Certificates":[]}`, "cdn-test.ddoidc.com"); e != nil || len(rows) != 0 {
		t.Fatal("valid empty rejected")
	}
}
