package customer

import (
	"context"
	"net/http/httptest"
	"testing"

	domain "ddyidc/internal/customer/domain"
	"gorm.io/gorm"
)

type authRepositoryStub struct {
	phone        string
	customer     *domain.Customer
	phoneLookups int
}

func (r *authRepositoryStub) Create(context.Context, *domain.Customer) error { return nil }
func (r *authRepositoryStub) Update(context.Context, *domain.Customer) error { return nil }
func (r *authRepositoryStub) Delete(context.Context, int64) error            { return nil }
func (r *authRepositoryStub) FindByID(context.Context, int64) (*domain.Customer, error) {
	return nil, gorm.ErrRecordNotFound
}
func (r *authRepositoryStub) FindByUsername(context.Context, string) (*domain.Customer, error) {
	return nil, gorm.ErrRecordNotFound
}
func (r *authRepositoryStub) FindByEmail(context.Context, string) (*domain.Customer, error) {
	return nil, gorm.ErrRecordNotFound
}
func (r *authRepositoryStub) FindByPhone(_ context.Context, phone string) (*domain.Customer, error) {
	r.phoneLookups++
	if phone == r.phone {
		return r.customer, nil
	}
	return nil, gorm.ErrRecordNotFound
}
func (r *authRepositoryStub) List(context.Context, int, int, map[string]any) ([]*domain.Customer, int64, error) {
	return nil, 0, nil
}
func (r *authRepositoryStub) Count(context.Context) (int64, error) { return 0, nil }

func TestFindAccountSupportsMainlandMobile(t *testing.T) {
	const mobile = "13800138000"
	want := &domain.Customer{ID: 42, Username: "mobile-user"}
	repository := &authRepositoryStub{phone: mobile, customer: want}
	handler := &authHandler{customers: repository}
	request := httptest.NewRequest("GET", "/api/v1/auth/check-account", nil)

	got, err := handler.findAccount(request, mobile)
	if err != nil {
		t.Fatalf("findAccount returned error: %v", err)
	}
	if got != want {
		t.Fatalf("findAccount returned %#v, want %#v", got, want)
	}
	if repository.phoneLookups != 1 {
		t.Fatalf("FindByPhone called %d times, want 1", repository.phoneLookups)
	}
}

func TestFindAccountSkipsPhoneLookupForUsername(t *testing.T) {
	repository := &authRepositoryStub{}
	handler := &authHandler{customers: repository}
	request := httptest.NewRequest("GET", "/api/v1/auth/check-account", nil)

	_, err := handler.findAccount(request, "missing-user")
	if err != gorm.ErrRecordNotFound {
		t.Fatalf("findAccount returned %v, want record not found", err)
	}
	if repository.phoneLookups != 0 {
		t.Fatalf("FindByPhone called %d times for a username, want 0", repository.phoneLookups)
	}
}
