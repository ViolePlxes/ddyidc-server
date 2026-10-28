package zjmf

import (
	"crypto/tls"
	"ddyidc/internal/upstream/provider/contracts"
	"net/http/httptrace"
	"sync"
	"time"
)

// Fixed categories only: no URLs, IPs, headers, body, passwords or raw errors.
// WroteRequest proves only that the client completed a write, not acceptance.
// Absence of the callback is NOT sufficient permission to replay a purchase.
type procurementTrace struct {
	mu                 sync.Mutex
	started            time.Time
	phase              string
	written, firstByte bool
}

func newProcurementTrace() *procurementTrace {
	return &procurementTrace{started: time.Now(), phase: "connection_wait"}
}
func (t *procurementTrace) setPhase(phase string) { t.mu.Lock(); defer t.mu.Unlock(); t.phase = phase }
func (t *procurementTrace) hooks() *httptrace.ClientTrace {
	return &httptrace.ClientTrace{
		GetConn:  func(string) { t.setPhase("connection_wait") },
		DNSStart: func(httptrace.DNSStartInfo) { t.setPhase("dns") },
		DNSDone: func(v httptrace.DNSDoneInfo) {
			if v.Err == nil {
				t.setPhase("connect")
			}
		},
		ConnectStart:      func(string, string) { t.setPhase("connect") },
		TLSHandshakeStart: func() { t.setPhase("tls") },
		TLSHandshakeDone: func(_ tls.ConnectionState, err error) {
			if err == nil {
				t.setPhase("request_write")
			}
		},
		GotConn: func(httptrace.GotConnInfo) { t.setPhase("request_write") },
		WroteRequest: func(v httptrace.WroteRequestInfo) {
			t.mu.Lock()
			defer t.mu.Unlock()
			if v.Err == nil {
				t.written = true
				t.phase = "response_headers"
			}
		},
		GotFirstResponseByte: func() { t.mu.Lock(); defer t.mu.Unlock(); t.firstByte = true; t.phase = "response_headers" },
	}
}
func (t *procurementTrace) failure(err error, status int) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	f := contracts.ClassifyProcurementFailure("provider", err)
	// The business stage is added by CreateOrder after this transport boundary.
	f.Stage = ""
	f.TransportPhase = t.phase
	f.RequestWritten = t.written
	f.FirstByte = t.firstByte
	f.ElapsedMS = time.Since(t.started).Milliseconds()
	f.HTTPStatus = status
	return f
}
