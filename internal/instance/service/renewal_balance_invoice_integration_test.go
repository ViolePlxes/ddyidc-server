package instance

import (
	"context"
	balance "ddyidc/internal/billing/balance"
	dom "ddyidc/internal/instance/domain"
	repo "ddyidc/internal/instance/repository"
	order "ddyidc/internal/order/domain"
	"ddyidc/internal/shared/snowflake"
	"ddyidc/internal/upstream/provider/contracts"
	"errors"
	"fmt"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

// invoicePreparingProvider mirrors the real zjmf provider: it creates the
// supplier renewal invoice before the customer is charged, which moves the
// renewal to invoiced_verified. The gate fixture alone never implemented this,
// so the payable-state regression stayed invisible to tests.
type invoicePreparingProvider struct {
	*gateRenewalFixture
	mu      sync.Mutex
	calls   int
	invoice string
}

func (p *invoicePreparingProvider) WithInstanceContext(context.Context) contracts.InstanceProvider {
	return p
}

func (p *invoicePreparingProvider) PrepareRenewalInvoice(int64, string, int64) (string, string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls++
	return p.invoice, p.cost, nil
}

func (p *invoicePreparingProvider) prepared() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.calls
}

func openRenewalBalanceDB(t *testing.T) *gorm.DB {
	t.Helper()
	socket := os.Getenv("FOUNDATION_MYSQL_SOCKET")
	if socket == "" {
		t.Skip("isolated MySQL required")
	}
	if !strings.HasPrefix(socket, "/tmp/ddyidc-foundation-") || !strings.HasSuffix(socket, "/mysql.sock") {
		t.Fatal("unsafe socket")
	}
	db, err := gorm.Open(mysql.Open("root@unix("+socket+")/?parseTime=true"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	pool, _ := db.DB()
	t.Cleanup(func() { _ = pool.Close() })
	if err := snowflake.Configure(context.Background(), pool); err != nil {
		t.Fatal(err)
	}
	return db
}

type renewalBalanceScenario struct {
	customer, supplierID, productID, hostID, orderID, renewalID int64
	host                                                         dom.Host
	renewal                                                      Renewal
	provider                                                     *invoicePreparingProvider
	service                                                      *Service
}

func seedRenewalBalanceScenario(t *testing.T, db *gorm.DB, base int64, invoice string) *renewalBalanceScenario {
	t.Helper()
	s := &renewalBalanceScenario{
		customer: base + 1, supplierID: base + 2, productID: base + 3,
		hostID: base + 4, orderID: base + 5, renewalID: base + 6,
	}
	username := fmt.Sprintf("isolated-renewal-%d", s.customer)
	if err := db.Exec("INSERT INTO customer.customers(id,username,password_hash) VALUES(?,?,'!login-disabled')", s.customer, username).Error; err != nil {
		t.Fatal(err)
	}
	seedGateSupplier(t, db, s.supplierID, s.productID)
	s.host = dom.Host{UpstreamProviderID: &s.supplierID, ID: s.hostID, InstanceNo: fmt.Sprintf("RENEW-%d", s.hostID), CustomerID: s.customer, ProductID: s.productID, ModuleType: "cloud", ConfigSnapshot: "{}", Status: 3, UpstreamInstanceID: fmt.Sprint(base + 7), OwnershipVersion: 1}
	if err := db.Create(&s.host).Error; err != nil {
		t.Fatal(err)
	}
	o := order.Order{ID: s.orderID, OrderNo: fmt.Sprintf("REN-%d", s.orderID), CustomerID: s.customer, TotalAmount: 100, PayAmount: 100, Status: 1}
	if err := db.Create(&o).Error; err != nil {
		t.Fatal(err)
	}
	item := order.OrderItem{OrderID: o.ID, ProductID: s.host.ProductID, ProductName: "renewal fixture", ModuleType: "instance_renewal", ConfigSnapshot: "{}", Qty: 1, UnitPrice: 100, Subtotal: 100, InstanceID: &s.host.ID}
	if err := db.Create(&item).Error; err != nil {
		t.Fatal(err)
	}
	expiry := time.Now().Truncate(time.Second)
	s.renewal = Renewal{SupplierID: s.supplierID, ID: s.renewalID, OrderID: s.orderID, InstanceID: s.hostID, CustomerID: s.customer, OwnershipVersion: 1, HostID: s.host.UpstreamInstanceID, Cycle: "monthly", Duration: 1, Amount: 100, Cost: 100, BaseExpiry: expiry, State: "unpaid"}
	if err := db.Create(&s.renewal).Error; err != nil {
		t.Fatal(err)
	}
	s.provider = &invoicePreparingProvider{gateRenewalFixture: &gateRenewalFixture{retail: "1.00", cost: "1.00", duration: 1, expiry: expiry}, invoice: invoice}
	s.service = NewService(repo.NewRepository(db), securityResolver{provider: s.provider})
	s.service.db = db
	return s
}

func (s *renewalBalanceScenario) fund(t *testing.T, db *gorm.DB, amount, key string) {
	t.Helper()
	if _, err := balance.New(db).Change(context.Background(), balance.ChangeRequest{CustomerID: s.customer, Type: "manual_credit", Amount: amount, RefType: "customer", RefID: s.customer, Channel: "admin_manual", RequestID: key, AdminID: 1, AdminName: "isolated", OperatorIP: "127.0.0.1", ClientNotes: "isolated renewal balance funding", Notes: "isolated integration funds"}); err != nil {
		t.Fatal(err)
	}
}

func (s *renewalBalanceScenario) assertDebits(t *testing.T, db *gorm.DB, want int64) {
	t.Helper()
	var debits int64
	if err := db.Table("billing.transactions").Where("related_type='instance_renewal' AND related_id=? AND type='instance_renewal_pay'", s.renewalID).Count(&debits).Error; err != nil {
		t.Fatal(err)
	}
	if debits != want {
		t.Fatalf("renewal debits=%d want=%d", debits, want)
	}
}

func (s *renewalBalanceScenario) orderStatus(t *testing.T, db *gorm.DB) int {
	t.Helper()
	var status int
	if err := db.Raw("SELECT status FROM `order`.orders WHERE id=?", s.orderID).Scan(&status).Error; err != nil {
		t.Fatal(err)
	}
	return status
}

// TestRenewalBalanceAfterInvoicePreparationMySQL covers the production path
// where the supplier invoice exists before the debit: the renewal row is in
// invoiced_verified and the balance layer must still accept the payment.
func TestRenewalBalanceAfterInvoicePreparationMySQL(t *testing.T) {
	db := openRenewalBalanceDB(t)
	ctx := context.Background()
	s := seedRenewalBalanceScenario(t, db, 900000, "SUP-900006")
	s.fund(t, db, "10.00", "isolated-prepared-fund")
	if err := s.service.PayRenewalBalance(ctx, s.customer, s.hostID, s.orderID, "127.0.0.1"); err != nil {
		t.Fatalf("balance payment after supplier invoice preparation: %v", err)
	}
	var stored Renewal
	if err := db.Where("id=?", s.renewalID).Take(&stored).Error; err != nil {
		t.Fatal(err)
	}
	if stored.State != "invoiced_verified" || stored.InvoiceID != "SUP-900006" {
		t.Fatalf("renewal state=%q invoice=%q", stored.State, stored.InvoiceID)
	}
	if got := s.provider.prepared(); got != 1 {
		t.Fatalf("supplier invoice prepared %d times", got)
	}
	s.assertDebits(t, db, 1)
	var jobs int64
	if err := db.Table("system.durable_tasks").Where("task_key=? AND status='pending'", fmt.Sprintf("renewal-balance:%d", s.renewalID)).Count(&jobs).Error; err != nil {
		t.Fatal(err)
	}
	if jobs != 1 {
		t.Fatalf("durable renewal tasks=%d", jobs)
	}
	if status := s.orderStatus(t, db); status != 2 {
		t.Fatalf("order status=%d", status)
	}
	t.Log("prepared supplier invoice + balance debit: one debit, one durable task, order paid, renewal keeps invoiced_verified for the worker")
}

// TestRenewalBalanceAfterInvoicePreparationRetryMySQL covers the state left
// behind by a failed debit: the invoice is prepared once and a later retry must
// charge exactly once without creating a second supplier invoice.
func TestRenewalBalanceAfterInvoicePreparationRetryMySQL(t *testing.T) {
	db := openRenewalBalanceDB(t)
	ctx := context.Background()
	s := seedRenewalBalanceScenario(t, db, 910000, "SUP-910006")
	err := s.service.PayRenewalBalance(ctx, s.customer, s.hostID, s.orderID, "127.0.0.1")
	if err == nil || !errors.Is(err, balance.ErrInsufficient) {
		t.Fatalf("first attempt error=%v", err)
	}
	var failed Renewal
	if err := db.Where("id=?", s.renewalID).Take(&failed).Error; err != nil {
		t.Fatal(err)
	}
	if failed.State != "invoiced_verified" || failed.InvoiceID != "SUP-910006" {
		t.Fatalf("after failed debit state=%q invoice=%q", failed.State, failed.InvoiceID)
	}
	s.assertDebits(t, db, 0)
	if status := s.orderStatus(t, db); status != 1 {
		t.Fatalf("failed debit changed order status=%d", status)
	}
	s.fund(t, db, "10.00", "isolated-prepared-retry-fund")
	if err := s.service.PayRenewalBalance(ctx, s.customer, s.hostID, s.orderID, "127.0.0.1"); err != nil {
		t.Fatalf("retry after funding: %v", err)
	}
	if got := s.provider.prepared(); got != 1 {
		t.Fatalf("retry created %d supplier invoices", got)
	}
	s.assertDebits(t, db, 1)
	if status := s.orderStatus(t, db); status != 2 {
		t.Fatalf("retry order status=%d", status)
	}
	t.Log("failed debit keeps the prepared invoice; a funded retry charges once and reuses the same supplier invoice")
}

// TestRenewalBalanceLayerInvoiceGuardMySQL pins the ledger guard itself: an
// invoiced_verified renewal without an invoice id is never charged, while the
// same row with a prepared invoice is accepted.
func TestRenewalBalanceLayerInvoiceGuardMySQL(t *testing.T) {
	db := openRenewalBalanceDB(t)
	ctx := context.Background()
	s := seedRenewalBalanceScenario(t, db, 920000, "SUP-920006")
	s.fund(t, db, "10.00", "isolated-unprepared-fund")
	change := func(request string) error {
		_, err := balance.New(db).Change(ctx, balance.ChangeRequest{
			CustomerID: s.customer, Type: "instance_renewal_pay", Amount: "-1.00",
			RefType: "instance_renewal", RefID: s.renewalID, Channel: "instance_renewal",
			RequestID: request, Notes: "isolated renewal balance probe",
		})
		return err
	}
	if err := db.Model(&Renewal{}).Where("id=?", s.renewalID).Updates(map[string]any{"state": "invoiced_verified", "invoice_id": ""}).Error; err != nil {
		t.Fatal(err)
	}
	if err := change("isolated-guard:missing-invoice"); !errors.Is(err, balance.ErrBusiness) {
		t.Fatalf("invoiced_verified without invoice id error=%v", err)
	}
	s.assertDebits(t, db, 0)
	if status := s.orderStatus(t, db); status != 1 {
		t.Fatalf("rejected charge changed order status=%d", status)
	}
	if err := db.Model(&Renewal{}).Where("id=?", s.renewalID).Update("invoice_id", "SUP-920006").Error; err != nil {
		t.Fatal(err)
	}
	if err := change("isolated-guard:prepared-invoice"); err != nil {
		t.Fatalf("invoiced_verified with invoice id charge: %v", err)
	}
	s.assertDebits(t, db, 1)
	t.Log("ledger guard: invoiced_verified without an invoice id rejected, with an invoice id accepted")
}
