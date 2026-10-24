package instance

import "testing"

func TestLegacyCataloguePlaceholderOnlyKnownServerTypes(t *testing.T) {
	for _, target := range []string{"cloud", "dcimcloud", "dcim", "mf_cloud", "mf_dcim"} {
		if !legacyCataloguePlaceholder("legacy_snapshot", target) {
			t.Fatalf("verified server type rejected: %s", target)
		}
		if legacyCataloguePlaceholder("dcimcloud", target) {
			t.Fatal("real catalogue type treated as a migration placeholder")
		}
	}
	for _, target := range []string{"", "legacy_snapshot", "different-module", "manual", "payment"} {
		if legacyCataloguePlaceholder("legacy_snapshot", target) {
			t.Fatalf("unverified type accepted: %q", target)
		}
	}
}
