package daemon

import (
	"bytes"
	"encoding/xml"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
)

var ErrStorageBusy = errors.New("storage_busy")
var ErrStorageTarget = errors.New("storage_target_invalid")
var ErrStorageVolume = errors.New("storage_different_volume")

var ErrServiceBusy = errors.New("daemon service settings are busy")
var ErrServiceUnavailable = errors.New("daemon service settings are unavailable")

type ServiceSettings struct {
	Supported bool   `json:"supported"`
	Installed bool   `json:"installed"`
	Startup   string `json:"startup"`
	KeepAlive bool   `json:"keep_alive"`
}

func (m Manager) label() string {
	if m.Label != "" {
		return m.Label
	}
	return LaunchAgentLabel
}

func (m Manager) supportsService() bool {
	return m.GOOS == "darwin" || (m.GOOS == "" && runtime.GOOS == "darwin")
}

func (m Manager) manualPath() string {
	if m.ManualAgentPath != "" {
		return m.ManualAgentPath
	}
	if m.SocketPath != "" {
		return filepath.Join(filepath.Dir(m.SocketPath), "daemon-service.plist")
	}
	return m.LaunchAgentPath + ".manual"
}

// servicePath preserves legacy automatic registration and never silently chooses
// between conflicting configurations left by an external writer.
func (m Manager) servicePath() (string, bool, error) {
	auto, err := serviceFileExists(m.LaunchAgentPath)
	if err != nil {
		return "", false, err
	}
	manual, err := serviceFileExists(m.manualPath())
	if err != nil || (auto && manual) {
		return "", false, ErrServiceUnavailable
	}
	if manual {
		return m.manualPath(), true, nil
	}
	return m.LaunchAgentPath, auto, nil
}

func serviceFileExists(path string) (bool, error) {
	if !filepath.IsAbs(path) {
		return false, ErrServiceUnavailable
	}
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if !info.Mode().IsRegular() {
		return false, ErrUnsafeFile
	}
	return true, nil
}

func (m Manager) serviceSettings() (ServiceSettings, error) {
	settings := ServiceSettings{Supported: m.supportsService()}
	if !settings.Supported {
		return settings, nil
	}
	path, exists, err := m.servicePath()
	if err != nil || !exists {
		return settings, err
	}
	data, err := readPrivateFile(path, maxConfigBytes)
	if err != nil {
		return settings, err
	}
	// Only move OpenFox's own user-private plist. Preserve its exact program and
	// config arguments rather than regenerating a remote registration as local.
	decoder := xml.NewDecoder(bytes.NewReader(data))
	key, label, keepAlive, runAtLoad := "", "", false, false
	for {
		token, err := decoder.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return settings, ErrServiceUnavailable
		}
		start, ok := token.(xml.StartElement)
		if !ok {
			continue
		}
		if start.Name.Local == "key" {
			if decoder.DecodeElement(&key, &start) != nil {
				return settings, ErrServiceUnavailable
			}
		} else if key == "Label" && start.Name.Local == "string" {
			if decoder.DecodeElement(&label, &start) != nil {
				return settings, ErrServiceUnavailable
			}
			key = ""
		} else if start.Name.Local == "true" {
			if key == "KeepAlive" {
				keepAlive = true
			}
			if key == "RunAtLoad" {
				runAtLoad = true
			}
			key = ""
		}
	}
	if label != m.label() || !keepAlive || !runAtLoad {
		return settings, ErrServiceUnavailable
	}
	settings.Installed, settings.KeepAlive, settings.Startup = true, true, "login"
	if path == m.manualPath() {
		settings.Startup = "manual"
	}
	return settings, nil
}

func (m Manager) ServiceSettings() (ServiceSettings, error) {
	var result ServiceSettings
	err := m.withServiceLock(func() error { var err error; result, err = m.serviceSettings(); return err })
	return result, err
}

// SetStartup changes only the location scanned at the next login. It does not
// bootout, bootstrap, signal or otherwise disturb an already running job.
func (m Manager) SetStartup(mode string) (ServiceSettings, error) {
	var result ServiceSettings
	if mode != "login" && mode != "manual" {
		return result, errors.New("invalid startup mode")
	}
	err := m.withServiceLock(func() error {
		current, err := m.serviceSettings()
		if err != nil {
			return err
		}
		if !current.Supported || !current.Installed {
			return ErrServiceUnavailable
		}
		if current.Startup == mode {
			result = current
			return nil
		}
		source, target := m.LaunchAgentPath, m.manualPath()
		if mode == "login" {
			source, target = target, source
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
			return err
		}
		if !checkedDirectory(filepath.Dir(target)) {
			return ErrUnsafeFile
		}
		// Same-user home directories share a filesystem. Rename is atomic; on a
		// cross-device or permission failure the original file stays in place.
		if err := os.Rename(source, target); err != nil {
			return err
		}
		result = current
		result.Startup = mode
		return nil
	})
	return result, err
}
