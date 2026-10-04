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

func TestFinalizedLegacyDetailMySQL(t *testing.T) {
	socket := os.Getenv("FINAL_CUTOVER_MYSQL_SOCKET")
	if socket == "" {
		t.Skip("isolated frozen migration database required")
	}
	if !strings.HasPrefix(socket, "/tmp/ddyidc-foundation-v10-cutover-target-") || !strings.HasSuffix(socket, "/mysql.sock") {
		t.Fatal("unsafe socket")
	}
	db, err := gorm.Open(mysql.Open("root@unix("+socket+")/?parseTime=true"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	pool, _ := db.DB()
	defer pool.Close()
	tx := db.Begin()
	defer tx.Rollback()
	var id int64
	if err := tx.Table("system.v10_instance_source").Select("instance_id").Order("instance_id").Limit(1).Scan(&id).Error; err != nil || id == 0 {
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
	if err := tx.Table("system.v10_instance_source").Where("instance_id=?", id).Update("read_only", true).Error; err != nil {
		t.Fatal(err)
	}
	v, err := s.Detail(context.Background(), 21, id)
	if err != nil || !v.ReadOnly || p.calls != 0 {
		t.Fatal("unfinalized snapshot must stay read only")
	}
	if err := tx.Table("system.v10_instance_source").Where("instance_id=?", id).Update("read_only", false).Error; err != nil {
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
