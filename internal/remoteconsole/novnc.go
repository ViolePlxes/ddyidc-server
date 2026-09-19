package remoteconsole

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"io"
	"net"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

const defaultSessionTTL = 10 * time.Minute

type RemoteUpstream struct {
	URL      string
	Password string
}

type UpstreamSource interface {
	CreateRemoteUpstream(ctx context.Context, instanceID, customerID int64) (*RemoteUpstream, error)
}

type NoVNCProvider struct {
	source UpstreamSource
	ttl    time.Duration
	now    func() time.Time
	random io.Reader
	dialer *websocket.Dialer

	mu       sync.Mutex
	sessions map[int64]*cachedSession
}

type cachedSession struct {
	session *RemoteSession
	inUse   bool
	cancel  context.CancelFunc
}

func NewNoVNCProvider(source UpstreamSource) *NoVNCProvider {
	p := &NoVNCProvider{
		source:   source,
		ttl:      defaultSessionTTL,
		now:      time.Now,
		random:   rand.Reader,
		dialer:   &websocket.Dialer{HandshakeTimeout: 15 * time.Second, Subprotocols: []string{"binary"}},
		sessions: make(map[int64]*cachedSession),
	}
	activeProviders.Store(p, struct{}{})
	return p
}

func (p *NoVNCProvider) Engine() string { return "novnc" }

func (p *NoVNCProvider) CreateSession(ctx context.Context, instanceID, customerID int64) (*RemoteSession, error) {
	if p.source == nil || instanceID <= 0 || customerID <= 0 {
		return nil, ErrUpstreamUnavailable
	}
	var version int64
	if source, ok := p.source.(OwnershipSource); ok {
		v, e := source.RemoteOwnership(ctx, instanceID, customerID)
		if e != nil {
			return nil, ErrSessionInvalid
		}
		version = v
	}
	upstream, err := p.source.CreateRemoteUpstream(ctx, instanceID, customerID)
	if err != nil || upstream == nil || strings.TrimSpace(upstream.URL) == "" {
		return nil, ErrUpstreamUnavailable
	}
	wsURL, err := upstreamWebSocketURL(upstream.URL, upstream.Password)
	if err != nil {
		return nil, ErrUpstreamUnavailable
	}
	secret := make([]byte, 32)
	if _, err := io.ReadFull(p.random, secret); err != nil {
		return nil, ErrUpstreamUnavailable
	}
	token := hex.EncodeToString(secret)
	session := &RemoteSession{
		ownershipVersion: version,
		Type:             "novnc",
		URL:              "/console/instance/" + strconv.FormatInt(instanceID, 10) + "/vnc?token=" + url.QueryEscape(token) + "&type=novnc",
		Token:            token,
		ExpiresAt:        p.now().Add(p.ttl).UnixMilli(),
		instanceID:       instanceID,
		customerID:       customerID,
		engine:           p.Engine(),
		upstream:         wsURL,
		password:         upstream.Password,
	}
	if !p.valid(ctx, session) {
		return nil, ErrSessionInvalid
	}
	p.revoke(instanceID)
	p.mu.Lock()
	p.sessions[instanceID] = &cachedSession{session: session}
	p.mu.Unlock()
	return session, nil
}

func (p *NoVNCProvider) Claim(instanceID, customerID int64, token string) (*RemoteSession, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	cached := p.sessions[instanceID]
	if cached == nil || cached.session == nil {
		return nil, ErrSessionInvalid
	}
	session := cached.session
	if p.now().UnixMilli() >= session.ExpiresAt {
		delete(p.sessions, instanceID)
		return nil, ErrSessionInvalid
	}
	if session.customerID != customerID || !sameToken(session.Token, token) {
		return nil, ErrSessionInvalid
	}
	if cached.inUse {
		return nil, ErrSessionBusy
	}
	cached.inUse = true
	return session, nil
}

func (p *NoVNCProvider) Proxy(ctx context.Context, session *RemoteSession, clientConn net.Conn) error {
	if session == nil || clientConn == nil || session.engine != p.Engine() || strings.TrimSpace(session.upstream) == "" {
		return ErrSessionInvalid
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	p.mu.Lock()
	cached := p.sessions[session.instanceID]
	if cached == nil || cached.session != session {
		p.mu.Unlock()
		return ErrSessionInvalid
	}
	cached.cancel = cancel
	p.mu.Unlock()
	if !p.valid(ctx, session) {
		return ErrSessionInvalid
	}
	go p.watch(ctx, cancel, session)
	clientConn = &ownershipConn{Conn: clientConn, valid: func() bool { return p.valid(ctx, session) }}
	defer p.Discard(session)
	upstream, response, err := p.dialer.DialContext(ctx, session.upstream, nil)
	if response != nil && response.Body != nil {
		_ = response.Body.Close()
	}
	if err != nil {
		return ErrUpstreamUnavailable
	}
	defer upstream.Close()
	if err := relay(ctx, clientConn, NewWebSocketConn(upstream)); err != nil {
		return ErrUpstreamUnavailable
	}
	return nil
}

func (p *NoVNCProvider) Discard(session *RemoteSession) {
	if session == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	cached := p.sessions[session.instanceID]
	if cached != nil && cached.session == session {
		delete(p.sessions, session.instanceID)
	}
}

func upstreamWebSocketURL(raw, password string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Host == "" {
		return "", ErrUpstreamUnavailable
	}
	switch strings.ToLower(parsed.Scheme) {
	case "http":
		parsed.Scheme = "ws"
	case "https":
		parsed.Scheme = "wss"
	case "ws", "wss":
	default:
		return "", ErrUpstreamUnavailable
	}
	if password != "" {
		query := parsed.Query()
		if query.Get("password") == "" && query.Get("pass") == "" {
			query.Set("password", password)
			parsed.RawQuery = query.Encode()
		}
	}
	parsed.Fragment = ""
	return parsed.String(), nil
}

func sameToken(expected, actual string) bool {
	if len(expected) != len(actual) || len(expected) == 0 {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(expected), []byte(actual)) == 1
}

func relay(ctx context.Context, left, right net.Conn) error {
	copyResult := make(chan error, 2)
	go func() { _, err := io.Copy(left, right); copyResult <- err }()
	go func() { _, err := io.Copy(right, left); copyResult <- err }()
	select {
	case <-ctx.Done():
		_ = left.Close()
		_ = right.Close()
		return nil
	case err := <-copyResult:
		_ = left.Close()
		_ = right.Close()
		if errorsIsClosed(err) {
			return nil
		}
		return err
	}
}

func errorsIsClosed(err error) bool {
	return err == nil || strings.Contains(strings.ToLower(err.Error()), "closed") || strings.Contains(strings.ToLower(err.Error()), "reset by peer")
}

type websocketConn struct {
	conn   *websocket.Conn
	reader io.Reader
	mu     sync.Mutex
}

func NewWebSocketConn(conn *websocket.Conn) net.Conn { return &websocketConn{conn: conn} }

func (c *websocketConn) Read(buffer []byte) (int, error) {
	for {
		if c.reader == nil {
			messageType, reader, err := c.conn.NextReader()
			if err != nil {
				return 0, err
			}
			if messageType != websocket.BinaryMessage && messageType != websocket.TextMessage {
				continue
			}
			c.reader = reader
		}
		n, err := c.reader.Read(buffer)
		if err == io.EOF {
			c.reader = nil
			if n > 0 {
				return n, nil
			}
			continue
		}
		return n, err
	}
}

func (c *websocketConn) Write(buffer []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.conn.WriteMessage(websocket.BinaryMessage, buffer); err != nil {
		return 0, err
	}
	return len(buffer), nil
}

func (c *websocketConn) Close() error         { return c.conn.Close() }
func (c *websocketConn) LocalAddr() net.Addr  { return c.conn.LocalAddr() }
func (c *websocketConn) RemoteAddr() net.Addr { return c.conn.RemoteAddr() }
func (c *websocketConn) SetDeadline(t time.Time) error {
	_ = c.conn.SetReadDeadline(t)
	return c.conn.SetWriteDeadline(t)
}
func (c *websocketConn) SetReadDeadline(t time.Time) error  { return c.conn.SetReadDeadline(t) }
func (c *websocketConn) SetWriteDeadline(t time.Time) error { return c.conn.SetWriteDeadline(t) }
