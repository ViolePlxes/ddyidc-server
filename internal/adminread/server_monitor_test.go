package adminread

import (
	"ddyidc/pkg/auth"
	"math"
	"net/http/httptest"
	"testing"
)

func TestCPUUsageExcludesGuestAndUsesDeltas(t *testing.T) {
	before, e := parseCPU("cpu 100 0 50 800 50 0 0 0 100 0\ncpu0 100 0 50 800 50 0 0 0 100 0\n")
	if e != nil {
		t.Fatal(e)
	}
	after, e := parseCPU("cpu 130 0 60 850 60 0 0 0 130 0\ncpu0 130 0 60 850 60 0 0 0 130 0\n")
	if e != nil {
		t.Fatal(e)
	}
	result := cpuDelta(before, after, 10)
	if result == nil || math.Abs(result.UsedPercent-40) > 0.001 || len(result.Cores) != 1 || result.Cores[0].UsedPercent != 40 {
		t.Fatalf("wrong CPU delta: %+v", result)
	}
	if cpuDelta(after, before, 10) != nil || cpuDelta(before, before, 10) != nil {
		t.Fatal("counter reset or unchanged ticks must not fabricate usage")
	}
}
func TestMemoryUsesAvailableRatherThanFree(t *testing.T) {
	got := parseMemory("MemTotal: 1000 kB\nMemFree: 10 kB\nMemAvailable: 400 kB\n")
	if got == nil || got.Total != 1024000 || got.Used != 614400 || got.Available != 409600 || got.UsedPercent != 60 {
		t.Fatalf("wrong memory: %+v", got)
	}
	if parseMemory("MemTotal: 100 kB\n") == nil {
	} else {
		t.Fatal("missing available must stay unavailable")
	}
}
func TestDiskMountsDeduplicateAndIgnoreVirtualFilesystems(t *testing.T) {
	got := parseMounts("1 0 8:1 / / rw - ext4 /dev/sda1 rw\n2 1 8:1 /var /var/lib rw - ext4 /dev/sda1 rw\n3 1 0:1 / /proc rw - proc proc rw\n4 1 0:2 / /run rw - tmpfs tmpfs rw\n5 1 8:2 / /data\\040disk rw - xfs /dev/sda2 rw\n")
	if len(got) != 2 || got[0].mount != "/" || got[1].mount != "/data disk" {
		t.Fatalf("unexpected mounts: %+v", got)
	}
}
func TestServerMonitorRequiresAdminAuthorization(t *testing.T) {
	h := &handler{auth: auth.NewManager("test-access", "test-refresh", 900, 900)}
	for _, tc := range []struct {
		header string
		status int
	}{{"", 401}, {"Bearer invalid", 403}} {
		req := httptest.NewRequest("GET", "/admin/v1/server-monitor", nil)
		req.Header.Set("Authorization", tc.header)
		w := httptest.NewRecorder()
		h.serverMonitor(w, req)
		if w.Code != tc.status {
			t.Fatalf("got %d want %d", w.Code, tc.status)
		}
		if hostSampler.previous != nil {
			t.Fatal("unauthorized request sampled host")
		}
	}
}
