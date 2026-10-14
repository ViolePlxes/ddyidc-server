package instance

import (
	"context"
	balance "ddyidc/internal/billing/balance"
	dom "ddyidc/internal/instance/domain"
	repo "ddyidc/internal/instance/repository"
	"ddyidc/internal/shared/snowflake"
	"ddyidc/internal/shared/testenv"
	upp "ddyidc/internal/upstream/provider"
	"ddyidc/internal/upstream/provider/contracts"
	"errors"
	"fmt"
	"testing"
	"time"
)

type upgradeTestProvider struct {
	upp.UpstreamProvider
	checkouts, payments int
	ambiguous, paid     bool
}

func (p *upgradeTestProvider) GetInstanceStatus(int64) (string, error) { return "on", nil }
func (p *upgradeTestProvider) ExecuteInstanceAction(int64, contracts.InstanceActionInput) (*contracts.InstanceActionResult, error) {
	return nil, ErrUnsupported
}
func (p *upgradeTestProvider) GetInstanceMonitor(int64, time.Time, time.Time) (*contracts.InstanceMonitor, error) {
	return nil, ErrUnsupported
}
func (p *upgradeTestProvider) GetInstanceDetail(id int64) (*contracts.InstanceDetail, error) {
	product := "247"
	if p.paid {
		product = "256"
	}
	return &contracts.InstanceDetail{ID: fmt.Sprint(id), ProductID: product, Status: "Active"}, nil
}
func (p *upgradeTestProvider) UpgradePlans(int64) ([]contracts.UpgradePlan, error) {
	return []contracts.UpgradePlan{{ProductID: "256", Name: "4C4G", Cycles: []contracts.UpgradeCycle{{ID: "monthly", Label: "月付"}}}}, nil
}
func (p *upgradeTestProvider) QuoteUpgrade(int64, string, string) (contracts.UpgradeQuote, error) {
	return contracts.UpgradeQuote{Cost: "3.00", OriginalAmount: "5.00"}, nil
}
func (p *upgradeTestProvider) CreateUpgradeInvoice(int64, string, string, string) (string, string, error) {
	p.checkouts++
	if p.ambiguous {
		return "501", "", errors.New("ambiguous amount")
	}
	return "501", "3.00", nil
}
func (p *upgradeTestProvider) PayUpgradeInvoice(string) error {
	p.payments++
	p.paid = true
	return nil
}

func TestUpgradePaidDeliveryAndUnknownOutcomeMySQL(t *testing.T) {
	db := testenv.MySQL(t).Begin()
	defer db.Rollback()
	ctx := context.Background()
	must := func(e error) {
		t.Helper()
		if e != nil {
			t.Fatal(e)
		}
	}
	for _, ambiguous := range []bool{false, true} {
		customer, host, orderID, item := snowflake.GenID(), snowflake.GenID(), snowflake.GenID(), snowflake.GenID()
		supplier := int64(1)
		now := time.Now().UTC().Truncate(time.Millisecond)
		must(db.Table("customer.customers").Create(map[string]any{"id": customer, "username": fmt.Sprintf("up-%d", customer), "password_hash": "isolated", "status": 1}).Error)
		must(db.Table("`order`.orders").Create(map[string]any{"id": orderID, "order_no": fmt.Sprintf("UPTEST%d", orderID), "customer_id": customer, "status": 1, "pay_amount": 500, "total_amount": 500, "created_at": now, "updated_at": now}).Error)
		must(db.Table("`order`.order_items").Create(map[string]any{"id": item, "order_id": orderID, "product_id": 2, "product_name": "4C4G", "module_type": "instance_upgrade", "qty": 1, "unit_price": 500, "subtotal": 500, "config_snapshot": "{}"}).Error)
		h := dom.Host{ID: host, InstanceNo: fmt.Sprintf("UPHOST%d", host), CustomerID: customer, OrderID: orderID - 1, OrderItemID: item - 1, ProductID: 1, ProductName: "2C2G", ModuleType: "dcimcloud", ConfigSnapshot: "{}", Status: 3, OwnershipVersion: 1, UpstreamProviderID: &supplier, UpstreamInstanceID: "55101", CreatedAt: now, UpdatedAt: now}
		must(db.Create(&h).Error)
		row := Upgrade{ID: snowflake.GenID(), OrderID: orderID, InstanceID: host, CustomerID: customer, OwnershipVersion: 1, SupplierID: supplier, HostID: "55101", SourceProductID: 1, TargetProductID: 2, RemoteProductID: "256", TargetName: "4C4G", Cycle: "monthly", Amount: balance.Money(500), Cost: balance.Money(300), RequestID: fmt.Sprintf("isolated-up-%d", orderID), State: "unpaid", ExpiresAt: now.Add(time.Minute), CreatedAt: now, UpdatedAt: now}
		must(db.Create(&row).Error)
		p := &upgradeTestProvider{ambiguous: ambiguous}
		s := NewService(repo.NewRepository(db), securityResolver{p})
		s.SetManagement(db)
		_, e := balance.New(db).Change(ctx, balance.ChangeRequest{CustomerID: customer, Type: "manual_credit", Amount: "10.00", RefType: "customer", RefID: customer, Channel: "admin_manual", RequestID: fmt.Sprintf("fixture-credit-%d", customer), AdminID: 1, AdminName: "integration", OperatorIP: "127.0.0.1", ClientNotes: "测试客户可见原因", Notes: "isolated balance fixture"})
		must(e)
		must(s.PayUpgradeBalance(ctx, customer, host, orderID, "127.0.0.1"))
		must(s.PayUpgradeBalance(ctx, customer, host, orderID, "127.0.0.1"))
		var debits int64
		must(db.Table("billing.transactions").Where("customer_id=? AND type='instance_upgrade_pay'", customer).Count(&debits).Error)
		if debits != 1 {
			t.Fatal("upgrade balance payment duplicated", debits)
		}

		if ok, e := s.ProvisionUpgrade(ctx, customer+1, orderID, "balance"); !ok || e == nil || p.checkouts != 0 {
			t.Fatal("wrong customer reached upgrade supplier")
		}
		ok, e := s.ProvisionUpgrade(ctx, customer, orderID, "balance")
		if !ok {
			t.Fatal("not recognized")
		}
		if ambiguous {
			if e == nil || p.payments != 0 {
				t.Fatal("unverified invoice was paid")
			}
		} else {
			must(e)
			must(db.First(&h, host).Error)
			if h.ProductID != 2 || p.payments != 1 {
				t.Fatal("paid upgrade not delivered")
			}
		}
		_, _ = s.ProvisionUpgrade(ctx, customer, orderID, "balance")
		if p.checkouts != 1 || p.payments > 1 {
			t.Fatal("delivery retry repeated checkout/payment")
		}
		must(db.First(&row, row.ID).Error)
		if ambiguous && row.State != "uncertain" || !ambiguous && row.State != "completed" {
			t.Fatal("incorrect durable state", row.State)
		}
	}
}
