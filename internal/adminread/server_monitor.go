package adminread

import (
	"bufio"
	"context"
	"fmt"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/sys/unix"
)

type cpuTicks struct{ total, idle uint64 }
type coreUsage struct {
	Name        string  `json:"name"`
	UsedPercent float64 `json:"usedPercent"`
}
type cpuUsage struct {
	UsedPercent   float64     `json:"usedPercent"`
	Cores         []coreUsage `json:"cores"`
	SampleSeconds float64     `json:"sampleSeconds"`
	Load          []float64   `json:"load"`
}
type memoryUsage struct {
	Total       uint64  `json:"total"`
	Used        uint64  `json:"used"`
	Available   uint64  `json:"available"`
	UsedPercent float64 `json:"usedPercent"`
}
type diskUsage struct {
	Mount       string  `json:"mount"`
	Device      string  `json:"device"`
	Filesystem  string  `json:"filesystem"`
	Total       uint64  `json:"total"`
	Used        uint64  `json:"used"`
	Available   uint64  `json:"available"`
	UsedPercent float64 `json:"usedPercent"`
}
type diskSnapshot struct {
	Total       uint64      `json:"total"`
	Used        uint64      `json:"used"`
	Available   uint64      `json:"available"`
	UsedPercent float64     `json:"usedPercent"`
	Partitions  []diskUsage `json:"partitions"`
}
type serverSnapshot struct {
	SampledAt time.Time     `json:"sampledAt"`
	CPU       *cpuUsage     `json:"cpu"`
	Memory    *memoryUsage  `json:"memory"`
	Disk      *diskSnapshot `json:"disk"`
	Warnings  []string      `json:"warnings"`
}
type systemSampler struct {
	mu         sync.Mutex
	previous   map[string]cpuTicks
	previousAt time.Time
	cached     serverSnapshot
}

var hostSampler systemSampler

func (h *handler) serverMonitor(w http.ResponseWriter, r *http.Request) {
	if !h.authorize(w, r) {
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	snapshot, err := hostSampler.sample(r.Context())
	if err != nil {
		writeError(w, 503, "服务器采样暂不可用")
		return
	}
	writeJSON(w, snapshot)
}

// Shared short-lived cache bounds work for multiple administrators. No shell,
// privileged agent, database query, or permanent background task is involved.
func (s *systemSampler) sample(ctx context.Context) (serverSnapshot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.cached.SampledAt.IsZero() && time.Since(s.cached.SampledAt) < 8*time.Second {
		return s.cached, nil
	}
	current, err := readCPU("/proc/stat")
	currentAt := time.Now()
	result := serverSnapshot{Warnings: []string{}}
	if err == nil && s.previous == nil {
		s.previous = current
		s.previousAt = currentAt
		timer := time.NewTimer(time.Second)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return result, ctx.Err()
		case <-timer.C:
		}
		current, err = readCPU("/proc/stat")
		currentAt = time.Now()
	}
	if err == nil {
		result.CPU = cpuDelta(s.previous, current, currentAt.Sub(s.previousAt).Seconds())
		s.previous = current
		s.previousAt = currentAt
	}
	if result.CPU == nil {
		result.Warnings = append(result.Warnings, "CPU 采样不可用")
	} else {
		if raw, e := os.ReadFile("/proc/loadavg"); e == nil {
			for i, v := range strings.Fields(string(raw)) {
				if i >= 3 {
					break
				}
				n, e := strconv.ParseFloat(v, 64)
				if e == nil {
					result.CPU.Load = append(result.CPU.Load, n)
				}
			}
		}
	}
	if raw, e := os.ReadFile("/proc/meminfo"); e == nil {
		result.Memory = parseMemory(string(raw))
	}
	if result.Memory == nil {
		result.Warnings = append(result.Warnings, "内存采样不可用")
	}
	result.Disk = readDisks()
	if result.Disk == nil {
		result.Warnings = append(result.Warnings, "磁盘采样不可用")
	}
	result.SampledAt = time.Now().UTC()
	s.cached = result
	return result, nil
}

func readCPU(path string) (map[string]cpuTicks, error) {
	raw, e := os.ReadFile(path)
	if e != nil {
		return nil, e
	}
	return parseCPU(string(raw))
}
func parseCPU(raw string) (map[string]cpuTicks, error) {
	result := map[string]cpuTicks{}
	for _, line := range strings.Split(raw, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 5 || !strings.HasPrefix(fields[0], "cpu") {
			continue
		}
		var ticks cpuTicks
		// guest and guest_nice are already included in user/nice: never double-count.
		for i := 1; i < len(fields) && i <= 8; i++ {
			v, e := strconv.ParseUint(fields[i], 10, 64)
			if e != nil {
				return nil, e
			}
			ticks.total += v
			if i == 4 || i == 5 {
				ticks.idle += v
			}
		}
		result[fields[0]] = ticks
	}
	if _, ok := result["cpu"]; !ok {
		return nil, fmt.Errorf("aggregate CPU missing")
	}
	return result, nil
}
func percent(used, total uint64) float64 {
	if total == 0 {
		return 0
	}
	return float64(used) * 100 / float64(total)
}
func cpuDelta(before, after map[string]cpuTicks, seconds float64) *cpuUsage {
	aggregate, ok := after["cpu"]
	prev, exists := before["cpu"]
	if !ok || !exists || aggregate.total <= prev.total || aggregate.idle < prev.idle || seconds <= 0 {
		return nil
	}
	usage := func(a, b cpuTicks) (float64, bool) {
		if b.total <= a.total || b.idle < a.idle {
			return 0, false
		}
		total, idle := b.total-a.total, b.idle-a.idle
		if idle > total {
			return 0, false
		}
		return percent(total-idle, total), true
	}
	total, ok := usage(prev, aggregate)
	if !ok {
		return nil
	}
	result := &cpuUsage{UsedPercent: total, SampleSeconds: seconds, Cores: []coreUsage{}, Load: []float64{}}
	names := []string{}
	for name := range after {
		if name != "cpu" {
			names = append(names, name)
		}
	}
	sort.Slice(names, func(i, j int) bool {
		a, _ := strconv.Atoi(strings.TrimPrefix(names[i], "cpu"))
		b, _ := strconv.Atoi(strings.TrimPrefix(names[j], "cpu"))
		return a < b
	})
	for _, name := range names {
		if previous, ok := before[name]; ok {
			if value, ok := usage(previous, after[name]); ok {
				result.Cores = append(result.Cores, coreUsage{name, value})
			}
		}
	}
	return result
}
func parseMemory(raw string) *memoryUsage {
	values := map[string]uint64{}
	for _, line := range strings.Split(raw, "\n") {
		f := strings.Fields(line)
		if len(f) < 2 {
			continue
		}
		n, e := strconv.ParseUint(f[1], 10, 64)
		if e == nil {
			values[strings.TrimSuffix(f[0], ":")] = n * 1024
		}
	}
	total := values["MemTotal"]
	available, ok := values["MemAvailable"]
	if total == 0 || !ok || available > total {
		return nil
	}
	return &memoryUsage{total, total - available, available, percent(total-available, total)}
}

type diskMount struct{ id, mount, device, filesystem string }

func parseMounts(raw string) []diskMount {
	seen := map[string]diskMount{}
	unescape := strings.NewReplacer(`\040`, " ", `\011`, "\t", `\012`, "\n", `\134`, `\`)
	scanner := bufio.NewScanner(strings.NewReader(raw))
	for scanner.Scan() {
		parts := strings.SplitN(scanner.Text(), " - ", 2)
		if len(parts) != 2 {
			continue
		}
		left, right := strings.Fields(parts[0]), strings.Fields(parts[1])
		if len(left) < 6 || len(right) < 2 {
			continue
		}
		fs := right[0]
		switch fs {
		case "ext2", "ext3", "ext4", "xfs", "btrfs", "zfs", "f2fs", "vfat", "exfat", "ntfs", "ntfs3", "overlay":
		default:
			continue
		}
		mount := diskMount{left[2], unescape.Replace(left[4]), unescape.Replace(right[1]), fs}
		// Bind mounts share the same major:minor. Prefer the shortest mount path.
		old, ok := seen[mount.id]
		if !ok || len(mount.mount) < len(old.mount) {
			seen[mount.id] = mount
		}
	}
	result := []diskMount{}
	for _, m := range seen {
		result = append(result, m)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].mount < result[j].mount })
	return result
}
func readDisks() *diskSnapshot {
	raw, e := os.ReadFile("/proc/self/mountinfo")
	if e != nil {
		return nil
	}
	result := &diskSnapshot{Partitions: []diskUsage{}}
	for _, mount := range parseMounts(string(raw)) {
		var stat unix.Statfs_t
		if unix.Statfs(mount.mount, &stat) != nil || stat.Blocks == 0 || stat.Bsize <= 0 || stat.Bfree > stat.Blocks {
			continue
		}
		total := stat.Blocks * uint64(stat.Bsize)
		used := (stat.Blocks - stat.Bfree) * uint64(stat.Bsize)
		available := stat.Bavail * uint64(stat.Bsize)
		result.Partitions = append(result.Partitions, diskUsage{mount.mount, mount.device, mount.filesystem, total, used, available, percent(used, total)})
		result.Total += total
		result.Used += used
		result.Available += available
	}
	if result.Total == 0 {
		return nil
	}
	result.UsedPercent = percent(result.Used, result.Total)
	return result
}
