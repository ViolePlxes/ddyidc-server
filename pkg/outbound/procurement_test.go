package outbound

import (
	"testing"
	"time"
)

func TestProcurementTransportRetainsBoundaries(t *testing.T) {
	c := NewProcurementClient(time.Minute)
	tr := c.Transport.(transport).base
	if c.Timeout != time.Minute || tr.ResponseHeaderTimeout != time.Minute || !tr.DisableKeepAlives || tr.Proxy != nil || tr.DialContext == nil || tr.MaxResponseHeaderBytes != 1<<20 {
		t.Fatal("procurement policy changed")
	}
	ordinary := NewClient(30 * time.Second).Transport.(transport).base
	if ordinary.ResponseHeaderTimeout != 15*time.Second || ordinary.DisableKeepAlives {
		t.Fatal("ordinary policy widened")
	}
}
