package zjmf

import (
	"context"
	"ddyidc/internal/upstream/provider/contracts"
	"errors"
	"net/http/httptrace"
	"strings"
	"testing"
)

func TestProcurementTracePreservesBusinessStageWithoutSecrets(t *testing.T) {
	for _, phase := range []string{"dns", "connect", "tls", "request_write", "response_headers", "response_body"} {
		tr := newProcurementTrace()
		tr.setPhase(phase)
		if phase == "response_headers" || phase == "response_body" {
			tr.hooks().WroteRequest(httptrace.WroteRequestInfo{})
			if phase == "response_body" {
				tr.hooks().GotFirstResponseByte()
				tr.setPhase(phase)
			}
		}
		e := contracts.ClassifyProcurementFailure("cart_settle", tr.failure(context.DeadlineExceeded, 0))
		if e.Stage != "cart_settle" || e.Code != "timeout" || e.TransportPhase != phase || e.RequestWritten != (phase == "response_headers" || phase == "response_body") {
			t.Fatalf("wrong trace: %+v", e)
		}
		if _, ok := any(e).(interface{ SafeToRetry() bool }); ok {
			t.Fatal("transport evidence enabled write replay")
		}
	}
	tr := newProcurementTrace()
	tr.hooks().WroteRequest(httptrace.WroteRequestInfo{Err: errors.New("SECRET")})
	e := contracts.ClassifyProcurementFailure("cart_add", tr.failure(errors.New("https://SECRET/password"), 0))
	if e.RequestWritten || strings.Contains(e.Error(), "SECRET") {
		t.Fatal("secret leaked or partial write classified complete")
	}
	e.TransportPhase = "SECRET"
	if strings.Contains(e.Error(), "SECRET") {
		t.Fatal("untrusted phase leaked")
	}
}
