package instance

import (
	"context"
	dom "ddyidc/internal/instance/domain"
	repo "ddyidc/internal/instance/repository"
	"ddyidc/internal/shared/testenv"
	"errors"
	"fmt"
	"strings"
	"testing"
)

type lockEmailTestVerifier struct{ codes map[string]string }

func (v *lockEmailTestVerifier) ConsumeProductionLockCode(_ context.Context, email, scope, code string) error {
	key := email + scope
	if v.codes[key] != code || code == "" {
		return errors.New("验证码错误或已使用")
	}
	delete(v.codes, key)
	return nil
}
func issueLockTestCode(t *testing.T, s *Service, customer, id int64, enabled bool, email string) ProductionLockEmailInput {
	t.Helper()
	target, scope, e := s.ProductionLockEmailTarget(context.Background(), customer, id, enabled, email)
	if e != nil {
		t.Fatal(e)
	}
	v, ok := s.lockEmailVerifier.(*lockEmailTestVerifier)
	if !ok {
		v = &lockEmailTestVerifier{codes: map[string]string{}}
		s.SetProductionLockEmailVerifier(v)
	}
	v.codes[target+scope] = "654321"
	return ProductionLockEmailInput{Email: email, Code: "654321"}
}
func TestProductionLockEmailBindingMySQL(t *testing.T) {
	db := testenv.MySQL(t)
	tx := db.Begin()
	defer tx.Rollback()
	ctx := context.Background()
	id := int64(997303)
	customer := int64(997304)
	if e := tx.Exec("INSERT INTO customer.customers(id,username,password_hash,status) VALUES(?,?,'!isolated',1)", customer, fmt.Sprint("lock-email-", customer)).Error; e != nil {
		t.Fatal(e)
	}
	h := dom.Host{ID: id, InstanceNo: "LOCK-EMAIL", CustomerID: customer, ProductID: 1, ModuleType: "cloud", ConfigSnapshot: "{}", Status: 3, OwnershipVersion: 1}
	if e := tx.Create(&h).Error; e != nil {
		t.Fatal(e)
	}
	s := NewService(repo.NewRepository(tx), nil)
	s.db = tx
	if e := s.SetProductionLock(ctx, customer, id, true); e == nil {
		t.Fatal("missing verification accepted")
	}
	proof := issueLockTestCode(t, s, customer, id, true, "bound@example.invalid")
	if e := s.SetProductionLock(ctx, customer, id, false, proof); e == nil {
		t.Fatal("opposite direction accepted")
	}
	if e := s.SetProductionLock(ctx, customer+1, id, true, proof); !errors.Is(e, ErrForbidden) {
		t.Fatal("foreign instance accepted", e)
	}
	// A non-mutating unlock avoids unrelated machine/trading fixture requirements.
	proof = issueLockTestCode(t, s, customer, id, false, "bound@example.invalid")
	if e := s.SetProductionLock(ctx, customer, id, false, proof); e != nil {
		t.Fatal(e)
	}
	var email string
	if e := tx.Table("customer.customers").Select("email").Where("id=?", customer).Scan(&email).Error; e != nil || email != "bound@example.invalid" {
		t.Fatal("email not bound atomically", e)
	}
	if e := s.SetProductionLock(ctx, customer, id, false, proof); e == nil {
		t.Fatal("replayed binding code accepted")
	}
	if _, _, e := s.ProductionLockEmailTarget(ctx, customer, id, false, "other@example.invalid"); e == nil {
		t.Fatal("bound mailbox replaced")
	}
	state, e := s.ProductionLockEmailStatus(ctx, customer, id)
	if e != nil || !state.HasEmail || strings.Contains(state.EmailHint, "bound@") {
		t.Fatal("email status not masked", e)
	}
	// A later state revision invalidates an unconsumed authorization.
	proof = issueLockTestCode(t, s, customer, id, false, "")
	if e := tx.Model(&dom.Host{}).Where("id=?", id).Update("ownership_version", 2).Error; e != nil {
		t.Fatal(e)
	}
	if e := s.SetProductionLock(ctx, customer, id, false, proof); e == nil {
		t.Fatal("changed ownership reused authorization")
	}
}
