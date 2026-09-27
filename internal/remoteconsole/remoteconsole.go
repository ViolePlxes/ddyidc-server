package remoteconsole

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"sync"
)

var (
	ErrEngineUnavailable   = errors.New("remote console engine unavailable")
	ErrSessionInvalid      = errors.New("remote console session invalid")
	ErrSessionBusy         = errors.New("remote console session busy")
	ErrUpstreamUnavailable = errors.New("remote console upstream unavailable")
)

// RemoteSession is the only remote-console contract exposed to the HTTP layer.
// URL is always a same-origin relative path. Upstream connection material is
// kept in unexported fields and therefore cannot be serialized accidentally.
type RemoteSession struct {
	ownershipVersion int64
	Type             string            `json:"type"`
	URL              string            `json:"url"`
	Token            string            `json:"token"`
	ExpiresAt        int64             `json:"expiresAt"`
	Extra            map[string]string `json:"extra,omitempty"`

	instanceID int64
	customerID int64
	engine     string
	upstream   string
	password   string
	rfbAuth    bool
}

type RemoteConsoleProvider interface {
	Engine() string
	CreateSession(ctx context.Context, instanceID int64, customerID int64) (*RemoteSession, error)
	Proxy(ctx context.Context, session *RemoteSession, clientConn net.Conn) error
}

type sessionProvider interface {
	RemoteConsoleProvider
	Claim(instanceID, customerID int64, token string) (*RemoteSession, error)
	Discard(session *RemoteSession)
}

type Registry struct {
	mu        sync.RWMutex
	providers map[string]RemoteConsoleProvider
	engine    string
}

func NewRegistry() *Registry {
	return &Registry{providers: make(map[string]RemoteConsoleProvider), engine: "novnc"}
}

func (r *Registry) Register(provider RemoteConsoleProvider) error {
	if provider == nil {
		return fmt.Errorf("%w: empty provider", ErrEngineUnavailable)
	}
	if _, ok := provider.(sessionProvider); !ok {
		return fmt.Errorf("%w: provider cannot manage sessions", ErrEngineUnavailable)
	}
	engine := strings.TrimSpace(strings.ToLower(provider.Engine()))
	if engine == "" {
		return fmt.Errorf("%w: empty engine", ErrEngineUnavailable)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.providers[engine] = provider
	return nil
}

func (r *Registry) Use(engine string) error {
	engine = strings.TrimSpace(strings.ToLower(engine))
	if engine == "" {
		engine = "novnc"
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.providers[engine]; !ok {
		return ErrEngineUnavailable
	}
	r.engine = engine
	return nil
}

func (r *Registry) Engine() string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.engine
}

func (r *Registry) current() (RemoteConsoleProvider, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	provider, ok := r.providers[r.engine]
	if !ok {
		return nil, ErrEngineUnavailable
	}
	return provider, nil
}

func (r *Registry) CreateSession(ctx context.Context, instanceID, customerID int64) (*RemoteSession, error) {
	provider, err := r.current()
	if err != nil {
		return nil, err
	}
	return provider.CreateSession(ctx, instanceID, customerID)
}

func (r *Registry) Claim(instanceID, customerID int64, token string) (*RemoteSession, error) {
	provider, err := r.current()
	if err != nil {
		return nil, err
	}
	return provider.(sessionProvider).Claim(instanceID, customerID, token)
}

func (r *Registry) Proxy(ctx context.Context, session *RemoteSession, clientConn net.Conn) error {
	if session == nil {
		return ErrSessionInvalid
	}
	r.mu.RLock()
	provider := r.providers[session.engine]
	r.mu.RUnlock()
	if provider == nil {
		return ErrEngineUnavailable
	}
	return provider.Proxy(ctx, session, clientConn)
}

func (r *Registry) Discard(session *RemoteSession) {
	if session == nil {
		return
	}
	r.mu.RLock()
	provider := r.providers[session.engine]
	r.mu.RUnlock()
	if provider != nil {
		provider.(sessionProvider).Discard(session)
	}
}

type Config struct {
	Engine string `json:",optional"`
	NodeID string `json:",optional"`
}
