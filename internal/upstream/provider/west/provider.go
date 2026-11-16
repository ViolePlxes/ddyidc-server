package west

import (
	"errors"

	"ddyidc/internal/upstream/provider/contracts"
)

// Every operation the vendor manual does not document for elastic cloud servers
// is refused here. A refusal reaches the operator as a Chinese message instead
// of a silent success, and the module must never advertise the capability.

var errRenewalFlow = errors.New("西部数码续费为即时扣款，需先确认平台续费开票流程后才能启用")
var errNoResource = errors.New("西部数码云服务器不提供可下载的资源文件")

// Auth validates the account and API password against a read-only endpoint. It
// is the connection test shown in the supplier page.
func (p *Provider) Auth() error {
	ctx, cancel := p.withTimeout()
	defer cancel()
	if _, _, err := p.balance(ctx); err != nil {
		return err
	}
	return nil
}

// GetBalance exposes the agent prepayment balance; the platform checks it before
// procurement once sales are enabled.
func (p *Provider) GetBalance() (float64, error) {
	ctx, cancel := p.withTimeout()
	defer cancel()
	available, _, err := p.balance(ctx)
	if err != nil {
		return 0, err
	}
	return available, nil
}

func (p *Provider) GetProductResource(int) (*contracts.ProductResource, error) {
	return nil, errNoResource
}

// Provisioning runs entirely through CreateOrder/CreateOrderContext; the legacy
// two-step CreateHost entry point is never used by the platform and would risk a
// second charge, so it stays closed.
func (p *Provider) CreateHost(int64) error {
	return errors.New("西部数码开通在 CreateOrder 中一次完成，不支持单独建机")
}

func (p *Provider) RenewHost(int64, string) (*contracts.RenewResult, error) {
	return nil, errRenewalFlow
}

func (p *Provider) UpgradeHost(int64, contracts.UpgradeConfig) (*contracts.UpgradeResult, error) {
	return nil, errors.New("西部数码云服务器暂不支持在线升级")
}

func (p *Provider) SuspendHost(hostID int64, _ string) error {
	ctx, cancel := p.withTimeout()
	defer cancel()
	ip, err := p.ipOf(ctx, hostID)
	if err != nil {
		return err
	}
	return p.setState(ctx, ip, "suspend")
}

func (p *Provider) UnsuspendHost(hostID int64) error {
	ctx, cancel := p.withTimeout()
	defer cancel()
	ip, err := p.ipOf(ctx, hostID)
	if err != nil {
		return err
	}
	return p.setState(ctx, ip, "resume")
}

// TerminateHost has no documented endpoint. Destroying capacity without an
// upstream confirmation would leave the customer paying for nothing.
func (p *Provider) TerminateHost(int64) error {
	return errors.New("西部数码云服务器暂不支持接口侧销毁")
}

func (p *Provider) RebootHost(hostID int64) error {
	ctx, cancel := p.withTimeout()
	defer cancel()
	ip, err := p.ipOf(ctx, hostID)
	if err != nil {
		return err
	}
	return p.setState(ctx, ip, "reboot")
}

func (p *Provider) ReinstallHost(hostID int64, imageID string) error {
	ctx, cancel := p.withTimeout()
	defer cancel()
	ip, err := p.ipOf(ctx, hostID)
	if err != nil {
		return err
	}
	osValue, err := p.osValue(ctx, imageID)
	if err != nil {
		return err
	}
	return p.reinstall(ctx, ip, osValue, true)
}

// West publishes no signed callback for reseller provisioning, so webhooks are
// refused instead of accepting unverifiable payloads.
func (p *Provider) VerifyWebhook(string, []byte) bool { return false }

func (p *Provider) ParseWebhook([]byte) (*contracts.WebhookEvent, error) {
	return nil, errors.New("西部数码云服务器不提供签名回调")
}
