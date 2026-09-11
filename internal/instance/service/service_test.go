package instance

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	dom "ddyidc/internal/instance/domain"
	"ddyidc/internal/remoteconsole"
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

type ownershipRepo struct {
	host             *dom.Host
	event            *dom.Event
	finishedResponse string
	finishedError    string
}

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

func (r *ownershipRepo) CreateEvent(_ context.Context, event *dom.Event) error {
	event.ID = 1
	r.event = event
	return nil
}
func (r *ownershipRepo) FinishEvent(_ context.Context, _ int64, _ uint8, _ *uint8, response, failure string) error {
	r.finishedResponse = response
	r.finishedError = failure
	return nil
}
func (r *ownershipRepo) ListEvents(context.Context, int64, int) ([]*dom.Event, error) {
	return nil, nil
}
func (r *ownershipRepo) IsMarketplaceLocked(context.Context, int64) (bool, error) { return false, nil }

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

type remoteInstanceProvider struct {
	upprovider.UpstreamProvider
	contracts.InstanceProvider
	result *contracts.InstanceActionResult
}

func (p *remoteInstanceProvider) ExecuteInstanceAction(int64, contracts.InstanceActionInput) (*contracts.InstanceActionResult, error) {
	return p.result, nil
}

type remoteResolver struct{ provider *remoteInstanceProvider }

func (r *remoteResolver) ProviderFor(string) (upprovider.UpstreamProvider, error) {
	return r.provider, nil
}
func (r *remoteResolver) GetOrderPageByLocalProduct(string) (*contracts.OrderPage, error) {
	return nil, nil
}

func TestRemoteActionResponseAndEventNeverExposeUpstreamMaterial(t *testing.T) {
	supplierID := int64(9)
	repository := &ownershipRepo{host: &dom.Host{
		ID: 123, CustomerID: 456, UpstreamProviderID: &supplierID, UpstreamInstanceID: "52483",
	}}
	resolver := &remoteResolver{provider: &remoteInstanceProvider{result: &contracts.InstanceActionResult{
		Status: "running", Message: "https://secret-upstream.example/accepted",
		RemoteEndpoint: "wss://secret-upstream.example/websockify?ticket=private", RemotePassword: "vnc-secret",
	}}}
	service := NewService(repository, resolver)
	registry := remoteconsole.NewRegistry()
	if err := registry.Register(remoteconsole.NewNoVNCProvider(service)); err != nil {
		t.Fatal(err)
	}
	if err := registry.Use("novnc"); err != nil {
		t.Fatal(err)
	}
	service.SetRemoteConsole(registry)

	result, err := service.Action(context.Background(), 456, 123, ActionInput{Action: "remote"})
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	for _, text := range []string{string(encoded), repository.finishedResponse, repository.finishedError} {
		for _, forbidden := range []string{"secret-upstream.example", "websockify", "vnc-secret", "private", "remoteUrl"} {
			if strings.Contains(text, forbidden) {
				t.Fatalf("remote response or event leaked %q: %s", forbidden, text)
			}
		}
	}
	if result.Remote == nil || result.Remote.Type != "novnc" || !strings.HasPrefix(result.Remote.URL, "/console/instance/123/vnc?") {
		t.Fatalf("unexpected remote response: %#v", result)
	}
	if repository.event == nil || repository.event.UpstreamRequest != `{"action":"remote"}` {
		t.Fatalf("unsafe or missing remote event: %#v", repository.event)
	}
	if _, err := service.ClaimRemoteSession(context.Background(), 999, 123, result.Remote.Token); !errors.Is(err, ErrForbidden) {
		t.Fatalf("foreign claim error = %v", err)
	}
}
