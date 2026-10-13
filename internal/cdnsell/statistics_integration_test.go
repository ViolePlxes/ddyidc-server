package cdnsell

import (
	"context"
	"ddyidc/internal/cdnsell/provider"
	"ddyidc/internal/shared/snowflake"
	"ddyidc/internal/shared/testenv"
	cryptoutil "ddyidc/pkg/crypto"
	"errors"
	"fmt"
	"testing"
	"time"
)

type statisticsProofCloud struct {
	provider.Provider
	calls     int
	certCalls int
}

func (f *statisticsProofCloud) GetStatistics(ctx context.Context, a provider.Account, d provider.Domain, name string, q provider.StatisticsQuery) (provider.StatisticsData, error) {
	f.calls++
	if name == "" || d.ServiceNo == "" {
		return provider.StatisticsData{}, errors.New("unscoped cloud call")
	}
	return provider.StatisticsData{Action: "isolated", Rows: []provider.StatisticsRow{{Time: q.Start.Format(time.RFC3339), Values: map[string]float64{"flow": 1}}}}, nil
}
func (f *statisticsProofCloud) GetCertificates(ctx context.Context, a provider.Account, domain string) ([]provider.CloudCertificate, error) {
	f.certCalls++
	return []provider.CloudCertificate{{ID: "isolated", Name: domain}}, nil
}
func TestStatisticsCacheAuthorizationMySQL(t *testing.T) {
	db := testenv.MySQL(t)
	ctx := context.Background()
	key := []byte("01234567890123456789012345678901")
	s := NewService(db, key, nil)
	cloud := &statisticsProofCloud{}
	s.cloud = cloud
	account, id := snowflake.GenID(), snowflake.GenID()
	encrypted, e := cryptoutil.Encrypt("isolated", key)
	if e != nil {
		t.Fatal(e)
	}
	if e = db.Exec("INSERT INTO system.cdnsell_upstream_accounts(id,name,ak_encrypted,sk_encrypted,status) VALUES(?,'statistics isolated',?,?,1)", account, encrypted, encrypted).Error; e != nil {
		t.Fatal(e)
	}
	if e = db.Exec("INSERT INTO system.cdnsell_domains(id,user_id,account_id,domain,business_type,origin_type,origin_host,origin_protocol,service_no,ownership_status) VALUES(?,701,?,?,'web','ip','192.0.2.1','http','isolated','verified')", id, account, fmt.Sprintf("statistics-%d.example.invalid", id)).Error; e != nil {
		t.Fatal(e)
	}
	var enabled bool
	db.Table(settingsTable).Select("enabled").Where("id=1").Scan(&enabled)
	defer db.Exec("UPDATE system.cdnsell_settings SET enabled=? WHERE id=1", enabled)
	db.Exec("UPDATE system.cdnsell_settings SET enabled=1 WHERE id=1")
	q := provider.StatisticsQuery{Kind: "traffic", Start: time.Now().Add(-time.Hour).Truncate(time.Minute), End: time.Now().Add(-time.Minute).Truncate(time.Minute), Interval: 5}
	for i := 0; i < 2; i++ {
		if _, e = s.Statistics(ctx, 701, false, []int64{id}, q); e != nil {
			t.Fatal(e)
		}
	}
	if _, e = s.CloudCertificates(ctx, 701, false, id); e != nil {
		t.Fatal(e)
	}
	if _, e = s.CloudCertificates(ctx, 702, false, id); !errors.Is(e, ErrForbidden) {
		t.Fatal("certificate tenant isolation failed", e)
	}
	if cloud.certCalls != 1 {
		t.Fatal("unauthorized certificate query reached cloud")
	}
	if cloud.calls != 1 {
		t.Fatal("cache not used", cloud.calls)
	}
	if _, e = s.Statistics(ctx, 702, false, []int64{id}, q); !errors.Is(e, ErrForbidden) {
		t.Fatal("cached tenant isolation failed", e)
	}
	db.Table(domainsTable).Where("id=?", id).Update("ownership_status", "unverified")
	if _, e = s.Statistics(ctx, 701, false, []int64{id}, q); e == nil {
		t.Fatal("revoked ownership returned cached data")
	}
	db.Table(domainsTable).Where("id=?", id).Update("ownership_status", "verified")
	db.Table(accountsTable).Where("id=?", account).Update("status", false)
	if _, e = s.Statistics(ctx, 701, false, []int64{id}, q); e == nil {
		t.Fatal("disabled account returned cache")
	}
	if cloud.calls != 1 {
		t.Fatal("unauthorized calls reached cloud")
	}
	if _, e = s.CloudCertificates(ctx, 701, false, id); e == nil {
		t.Fatal("disabled account certificate query permitted")
	}
	if cloud.certCalls != 1 {
		t.Fatal("disabled account reached cloud")
	}
	t.Log("certificates tenant scope and disabled account reject cloud access; cache hit rechecks tenant, verified ownership and enabled account; rejected requests do not call provider")
}
