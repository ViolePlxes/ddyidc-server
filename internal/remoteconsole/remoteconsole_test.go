package remoteconsole

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

type staticSource struct {
	endpoint *RemoteUpstream
	err      error
}

func (s staticSource) CreateRemoteUpstream(context.Context, int64, int64) (*RemoteUpstream, error) {
	return s.endpoint, s.err
}

func TestRemoteSessionJSONContainsOnlyLocalConnectionMaterial(t *testing.T) {
	provider := NewNoVNCProvider(staticSource{endpoint: &RemoteUpstream{
		URL: "wss://secret-upstream.example/websockify?ticket=private", Password: "vnc-secret",
	}})
	session, err := provider.CreateSession(context.Background(), 123, 456)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(session)
	if err != nil {
		t.Fatal(err)
	}
	text := string(encoded)
	for _, forbidden := range []string{"secret-upstream.example", "websockify", "vnc-secret", "private"} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("session JSON leaked %q: %s", forbidden, text)
		}
	}
	if !strings.HasPrefix(session.URL, "/console/instance/123/vnc?") || len(session.Token) != 64 || session.Type != "novnc" {
		t.Fatalf("unexpected local session: %#v", session)
	}
}

func TestNoVNCSessionRejectsInvalidExpiredForeignAndConcurrentClaims(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	newProvider := func() (*NoVNCProvider, *RemoteSession) {
		provider := NewNoVNCProvider(staticSource{endpoint: &RemoteUpstream{URL: "ws://127.0.0.1/websockify"}})
		provider.now = func() time.Time { return now }
		session, err := provider.CreateSession(context.Background(), 123, 456)
		if err != nil {
			t.Fatal(err)
		}
		return provider, session
	}

	provider, session := newProvider()
	if _, err := provider.Claim(123, 456, "bad-token"); !errors.Is(err, ErrSessionInvalid) {
		t.Fatalf("invalid token error = %v", err)
	}
	if _, err := provider.Claim(123, 999, session.Token); !errors.Is(err, ErrSessionInvalid) {
		t.Fatalf("foreign customer error = %v", err)
	}
	if _, err := provider.Claim(123, 456, session.Token); err != nil {
		t.Fatalf("valid claim error = %v", err)
	}
	if _, err := provider.Claim(123, 456, session.Token); !errors.Is(err, ErrSessionBusy) {
		t.Fatalf("concurrent claim error = %v", err)
	}

	provider, session = newProvider()
	now = now.Add(defaultSessionTTL)
	if _, err := provider.Claim(123, 456, session.Token); !errors.Is(err, ErrSessionInvalid) {
		t.Fatalf("expired token error = %v", err)
	}
}

func TestNoVNCProxyForwardsBytesThroughLocalWebsockify(t *testing.T) {
	upgrader := websocket.Upgrader{Subprotocols: []string{"binary"}, CheckOrigin: func(*http.Request) bool { return true }}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("password") != "server-only-password" {
			http.Error(w, "missing server credential", http.StatusUnauthorized)
			return
		}
		connection, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer connection.Close()
		for {
			messageType, payload, err := connection.ReadMessage()
			if err != nil {
				return
			}
			if err := connection.WriteMessage(messageType, payload); err != nil {
				return
			}
		}
	}))
	defer server.Close()

	provider := NewNoVNCProvider(staticSource{endpoint: &RemoteUpstream{
		URL: server.URL + "/websockify", Password: "server-only-password",
	}})
	session, err := provider.CreateSession(context.Background(), 123, 456)
	if err != nil {
		t.Fatal(err)
	}
	claimed, err := provider.Claim(123, 456, session.Token)
	if err != nil {
		t.Fatal(err)
	}
	proxySide, clientSide := net.Pipe()
	done := make(chan error, 1)
	go func() { done <- provider.Proxy(context.Background(), claimed, proxySide) }()

	want := []byte{0x52, 0x46, 0x42, 0x20, 0x00, 0xff, 0x17}
	if _, err := clientSide.Write(want); err != nil {
		t.Fatal(err)
	}
	got := make([]byte, len(want))
	if _, err := io.ReadFull(clientSide, got); err != nil {
		t.Fatal(err)
	}
	if string(got) != string(want) {
		t.Fatalf("forwarded bytes = %v, want %v", got, want)
	}
	_ = clientSide.Close()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("proxy returned %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("proxy did not clean up after client disconnect")
	}
	if _, err := provider.Claim(123, 456, session.Token); !errors.Is(err, ErrSessionInvalid) {
		t.Fatalf("used token remained available: %v", err)
	}
}

type pluginProvider struct {
	engine string
}

func (p *pluginProvider) Engine() string { return p.engine }
func (p *pluginProvider) CreateSession(context.Context, int64, int64) (*RemoteSession, error) {
	return &RemoteSession{Type: p.engine, engine: p.engine}, nil
}
func (p *pluginProvider) Proxy(context.Context, *RemoteSession, net.Conn) error { return nil }
func (p *pluginProvider) Claim(int64, int64, string) (*RemoteSession, error) {
	return &RemoteSession{Type: p.engine, engine: p.engine}, nil
}
func (p *pluginProvider) Discard(*RemoteSession) {}

func TestRegistryDefaultsToNoVNCAndCanRegisterAndSwitchEngines(t *testing.T) {
	registry := NewRegistry()
	if registry.Engine() != "novnc" {
		t.Fatalf("default engine = %q", registry.Engine())
	}
	for _, provider := range []*pluginProvider{{engine: "novnc"}, {engine: "guacamole"}} {
		if err := registry.Register(provider); err != nil {
			t.Fatal(err)
		}
	}
	if err := registry.Use("guacamole"); err != nil {
		t.Fatal(err)
	}
	if registry.Engine() != "guacamole" {
		t.Fatalf("selected engine = %q", registry.Engine())
	}
	if err := registry.Use("missing"); !errors.Is(err, ErrEngineUnavailable) {
		t.Fatalf("missing engine error = %v", err)
	}
}
