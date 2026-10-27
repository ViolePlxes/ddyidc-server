package instance

import (
	"context"
	dom "ddyidc/internal/instance/domain"
	"ddyidc/internal/upstream/provider/contracts"
	"ddyidc/pkg/outbound"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
	"os"
	"strings"
	"testing"
)

func TestArchivedFinalizedDetailMySQL(t *testing.T) {
	socket := os.Getenv("RETIRED_HISTORY_SOCKET")
	if socket == "" {
		t.Skip("isolated frozen migration database required")
	}
	if !strings.HasPrefix(socket, "/tmp/ddoidc-legacy-retirement-") || !strings.HasSuffix(socket, "/mysql.sock") {
		t.Fatal("unsafe socket")
	}
	db, err := gorm.Open(mysql.Open("root@unix("+socket+")/?parseTime=true"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	pool, _ := db.DB()
	defer pool.Close()
	var isolated int
	if err := db.Raw("SELECT @@skip_networking").Scan(&isolated).Error; err != nil || isolated != 1 {
		t.Fatal("unsafe target")
	}
	for _, ddl := range []string{
		"CREATE TABLE IF NOT EXISTS instance.instance_production_locks(instance_id BIGINT PRIMARY KEY,enabled BOOLEAN NOT NULL,updated_at DATETIME NOT NULL)",
		"CREATE TABLE IF NOT EXISTS instance.instance_renewal_settings(instance_id BIGINT PRIMARY KEY,customer_id BIGINT NOT NULL,enabled BOOLEAN NOT NULL,updated_at DATETIME NOT NULL)",
	} {
		if err := db.Exec(ddl).Error; err != nil {
			t.Fatal(err)
		}
	}
	tx := db.Begin()
	defer tx.Rollback()
	var id int64
	if err := tx.Table("system.instance_migration_metadata").Select("instance_id").Order("instance_id").Limit(1).Scan(&id).Error; err != nil || id == 0 {
		t.Fatal("frozen fixture required")
	}
	providerID := int64(7)
	r := &readDetailRepo{ownershipRepo: ownershipRepo{host: &dom.Host{ID: id, CustomerID: 21, ModuleType: "legacy_snapshot", UpstreamProviderID: &providerID, UpstreamInstanceID: "31", ConfigSnapshot: `{"legacyOrderNo":"v10-order","firstPayment":"10.00","renewalAmount":"12.00"}`}}}
	p := &readDetailProvider{detail: &contracts.InstanceDetail{ID: "31", FirstPayment: "1.00", PowerStatus: "running", Capabilities: contracts.InstanceCapabilities{Start: true}}}
	s := NewService(r, &readDetailResolver{p: p})
	s.SetManagement(tx)
	if err := outbound.Configure(outbound.Config{}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = outbound.Configure(outbound.Config{}) })
	if err := tx.Table("system.instance_migration_metadata").Where("instance_id=?", id).Update("read_only", true).Error; err != nil {
		t.Fatal(err)
	}
	v, err := s.Detail(context.Background(), 21, id)
	if err != nil || !v.ReadOnly || p.calls != 0 {
		t.Fatalf("unfinalized snapshot must stay read only: err=%v", err)
	}
	if err := tx.Table("system.instance_migration_metadata").Where("instance_id=?", id).Update("read_only", false).Error; err != nil {
		t.Fatal(err)
	}
	v, err = s.Detail(context.Background(), 21, id)
	if err != nil || v.ReadOnly || v.InfoSource != "upstream" || v.FirstPayment != "10.00" || !v.Capabilities.Start || p.calls != 1 {
		t.Fatal("finalized instance failed live read or changed retail history")
	}
	if err := outbound.Configure(outbound.Config{DetailReadOnly: true, Hosts: []string{"supplier.example"}}); err != nil {
		t.Fatal(err)
	}
	v, err = s.Detail(context.Background(), 21, id)
	if err != nil || !v.ReadOnly || v.Capabilities != (contracts.InstanceCapabilities{}) {
		t.Fatal("deployment fence must override finalized instance")
	}
}
