package zjmf

import (
	"ddyidc/internal/upstream/testpolicy"
	"testing"
)

func configureLocalProductPolicy(t *testing.T) {
	t.Helper()
	if err := testpolicy.Configure("local-vm", testpolicy.Config{Enabled: true, Rules: []testpolicy.Rule{
		{Host: "www.qkidc.cn", ProductIDs: []int{353, 489}, BlockCatalogue: true},
	}}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := testpolicy.Configure("production", testpolicy.Config{}); err != nil {
			t.Fatal(err)
		}
	})
}
