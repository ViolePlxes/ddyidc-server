package payment

import (
	"context"
	"errors"
	"net/http"
	"time"

	dom "ddyidc/internal/payment/domain"
)

// Validate local credentials at startup; fetch platform trust material only
// when a WeChat operation is requested. An outage must not stop the whole API.
type deferredWechatChannel struct {
	gate    chan struct{}
	build   func(context.Context) (Channel, error)
	channel Channel
	retryAt time.Time
}

func newDeferredWechatChannel(cfg WechatConfig) (Channel, error) {
	if _, _, err := wechatKeys(cfg); err != nil {
		return nil, err
	}
	return &deferredWechatChannel{gate: make(chan struct{}, 1), build: func(ctx context.Context) (Channel, error) {
		return newWechatChannel(ctx, cfg)
	}}, nil
}

func (c *deferredWechatChannel) get(ctx context.Context) (Channel, error) {
	select {
	case c.gate <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	defer func() { <-c.gate }()
	if c.channel != nil {
		return c.channel, nil
	}
	if time.Now().Before(c.retryAt) {
		return nil, errors.New("微信支付连接暂不可用，请稍后重试")
	}
	initCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	channel, err := c.build(initCtx)
	if err != nil {
		c.retryAt = time.Now().Add(30 * time.Second)
		return nil, errors.New("微信支付连接暂不可用，请稍后重试")
	}
	c.channel = channel
	return channel, nil
}

func (c *deferredWechatChannel) Create(ctx context.Context, p *dom.Payment, subject, mode string) (*Checkout, error) {
	ch, err := c.get(ctx)
	if err != nil {
		return nil, err
	}
	return ch.Create(ctx, p, subject, mode)
}
func (c *deferredWechatChannel) Close(ctx context.Context, p *dom.Payment) error {
	ch, err := c.get(ctx)
	if err != nil {
		return err
	}
	return ch.Close(ctx, p)
}
func (c *deferredWechatChannel) ParseNotification(r *http.Request) (dom.VerifiedNotification, error) {
	ch, err := c.get(r.Context())
	if err != nil {
		return dom.VerifiedNotification{}, err
	}
	return ch.ParseNotification(r)
}
func (c *deferredWechatChannel) Query(ctx context.Context, no string) (PlatformQuery, error) {
	ch, err := c.get(ctx)
	if err != nil {
		return PlatformQuery{}, err
	}
	return ch.(QueryableChannel).Query(ctx, no)
}
func (c *deferredWechatChannel) Refund(ctx context.Context, p *dom.Payment, refund *dom.Refund) error {
	ch, err := c.get(ctx)
	if err != nil {
		return err
	}
	return ch.(RefundableChannel).Refund(ctx, p, refund)
}
