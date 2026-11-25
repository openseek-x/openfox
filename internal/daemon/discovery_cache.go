package daemon

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// DiscoveryEntry 仅持久化安装清单。登录、模型、协议和可运行状态每次启动都重新探测。
type DiscoveryEntry struct {
	Path     string    `json:"path"`
	Source   string    `json:"source"`
	Version  string    `json:"version"`
	RealPath string    `json:"real_path"`
	Size     int64     `json:"size"`
	Modified time.Time `json:"modified"`
}

type discoveryCache struct {
	CheckedAt   time.Time                 `json:"checked_at"`
	Fingerprint string                    `json:"fingerprint"`
	Entries     map[string]DiscoveryEntry `json:"entries"`
}

func (entry DiscoveryEntry) identify() (DiscoveryEntry, bool) {
	path := entry.Path
	if strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return entry, false
		}
		path = filepath.Join(home, path[2:])
	}
	if !filepath.IsAbs(path) {
		return entry, false
	}
	real, err := filepath.EvalSymlinks(path)
	if err != nil {
		return entry, false
	}
	info, err := os.Stat(real)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0111 == 0 {
		return entry, false
	}
	entry.RealPath, entry.Size, entry.Modified = real, info.Size(), info.ModTime().UTC()
	return entry, true
}

func WriteDiscoveryCache(path, fingerprint string, now time.Time, entries map[string]DiscoveryEntry) error {
	cache := discoveryCache{CheckedAt: now.UTC(), Fingerprint: fingerprint, Entries: map[string]DiscoveryEntry{}}
	for name, entry := range entries {
		if observed, ok := entry.identify(); ok {
			cache.Entries[name] = observed
		}
	}
	data, err := json.Marshal(cache)
	if err != nil {
		return err
	}
	return WritePrivateFile(path, data)
}

func ReadDiscoveryCache(path, fingerprint string, now time.Time) (map[string]DiscoveryEntry, time.Time) {
	data, err := readPrivateFile(path, 64<<10)
	var cache discoveryCache
	if err != nil || json.Unmarshal(data, &cache) != nil || cache.Fingerprint != fingerprint {
		return nil, time.Time{}
	}
	if age := now.Sub(cache.CheckedAt); age < 0 || age >= 72*time.Hour {
		return nil, time.Time{}
	}
	for name, entry := range cache.Entries {
		observed, ok := entry.identify()
		if !ok || observed.RealPath != entry.RealPath || observed.Size != entry.Size || !observed.Modified.Equal(entry.Modified) {
			delete(cache.Entries, name)
		}
	}
	return cache.Entries, cache.CheckedAt
}
