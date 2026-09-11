package provider

import (
	"context"
	"errors"
	"time"
)

var ErrCapabilityNotOpen = errors.New("金山云当前账号未开放该 API 能力")

type Account struct{ AccessKey, SecretKey string }
type Domain struct{ ServiceNo, BusinessType string }
type ResourcePackage struct {
	ID, Name                  string
	CapacityBytes, UsedBytes  int64
	EffectiveTime, ExpireTime time.Time
}
type UsagePoint struct {
	Start, End time.Time
	Bytes      int64
}
type DomainInput struct{ Domain, BusinessType, Region, OriginType, OriginHost, OriginProtocol string }

// Provider is the complete CDN capability boundary. P1-supported methods are
// implemented by the Kingsoft provider; unopened capabilities return
// ErrCapabilityNotOpen rather than simulated success.
type Provider interface {
	TestConnection(context.Context, Account) error
	GetResourcePackages(context.Context, Account) ([]ResourcePackage, error)
	GetUsage(context.Context, Account, Domain, time.Time, time.Time) ([]UsagePoint, error)
	AddDomain(context.Context, Account, DomainInput) (string, error)
	RemoveDomain(context.Context, Account, string) error
	SetDomainEnabled(context.Context, Account, string, bool) error
	GetDomain(context.Context, Account, string) (map[string]any, error)
	SetDomainConfig(context.Context, Account, string, string, map[string]any) error
	GetDomainConfig(context.Context, Account, string, string) (map[string]any, error)
	DeployCertificate(context.Context, Account, string, string, string) error
	SubmitRefresh(context.Context, Account, string, []string) (string, error)
	GetRefreshProgress(context.Context, Account, string) (map[string]any, error)
	GetLogs(context.Context, Account, string, time.Time, time.Time) ([]map[string]any, error)
	CheckIP(context.Context, Account, string) (map[string]any, error)
}
