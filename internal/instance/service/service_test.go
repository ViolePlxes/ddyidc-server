package instance

import (
	"context"
	"errors"
	"strings"
	"testing"

	dom "ddyidc/internal/instance/domain"
	upprovider "ddyidc/internal/upstream/provider"
	"ddyidc/internal/upstream/provider/contracts"
)

type imageResolver struct {
	providerCalls int
	page          *contracts.OrderPage
}

func (r *imageResolver) ProviderFor(string) (upprovider.UpstreamProvider, error) {
	r.providerCalls++
	return nil, errors.New("provider must not be called")
}

func (r *imageResolver) GetOrderPageByLocalProduct(string) (*contracts.OrderPage, error) {
	return r.page, nil
}

type ownershipRepo struct{ host *dom.Host }

func (r *ownershipRepo) Create(context.Context, *dom.Host) error             { return nil }
func (r *ownershipRepo) Update(context.Context, *dom.Host) error             { return nil }
func (r *ownershipRepo) Delete(context.Context, int64) error                 { return nil }
func (r *ownershipRepo) FindByID(context.Context, int64) (*dom.Host, error)  { return r.host, nil }
func (r *ownershipRepo) FindByNo(context.Context, string) (*dom.Host, error) { return r.host, nil }
func (r *ownershipRepo) List(context.Context, int, int, map[string]any) ([]*dom.Host, int64, error) {
	return nil, 0, nil
}
func (r *ownershipRepo) EnsureSchema(context.Context) error                        { return nil }
func (r *ownershipRepo) UpdateFields(context.Context, int64, map[string]any) error { return nil }
func (r *ownershipRepo) FindOrderNo(context.Context, int64) (string, error)        { return "", nil }
func (r *ownershipRepo) CreateEvent(context.Context, *dom.Event) error             { return nil }
func (r *ownershipRepo) FinishEvent(context.Context, int64, uint8, *uint8, string, string) error {
	return nil
}
func (r *ownershipRepo) ListEvents(context.Context, int64, int) ([]*dom.Event, error) {
	return nil, nil
}

func TestDetailRejectsAnotherCustomersInstance(t *testing.T) {
	service := NewService(&ownershipRepo{host: &dom.Host{ID: 100, CustomerID: 200}}, nil)
	_, err := service.Detail(context.Background(), 201, 100)
	if !errors.Is(err, ErrForbidden) {
		t.Fatalf("Detail() error = %v, want ErrForbidden", err)
	}
}

func TestActionRejectsAnotherCustomersInstanceBeforeProviderCall(t *testing.T) {
	service := NewService(&ownershipRepo{host: &dom.Host{ID: 100, CustomerID: 200}}, nil)
	_, err := service.Action(context.Background(), 201, 100, ActionInput{Action: "reboot"})
	if !errors.Is(err, ErrForbidden) {
		t.Fatalf("Action() error = %v, want ErrForbidden", err)
	}
}

func TestImagesRejectsAnotherCustomersInstanceBeforeProviderCall(t *testing.T) {
	service := NewService(&ownershipRepo{host: &dom.Host{ID: 100, CustomerID: 200}}, nil)
	_, err := service.Images(context.Background(), 201, 100)
	if !errors.Is(err, ErrForbidden) {
		t.Fatalf("Images() error = %v, want ErrForbidden", err)
	}
}

func TestReinstallRejectsImageOutsideProductWhitelistBeforeProviderCall(t *testing.T) {
	supplierID := int64(1)
	resolver := &imageResolver{page: &contracts.OrderPage{ImageGroups: []contracts.ImageGroup{{
		Name: "Ubuntu", Child: []contracts.ConfigOptionValue{{ID: 13072, Name: "Ubuntu-18.04"}},
	}}}}
	service := NewService(&ownershipRepo{host: &dom.Host{
		ID: 100, CustomerID: 200, ProductID: 300, UpstreamProviderID: &supplierID, UpstreamInstanceID: "52483",
	}}, resolver)
	_, err := service.Action(context.Background(), 200, 100, ActionInput{Action: "reinstall", ImageID: "99999", Port: "22"})
	if err == nil || !strings.Contains(err.Error(), "image_not_found") {
		t.Fatalf("Action() error = %v, want image_not_found", err)
	}
	if resolver.providerCalls != 0 {
		t.Fatalf("provider called %d times before whitelist rejection", resolver.providerCalls)
	}
}
