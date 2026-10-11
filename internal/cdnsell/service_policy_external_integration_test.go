package cdnsell

import (
	"context"
	"ddyidc/internal/cdnsell/provider"
	"ddyidc/internal/shared/testenv"
	cryptoutil "ddyidc/pkg/crypto"
	"testing"
	"time"
)

type externalPauseCloud struct {
	provider.Provider
	writes int
}

func (f *externalPauseCloud) GetDomain(context.Context, provider.Account, string) (map[string]any, error) {
	return map[string]any{"status": "offline"}, nil
}
func (f *externalPauseCloud) SetDomainEnabled(context.Context, provider.Account, string, bool) error {
	f.writes++
	return nil
}
func TestExternalManualPauseMySQL(t *testing.T) {
	db := testenv.MySQL(t)
	ctx := context.Background()
	now := time.Now()
	key := []byte("01234567890123456789012345678901")
	s := NewService(db, key, nil)
	s.now = func() time.Time { return now }
	cloud := &externalPauseCloud{}
	s.cloud = cloud
	cdnFixture(t, db, 896001, 896101, now)
	encrypted, e := cryptoutil.Encrypt("isolated", key)
	if e != nil {
		t.Fatal(e)
	}
	if e = db.Exec("INSERT INTO system.cdnsell_upstream_accounts(id,name,ak_encrypted,sk_encrypted) VALUES(896001,'external-pause-test',?,?)", encrypted, encrypted).Error; e != nil {
		t.Fatal(e)
	}
	for _, sql := range []string{
		"UPDATE system.cdnsell_domains SET account_id=896001,service_no='isolated',upstream_status='active',enabled=1 WHERE id=896101",
		"UPDATE system.cdnsell_batches SET remaining_bytes=0,status='exhausted' WHERE id=896101",
		"UPDATE system.cdnsell_settings SET enabled=1,auto_suspend_enabled=1,auto_restore_enabled=1 WHERE id=1",
	} {
		if e = db.Exec(sql).Error; e != nil {
			t.Fatal(e)
		}
	}
	defer db.Exec("UPDATE system.cdnsell_settings SET enabled=0,auto_suspend_enabled=0,auto_restore_enabled=0 WHERE id=1")
	if e = s.planServicePolicy(ctx, 896101, true, true); e != nil {
		t.Fatal(e)
	}
	var task DomainTaskRecord
	if e = db.Table(tasksTable).Where("biz_id=896101").Take(&task).Error; e != nil {
		t.Fatal(e)
	}
	if e = s.executeAutomaticState(ctx, task); e != nil {
		t.Fatal(e)
	}
	var d DomainRecord
	if e = db.Table(domainsTable).Where("id=896101").Take(&d).Error; e != nil {
		t.Fatal(e)
	}
	if cloud.writes != 0 || d.Enabled || !d.ManualDisabled || d.AutoSuspended || d.AutoTaskID != 0 {
		t.Fatal("external manual pause acquired automatic ownership", cloud.writes, d)
	}
	db.Table(tasksTable).Where("id=?", task.ID).Update("status", "success")
	db.Exec("UPDATE system.cdnsell_batches SET remaining_bytes=1000,status='running' WHERE id=896101")
	if e = s.planServicePolicy(ctx, 896101, true, true); e != nil {
		t.Fatal(e)
	}
	var count int64
	db.Table(tasksTable).Where("biz_id=896101").Count(&count)
	if count != 1 {
		t.Fatal("external pause automatically restored")
	}
	t.Log("cloud manual pause preserved without write; refilled credit cannot auto restore")
}
