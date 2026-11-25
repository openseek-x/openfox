package supervisor

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"maps"

	"openfox/internal/runtimepolicy"
)

type DeviceSettings struct {
	Version  int64                           `json:"version"`
	Settings runtimepolicy.DevicePreferences `json:"settings"`
}

type DevicePathSettings interface {
	ValidateDevicePath(context.Context, string) (string, error)
	UseDevicePath(string)
}

func (s *Supervisor) loadDeviceSettings() error {
	s.preferences = DeviceSettings{Version: 1, Settings: runtimepolicy.DefaultDevicePreferences()}
	var data string
	err := s.config.Journal.db.QueryRow("SELECT version,settings FROM device_preferences WHERE id=1").Scan(&s.preferences.Version, &data)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if err = json.Unmarshal([]byte(data), &s.preferences.Settings); err != nil {
		return err
	}
	for name, path := range s.preferences.Settings.RuntimePaths {
		if a := s.pathSettings(name); a != nil {
			a.UseDevicePath(path)
		}
	}
	if a := s.pathSettings("codex"); a != nil {
		a.UseDevicePath(s.preferences.Settings.CodexPath)
	}
	return nil
}

func (s *Supervisor) pathSettings(name string) DevicePathSettings {
	if a, ok := s.adapterFor(name).(DevicePathSettings); ok {
		return a
	}
	return s.config.DiscoveryPaths[name]
}

func (s *Supervisor) DeviceSettings() DeviceSettings {
	s.prefsMu.RLock()
	defer s.prefsMu.RUnlock()
	v := s.preferences
	v.Settings.RuntimePaths = maps.Clone(v.Settings.RuntimePaths)
	return v
}

func (s *Supervisor) ConfigureDevice(ctx context.Context, cmd runtimepolicy.DeviceSettingsCommand) (DeviceSettings, error) {
	if cmd.Settings == nil {
		return s.DeviceSettings(), nil
	}
	next := *cmd.Settings
	if _, dropped := sanitizeCustomArgs(next.CodexArgs); len(dropped) > 0 {
		return DeviceSettings{}, errors.New("unsupported runtime arguments")
	}
	current := s.DeviceSettings()
	if next.SessionIdleMinutes == 0 {
		next.SessionIdleMinutes = current.Settings.SessionIdleMinutes
	}
	if next.SessionMaxMinutes == 0 {
		next.SessionMaxMinutes = current.Settings.SessionMaxMinutes
	}
	if current.Version != cmd.Version {
		return DeviceSettings{}, errors.New("settings changed")
	}
	changes := map[string]string{}
	if next.CodexPath != current.Settings.CodexPath {
		changes["codex"] = next.CodexPath
	}
	for name, path := range next.RuntimePaths {
		if path != current.Settings.RuntimePaths[name] {
			changes[name] = path
		}
	}
	next.RuntimePaths = maps.Clone(current.Settings.RuntimePaths)
	for name, path := range changes {
		a := s.pathSettings(name)
		if a == nil {
			return DeviceSettings{}, errors.New("runtime unavailable")
		}
		validated, err := a.ValidateDevicePath(ctx, path)
		if err != nil {
			return DeviceSettings{}, err
		}
		changes[name] = validated
		if name == "codex" {
			next.CodexPath = validated
		} else if validated == "" {
			delete(next.RuntimePaths, name)
		} else {
			if next.RuntimePaths == nil {
				next.RuntimePaths = map[string]string{}
			}
			next.RuntimePaths[name] = validated
		}
	}
	s.prefsMu.Lock()
	defer s.prefsMu.Unlock()
	if s.preferences.Version != cmd.Version {
		return DeviceSettings{}, errors.New("settings changed")
	}
	data, err := json.Marshal(next)
	if err != nil {
		return DeviceSettings{}, err
	}
	result, err := s.config.Journal.db.ExecContext(ctx, `INSERT INTO device_preferences(id,version,settings) VALUES(1,?,?) ON CONFLICT(id) DO UPDATE SET settings=excluded.settings,version=excluded.version WHERE device_preferences.version=?`, cmd.Version+1, string(data), cmd.Version)
	if err != nil {
		return DeviceSettings{}, err
	}
	if n, err := result.RowsAffected(); err != nil || n != 1 {
		return DeviceSettings{}, errors.New("settings changed")
	}
	for name, path := range changes {
		s.pathSettings(name).UseDevicePath(path)
	}
	s.preferences = DeviceSettings{Version: cmd.Version + 1, Settings: next}
	return s.preferences, nil
}
