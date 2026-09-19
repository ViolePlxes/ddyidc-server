package remoteconsole

import (
	"context"
	"github.com/gorilla/websocket"
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

type versionSource struct {
	staticSource
	version atomic.Int64
}

func (s *versionSource) RemoteOwnership(context.Context, int64, int64) (int64, error) {
	return s.version.Load(), nil
}
func TestTransferRevokesActiveAndIdleRemoteSessions(t *testing.T) {
	for _, crossProcess := range []bool{false, true} {
		t.Run(map[bool]string{false: "local-revoke", true: "durable-version"}[crossProcess], func(t *testing.T) {
			connected := make(chan struct{})
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				ws, err := (&websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}).Upgrade(w, r, nil)
				if err != nil {
					return
				}
				defer ws.Close()
				close(connected)
				for {
					if _, _, err = ws.ReadMessage(); err != nil {
						return
					}
				}
			}))
			defer upstream.Close()
			source := &versionSource{staticSource: staticSource{endpoint: &RemoteUpstream{URL: upstream.URL}}}
			p := NewNoVNCProvider(source)
			defer activeProviders.Delete(p)
			session, err := p.CreateSession(context.Background(), 11, 22)
			if err != nil {
				t.Fatal(err)
			}
			session, err = p.Claim(11, 22, session.Token)
			if err != nil {
				t.Fatal(err)
			}
			a, b := net.Pipe()
			defer a.Close()
			defer b.Close()
			done := make(chan error, 1)
			go func() { done <- p.Proxy(context.Background(), session, a) }()
			select {
			case <-connected:
			case <-time.After(time.Second):
				t.Fatal("proxy did not connect")
			}
			if crossProcess {
				source.version.Add(1)
			} else {
				RevokeInstance(11)
			}
			select {
			case <-done:
			case <-time.After(time.Second):
				t.Fatal("active connection survived ownership change")
			}
			if _, err = p.Claim(11, 22, session.Token); err == nil {
				t.Fatal("old token still accepted")
			}
		})
	}
}
