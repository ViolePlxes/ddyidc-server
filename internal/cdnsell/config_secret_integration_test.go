package cdnsell

import (
	"context"
	"ddyidc/internal/cdnsell/provider"
	"ddyidc/internal/shared/snowflake"
	"ddyidc/internal/shared/testenv"
	cryptoutil "ddyidc/pkg/crypto"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

type configProofCloud struct {
	provider.Provider
	config map[string]any
	during func()
}

func (f *configProofCloud) GetDomainConfig(context.Context, provider.Account, string, string) (map[string]any, error) {
	if f.during != nil {
		f.during()
	}
	return f.config, nil
}
func TestSecretConfigProofMySQL(t *testing.T) {
	db := testenv.MySQL(t)
	key := []byte("01234567890123456789012345678901")
	service := NewService(db, key, nil)
	ctx := context.Background()
	accountID := snowflake.GenID()
	encrypted, e := cryptoutil.Encrypt("isolatedcredential", key)
	if e != nil {
		t.Fatal(e)
	}
	if e = db.Exec("INSERT INTO system.cdnsell_upstream_accounts(id,name,ak_encrypted,sk_encrypted) VALUES(?,'isolated',?,?)", accountID, encrypted, encrypted).Error; e != nil {
		t.Fatal(e)
	}
	id := snowflake.GenID()
	user := int64(778812)
	if e = db.Exec("INSERT INTO system.cdnsell_domains(id,user_id,account_id,domain,business_type,origin_type,origin_host,origin_protocol,upstream_status,service_no,ownership_status) VALUES(?,?,?,?,'web','ip','1.1.1.1','http','active','isolated','verified')", id, user, accountID, fmt.Sprintf("config-%d.example.com", id)).Error; e != nil {
		t.Fatal(e)
	}
	local := map[string]any{"enabled": true, "authType": "1", "primaryKey": "secret123", "backupKey": "backup456", "expireSeconds": float64(300)}
	task, e := service.SaveDomainConfig(ctx, user, id, "request_auth", "member", local, false)
	if e != nil {
		t.Fatal(e)
	}
	var domain DomainRecord
	db.Table(domainsTable).Where("id=?", id).Take(&domain)
	cloud := map[string]any{"enabled": true, "authType": "1", "primaryKey": "wrong1234", "backupKey": "backup456", "expireSeconds": float64(300)}
	proof := &configProofCloud{config: cloud}
	service.cloud = proof
	if e = service.syncDomainConfig(ctx, domain, "request_auth", task.Version); e == nil {
		t.Fatal("different masked secrets accepted")
	}
	var record DomainConfigRecord
	db.Table(domainConfigsTable).Where("domain_id=? AND config_module='request_auth'", id).Take(&record)
	if record.CloudVersion != 0 || !strings.Contains(string(record.DiffFields), "primarykey") {
		t.Fatal("secret mismatch did not prevent confirmation", record.CloudVersion, string(record.DiffFields))
	}
	serialized, _ := json.Marshal(record)
	if strings.Contains(string(serialized), "secret123") || strings.Contains(string(serialized), "wrong1234") || strings.Contains(string(serialized), record.LocalPayload) {
		t.Fatal("secret material exposed by config JSON")
	}
	proof.config = local
	if e = service.syncDomainConfig(ctx, domain, "request_auth", task.Version); e != nil {
		t.Fatal(e)
	}
	proof.during = func() {
		if e := db.Table(domainConfigsTable).Where("id=?", record.ID).Update("local_version", task.Version+1).Error; e != nil {
			t.Fatal(e)
		}
	}
	if e = service.syncDomainConfig(ctx, domain, "request_auth", task.Version); e == nil {
		t.Fatal("stale config version accepted")
	}
	db.Table(domainConfigsTable).Where("id=?", record.ID).Take(&record)
	if record.CloudVersion != task.Version || record.LocalVersion != task.Version+1 {
		t.Fatal("stale cloud result advanced new version")
	}
	t.Log("different masked secrets fail real comparison; encrypted payload absent from JSON; matching values confirm; newer config blocks stale result")
}
