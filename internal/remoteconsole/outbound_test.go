package remoteconsole

import (
	"ddyidc/pkg/outbound"
	"os"
	"testing"
)

// No public network is used by the socket fixtures. Production has no such
// exception unless explicitly configured by the operator.
func TestMain(m *testing.M) {
	ports := make([]int, 65535)
	for i := range ports {
		ports[i] = i + 1
	}
	if err := outbound.Configure(outbound.Config{PrivateCIDRs: []string{"127.0.0.1/32", "::1/128"}, Ports: ports}); err != nil {
		panic(err)
	}
	os.Exit(m.Run())
}
