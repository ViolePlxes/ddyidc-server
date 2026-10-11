package cdnsell

import "testing"

func TestAutomaticPauseRequiresConfirmedCloudState(t *testing.T) {
	paused, err := externallyPaused("offline")
	if err != nil || !paused {
		t.Fatal("external manual pause not preserved")
	}
	for _, status := range []string{"online", "active", "ONLINE"} {
		paused, err = externallyPaused(status)
		if err != nil || paused {
			t.Fatal("active status rejected", status)
		}
	}
	for _, status := range []string{"", "deleted", "configuring", "invalid"} {
		if _, err = externallyPaused(status); err == nil {
			t.Fatal("unknown state permitted automatic write", status)
		}
	}
}
