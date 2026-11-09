package supervisor

import (
	"context"
	"openfox/internal/runtimepolicy"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// The first run is always admitted. Only sample when additional runs are offered.
func underResourcePressure(ctx context.Context, p runtimepolicy.DevicePreferences) bool {
	if !p.MemoryGuard && !p.LoadGuard {
		return false
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	if p.MemoryGuard {
		available, ok := availableMiB(ctx)
		if !ok || available < float64(p.MinMemoryMiB) {
			return true
		}
	}
	if !p.LoadGuard {
		return false
	}
	var data []byte
	var err error
	switch runtime.GOOS {
	case "darwin":
		data, err = exec.CommandContext(ctx, "/usr/sbin/sysctl", "-n", "vm.loadavg").Output()
	case "linux":
		data, err = os.ReadFile("/proc/loadavg")
	default:
		return true
	}
	parts := strings.Fields(strings.Trim(string(data), " {}\n"))
	if err != nil || len(parts) == 0 {
		return true
	}
	load, err := strconv.ParseFloat(parts[0], 64)
	factor := map[string]float64{"balanced": 1, "performance": 1.5, "conservative": 0.75}[p.LoadPolicy]
	return err != nil || load > float64(runtime.NumCPU())*factor
}
func availableMiB(ctx context.Context) (float64, bool) {
	switch runtime.GOOS {
	case "linux":
		data, err := os.ReadFile("/proc/meminfo")
		if err != nil {
			return 0, false
		}
		for _, l := range strings.Split(string(data), "\n") {
			v := strings.Fields(l)
			if len(v) == 3 && v[0] == "MemAvailable:" {
				n, e := strconv.ParseFloat(v[1], 64)
				return n / 1024, e == nil
			}
		}
	case "darwin":
		data, err := exec.CommandContext(ctx, "/usr/bin/vm_stat").Output()
		if err != nil {
			return 0, false
		}
		lines := strings.Split(string(data), "\n")
		_, after, ok := strings.Cut(lines[0], "page size of ")
		if !ok {
			return 0, false
		}
		v := strings.Fields(after)
		if len(v) == 0 {
			return 0, false
		}
		page, e := strconv.ParseFloat(v[0], 64)
		if e != nil {
			return 0, false
		}
		var pages float64
		found := false
		for _, l := range lines {
			key, value, _ := strings.Cut(l, ":")
			if key == "Pages free" || key == "Pages inactive" || key == "Pages speculative" {
				n, e := strconv.ParseFloat(strings.Trim(strings.TrimSpace(value), "."), 64)
				if e != nil {
					return 0, false
				}
				pages += n
				found = true
			}
		}
		return pages * page / (1024 * 1024), found
	}
	return 0, false
}
