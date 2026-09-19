package remoteconsole

import (
	"context"
	"net"
	"sync"
	"time"
)

// Production instance sources expose a durable ownership version. Checking it
// also invalidates sessions on other server processes and after a transfer back.
type OwnershipSource interface {
	RemoteOwnership(context.Context, int64, int64) (int64, error)
}

var activeProviders sync.Map

func RevokeInstance(id int64) {
	activeProviders.Range(func(key, value any) bool { key.(*NoVNCProvider).revoke(id); return true })
}
func (p *NoVNCProvider) revoke(id int64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if cached := p.sessions[id]; cached != nil {
		if cached.cancel != nil {
			cached.cancel()
		}
		delete(p.sessions, id)
	}
}
func (p *NoVNCProvider) valid(ctx context.Context, s *RemoteSession) bool {
	source, ok := p.source.(OwnershipSource)
	if !ok {
		return true
	}
	version, err := source.RemoteOwnership(ctx, s.instanceID, s.customerID)
	return err == nil && version == s.ownershipVersion
}

type ownershipConn struct {
	net.Conn
	valid func() bool
}

func (c *ownershipConn) Read(b []byte) (int, error) {
	n, err := c.Conn.Read(b)
	if !c.valid() {
		return 0, ErrSessionInvalid
	}
	return n, err
}
func (c *ownershipConn) Write(b []byte) (int, error) {
	if !c.valid() {
		return 0, ErrSessionInvalid
	}
	return c.Conn.Write(b)
}
func (p *NoVNCProvider) watch(ctx context.Context, cancel context.CancelFunc, s *RemoteSession) {
	tick := time.NewTicker(250 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			if !p.valid(ctx, s) {
				cancel()
				return
			}
		}
	}
}
