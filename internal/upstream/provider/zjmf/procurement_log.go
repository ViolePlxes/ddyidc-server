package zjmf

import (
	"net/url"

	"ddyidc/internal/upstream/provider/contracts"
	"github.com/zeromicro/go-zero/core/logx"
)

// logProcurementCall records one sanitized supplier call for a procurement write.
// It never logs the query string, headers, bodies, tokens, or raw transport
// errors: only the host, path, category and timings, which is what tells DNS,
// TLS, connection and read timeouts apart when a settle stage returns http=0.
func logProcurementCall(supplierID, method, endpoint string, attempt, status int, elapsedMS int64, failure *contracts.ProcurementFailure) {
	host, path := "", ""
	if parsed, err := url.Parse(endpoint); err == nil {
		host, path = parsed.Hostname(), parsed.Path
	}
	if failure == nil {
		// Slow but successful writes still matter: the supplier is close to the
		// timeout budget and the next call may cross it.
		if elapsedMS >= 30_000 {
			logx.Errorf("supplier procurement call slow supplier=%s host=%s path=%s method=%s attempt=%d status=%d elapsed_ms=%d", supplierID, host, path, method, attempt, status, elapsedMS)
		}
		return
	}
	logx.Errorf("supplier procurement call failed supplier=%s host=%s path=%s method=%s attempt=%d status=%d code=%s transport=%s written=%t first_byte=%t elapsed_ms=%d",
		supplierID, host, path, method, attempt, status, failure.Code, failure.TransportPhase, failure.RequestWritten, failure.FirstByte, failure.ElapsedMS)
}
