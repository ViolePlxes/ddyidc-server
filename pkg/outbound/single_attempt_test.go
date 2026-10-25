package outbound

import (
 "testing"
 "time"
)
func TestChargedGETClientDoesNotReuseConnections(t *testing.T){
 safe:=NewSingleAttemptClient(time.Second);normal:=NewClient(time.Second)
 if !safe.Transport.(transport).base.DisableKeepAlives{t.Fatal("charged GET may be retried on reused connections")}
 if normal.Transport.(transport).base.DisableKeepAlives{t.Fatal("ordinary client pooling changed")}
 if safe.Transport.(transport).base.Proxy!=nil{t.Fatal("environment proxy could bypass network policy")}
}
