package instance

import (
	"context"
	"errors"
	"testing"

	dom "ddyidc/internal/instance/domain"
	"ddyidc/internal/upstream/provider/contracts"
	"ddyidc/internal/upstream/provider"
	"ddyidc/pkg/outbound"
)

type readDetailProvider struct {
	remoteInstanceProvider
	detail *contracts.InstanceDetail
	calls int
}
func (p *readDetailProvider) GetInstanceDetail(int64) (*contracts.InstanceDetail,error) { p.calls++; return p.detail,nil }
type readDetailRepo struct { ownershipRepo; writes int }
type readDetailResolver struct { imageResolver; p *readDetailProvider }
func (r *readDetailResolver) ProviderFor(string) (provider.UpstreamProvider,error) { return r.p,nil }
func (r *readDetailRepo) UpdateFields(context.Context,int64,map[string]any) error { r.writes++; return nil }

func TestLegacyLiveDetailIsOwnedMatchedAndReadOnly(t *testing.T) {
	if err := outbound.Configure(outbound.Config{DetailReadOnly:true,Hosts:[]string{"supplier.example"}}); err != nil { t.Fatal(err) }
	t.Cleanup(func(){ _ = outbound.Configure(outbound.Config{}) })
	id := int64(7)
	r := &readDetailRepo{ownershipRepo:ownershipRepo{host:&dom.Host{ID:11,CustomerID:21,ModuleType:"legacy_snapshot",UpstreamProviderID:&id,UpstreamInstanceID:"31",PrimaryIP:"old",ConfigSnapshot:`{"legacyOrderNo":"v10-order","firstPayment":"10.00","renewalAmount":"12.00"}`}}}
	p := &readDetailProvider{detail:&contracts.InstanceDetail{ID:"31",PrimaryIP:"live",PowerStatus:"running",Capabilities:contracts.InstanceCapabilities{Start:true,Stop:true,Reinstall:true}}}
	s := NewService(r,&readDetailResolver{p:p})
	if _, err := s.Detail(context.Background(),22,11); !errors.Is(err,ErrForbidden) || p.calls != 0 { t.Fatal("ownership must precede provider call") }
	view,err := s.Detail(context.Background(),21,11)
	if err != nil || view.PrimaryIP != "live" || view.InfoSource != "upstream" || !view.ReadOnly || view.AutoRenew || view.Capabilities != (contracts.InstanceCapabilities{}) || r.writes != 0 { t.Fatalf("unsafe live result: %+v, %v, writes=%d",view,err,r.writes) }
	p.detail.ID="99"
	view,err=s.Detail(context.Background(),21,11)
	if err != nil || view.PrimaryIP != "old" || view.InfoSource != "snapshot" || view.Warning == "" || r.writes != 0 { t.Fatalf("mismatched resource accepted: %+v",view) }
	before:=p.calls
	if _,err=s.Action(context.Background(),21,11,ActionInput{Action:"reboot"}); !errors.Is(err,ErrUnsupported) || p.calls != before || r.event != nil { t.Fatal("action touched provider or event") }
}
