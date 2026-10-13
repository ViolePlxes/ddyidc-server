package provider

import "context"

type CloudCertificate struct {
	ID             string `json:"id"`
	Name           string `json:"name"`
	IssueDomain    string `json:"issueDomain"`
	IssueTime      string `json:"issueTime"`
	ExpirationTime string `json:"expirationTime"`
	Enabled        bool   `json:"enabled"`
	Type           string `json:"type"`
}
type CertificateReader interface {
	GetCertificates(context.Context, Account, string) ([]CloudCertificate, error)
}
