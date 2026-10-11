package cdnsell

import (
	"context"
	"ddyidc/internal/cdnsell/provider"
	"ddyidc/internal/shared/testenv"
	"errors"
	"testing"
	"time"
)

type removalCloud struct {
	provider.Provider
	domain         string
	reads, removes int
	confirmError   bool
}

func (c *removalCloud) GetDomain(context.Context, provider.Account, string) (map[string]any, error) {
	c.reads++
	if c.confirmError && c.reads > 1 {
		return nil, errors.New("isolated network uncertainty")
	}
	if c.reads >= 3 {
		return nil, provider.ErrDomainNotFound
	}
	return map[string]any{"domain": c.domain, "status": "offline"}, nil
}
func (c *removalCloud) RemoveDomain(context.Context, provider.Account, string) error {
	c.removes++
	return nil
}
func TestDomainRemovalConfirmationMySQL(t *testing.T) {
	db := testenv.MySQL(t)
	ctx := context.Background()
	now := time.Now()
	s := NewService(db, []byte("0123456789abcdef0123456789abcdef"), nil)
	s.now = func() time.Time { return now }
	account := UpstreamAccount{Name: "isolated removal account", AccessKey: "isolated-ak", SecretKey: "isolated-sk", Status: true}
	if err := s.SaveAccount(ctx, &account); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		user, domain := int64(940001+i), int64(940101+i)
		cdnFixture(t, db, user, domain, now)
		if err := db.Table(domainsTable).Where("id=?", domain).Updates(map[string]any{"account_id": account.ID, "service_no": "isolated-removal", "upstream_status": "active", "enabled": true}).Error; err != nil {
			t.Fatal(err)
		}
		if _, err := s.DeleteDomain(ctx, user, domain); err == nil {
			t.Fatal("online domain removal accepted")
		}
		db.Table(domainsTable).Where("id=?", domain).Update("enabled", false)
		d, err := s.domain(ctx, domain)
		if err != nil {
			t.Fatal(err)
		}
		cloud := &removalCloud{domain: d.Domain, confirmError: i == 1}
		s.cloud = cloud
		task, err := s.DeleteDomain(ctx, user, domain)
		if err != nil {
			t.Fatal(err)
		}
		if err = s.processDomainTask(ctx); err != nil {
			t.Fatal(err)
		}
		d, _ = s.domain(ctx, domain)
		if d.Deleted || d.ServiceNo == "" || cloud.removes != 1 {
			t.Fatal("accepted deletion lost local association or duplicated request")
		}
		now = now.Add(20 * time.Second)
		if err = s.processDomainTask(ctx); err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			d, _ = s.domain(ctx, domain)
			if d.Deleted {
				t.Fatal("still-present domain hidden")
			}
			now = now.Add(20 * time.Second)
			if err = s.processDomainTask(ctx); err != nil {
				t.Fatal(err)
			}
		} else {
			d, _ = s.domain(ctx, domain)
			if d.Deleted || d.ServiceNo == "" {
				t.Fatal("uncertain result cleared association")
			}
			if _, err = s.DeleteDomain(ctx, user, domain); err == nil {
				t.Fatal("uncertain removal can be resubmitted")
			}
			cloud.confirmError = false
			cloud.reads = 2
			if err = s.syncDomainFromCloud(ctx, d, provider.Account{}); err != nil {
				t.Fatal(err)
			}
		}
		d, _ = s.domain(ctx, domain)
		var final DomainTaskRecord
		db.Table(tasksTable).Where("id=?", task.ID).Take(&final)
		if !d.Deleted || d.ServiceNo != "" || final.Status != "success" || cloud.removes != 1 {
			t.Fatal("confirmed deletion not finalized exactly once", d.Deleted, final.Status, cloud.removes)
		}
	}
	t.Log("online deletion blocked; accepted deletion remains visible; confirmation performs reads only; uncertainty preserves association and cannot resend; verified absence finalizes once")
}
