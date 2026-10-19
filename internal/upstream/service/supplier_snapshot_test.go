package service

import (
	"encoding/json"
	"testing"
	"time"
)

func TestSupplierSnapshotSettings(t *testing.T) {
	v, e := decodeSnapshotState("")
	if e != nil || !v.Enabled || v.IntervalMinutes != 120 || v.State != "never" {
		t.Fatal("defaults")
	}
	for _, raw := range []string{`{"intervalMinutes":0}`, `{"intervalMinutes":14}`, `{"intervalMinutes":1441}`, `{bad`} {
		if _, e := decodeSnapshotState(raw); e == nil {
			t.Fatal("invalid setting accepted")
		}
	}
	v.State = "running"
	v.RunID = "private-fence"
	v.LeaseUntil = time.Now().Unix() - 1
	info := snapshotView(v)
	if info.State != "interrupted" {
		t.Fatal("expired running not visible")
	}
	raw, _ := json.Marshal(info)
	if string(raw) == "" {
		t.Fatal("empty view")
	}
	if info.LastSuccessAt != 0 {
		t.Fatal("interruption advanced success")
	}
}
