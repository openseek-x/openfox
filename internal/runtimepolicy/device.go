package runtimepolicy

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"unicode"
)

// DevicePreferences are local to a physical computer, shared by its owner's organizations.
type DevicePreferences struct {
	SessionIdleMinutes int      `json:"session_idle_minutes,omitempty"`
	SessionMaxMinutes  int      `json:"session_max_minutes,omitempty"`
	CodexPath          string   `json:"codex_path"`
	CodexArgs          []string `json:"codex_args"`
	MemoryGuard        bool     `json:"memory_guard"`
	MinMemoryMiB       int      `json:"min_memory_mib"`
	LoadGuard          bool     `json:"load_guard"`
	LoadPolicy         string   `json:"load_policy"`
}
type StoragePolicy struct {
	RepoDays      int  `json:"repo_days,omitempty"`
	Enabled       bool `json:"enabled"`
	IntervalHours int  `json:"interval_hours"`
	RetainDays    int  `json:"retain_days"`
	MinFreeGiB    int  `json:"min_free_gib"`
	MaxGiB        int  `json:"max_gib"`
}

func DefaultStoragePolicy() StoragePolicy {
	return StoragePolicy{RepoDays: 37, IntervalHours: 1, RetainDays: 7, MinFreeGiB: 10}
}
func (p StoragePolicy) Valid() bool {
	return (p.RepoDays == 0 || p.RepoDays >= 30 && p.RepoDays <= 3650) && p.IntervalHours >= 1 && p.IntervalHours <= 720 && p.RetainDays >= 1 && p.RetainDays <= 3650 && p.MinFreeGiB >= 0 && p.MinFreeGiB <= 1048576 && p.MaxGiB >= 0 && p.MaxGiB <= 1048576
}

type DeviceSettingsCommand struct {
	Storage bool           `json:"storage,omitempty"`
	Policy  *StoragePolicy `json:"policy,omitempty"`

	Version  int64              `json:"version,omitempty"`
	Settings *DevicePreferences `json:"settings,omitempty"`
}

func DefaultDevicePreferences() DevicePreferences {
	return DevicePreferences{SessionIdleMinutes: 10, SessionMaxMinutes: 120, CodexArgs: []string{}, MinMemoryMiB: 300, LoadPolicy: "balanced"}
}

func ParseDeviceSettings(raw []byte) (DeviceSettingsCommand, error) {
	var v DeviceSettingsCommand
	invalid := errors.New("invalid device settings")
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if !json.Valid(raw) || d.Decode(&v) != nil || v.Version < 0 {
		return v, invalid
	}
	if v.Storage {
		if v.Settings != nil || (v.Policy == nil && v.Version != 0) || (v.Policy != nil && (v.Version < 1 || !v.Policy.Valid())) {
			return v, invalid
		}
		return v, nil
	}
	if v.Policy != nil {
		return v, invalid
	}
	p := v.Settings
	if p == nil {
		if v.Version != 0 {
			return v, invalid
		}
		return v, nil
	}
	if p.SessionIdleMinutes < 0 || p.SessionIdleMinutes > 1440 || p.SessionMaxMinutes < 0 || p.SessionMaxMinutes > 10080 {
		return v, invalid
	}
	if v.Version < 1 || len(p.CodexPath) > 1024 || strings.IndexFunc(p.CodexPath, unicode.IsControl) >= 0 || len(p.CodexArgs) > 16 ||
		p.MinMemoryMiB < 1 || p.MinMemoryMiB > 1048576 || (p.LoadPolicy != "balanced" && p.LoadPolicy != "performance" && p.LoadPolicy != "conservative") {
		return v, invalid
	}
	for _, a := range p.CodexArgs {
		if a == "" || len(a) > 512 || strings.IndexFunc(a, unicode.IsControl) >= 0 {
			return v, invalid
		}
	}
	return v, nil
}
