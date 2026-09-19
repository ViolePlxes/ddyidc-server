package instance

import (
	"context"
	dom "ddyidc/internal/instance/domain"
	repo "ddyidc/internal/instance/repository"
	upp "ddyidc/internal/upstream/provider"
	"ddyidc/internal/upstream/provider/contracts"
	"errors"
	"testing"
)

type importRepo struct {
	ownershipRepo
	saved    int
	products []repo.ImportProduct
}

func (r *importRepo) ImportProducts(context.Context) ([]repo.ImportProduct, error) {
	return r.products, nil
}
func (r *importRepo) SaveImported(_ context.Context, h *dom.Host, _ int64, _ repo.ImportProduct) (*dom.Host, error) {
	r.saved++
	return h, nil
}

type importProvider struct {
	remoteInstanceProvider
	detail *contracts.InstanceDetail
	err    error
}

func (p *importProvider) GetInstanceDetail(int64) (*contracts.InstanceDetail, error) {
	return p.detail, p.err
}

type importResolver struct{ provider *importProvider }

func (r *importResolver) ProviderFor(string) (upp.UpstreamProvider, error) { return r.provider, nil }
func (r *importResolver) GetOrderPageByLocalProduct(string) (*contracts.OrderPage, error) {
	return nil, nil
}
func TestImportRejectsUntrustedIdentityWithoutSaving(t *testing.T) {
	for _, tc := range []struct {
		name   string
		detail *contracts.InstanceDetail
		err    error
	}{
		{name: "missing identity", detail: &contracts.InstanceDetail{ProductID: "353"}},
		{name: "other instance", detail: &contracts.InstanceDetail{ID: "11", ProductID: "353"}},
		{name: "other product", detail: &contracts.InstanceDetail{ID: "10", ProductID: "354"}},
		{name: "upstream failure", err: errors.New("unavailable")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := &importRepo{products: []repo.ImportProduct{{ID: 2, SupplierID: 3, UpstreamProductID: 353}}}
			s := NewService(r, &importResolver{provider: &importProvider{detail: tc.detail, err: tc.err}})
			if _, e := s.Import(WithAdminActor(context.Background(), 7), 4, ImportInput{ProductID: "2", UpstreamInstanceID: "10"}); e == nil {
				t.Fatal("bad upstream response accepted")
			}
			if r.saved != 0 {
				t.Fatal("partial instance persisted")
			}
		})
	}
}
func TestImportRequiresAdmin(t *testing.T) {
	s := NewService(&ownershipRepo{}, nil)
	if _, e := s.Import(context.Background(), 4, ImportInput{ProductID: "2", UpstreamInstanceID: "10"}); !errors.Is(e, ErrForbidden) {
		t.Fatal("customer allowed")
	}
}
