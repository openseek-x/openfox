package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"net/url"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"openfox/internal/daemon"
	"openfox/internal/devices"
)

// Each command is scoped to the expected registration. Only move-storage
// accepts a directory, selected by the native desktop picker.
func runLocalService(args []string, manager daemon.Manager, output io.Writer) error {
	return runLocalServiceInput(args, manager, strings.NewReader(""), output)
}

func runLocalServiceInput(args []string, manager daemon.Manager, input io.Reader, output io.Writer) error {
	flags := flag.NewFlagSet(args[0], flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	org := flags.String("organization-id", "", "expected organization")
	device := flags.String("device-id", "", "expected device")
	center := flags.String("center-url", "", "expected center")
	mode := flags.String("mode", "", "login or manual")
	parent := flags.String("parent", "", "new parent of the OpenFox data directory")
	cursor := flags.String("cursor", "", "opaque log cursor")
	action := flags.String("action", "", "start, stop or restart")
	if flags.Parse(args[1:]) != nil || flags.NArg() != 0 {
		return errors.New("invalid local service request")
	}
	if (args[0] != "move-storage" && *parent != "") || (args[0] != "startup" && *mode != "") || (args[0] != "logs" && *cursor != "") || (args[0] != "service-action" && *action != "") {
		return errors.New("invalid local service request")
	}
	configPath := filepath.Join(filepath.Dir(manager.SocketPath), daemon.DeviceConfigFileName)
	config, err := daemon.LoadDeviceConfig(configPath)
	if err != nil {
		return errors.New("local registration is unavailable")
	}
	actualCenter, err := devices.NormalizeCenterURL(config.CenterURL)
	requestedCenter, requestErr := devices.NormalizeCenterURL(*center)
	if err != nil || requestErr != nil || !sameLocalCenter(actualCenter, requestedCenter) || config.DeviceID != strings.ToLower(*device) || config.OrganizationID != strings.ToLower(*org) {
		return errors.New("local registration does not match")
	}
	manager = manager.ForRegistration(configPath)
	var result any
	switch args[0] {
	case "proxy-status", "proxy-save":
		goos := manager.GOOS
		if goos == "" {
			goos = runtime.GOOS
		}
		if goos != "darwin" {
			return errors.New("unsupported local proxy request")
		}
		store := daemon.MachineProxyStore{ConfigPath: configPath, Scope: daemon.ProxyScope(config)}
		view := store.View()
		if args[0] == "proxy-save" {
			body, readErr := io.ReadAll(io.LimitReader(input, daemon.MaxMachineProxyBytes+1))
			if readErr != nil || len(body) > daemon.MaxMachineProxyBytes {
				return daemon.ErrMachineProxyInvalid
			}
			var in daemon.MachineProxySave
			decoder := json.NewDecoder(strings.NewReader(string(body)))
			decoder.DisallowUnknownFields()
			if decoder.Decode(&in) != nil || decoder.Decode(new(any)) != io.EOF {
				return daemon.ErrMachineProxyInvalid
			}
			view, err = store.Save(context.Background(), in)
		}
		if err != nil {
			return err
		}
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		status, _ := manager.QueryStatus(ctx)
		result = struct {
			daemon.MachineProxyView
			Runtime *daemon.MachineProxyStatus `json:"runtime,omitempty"`
		}{view, status.MachineProxy}
	case "move-storage", "service-status", "startup":
		var service daemon.ServiceSettings
		if args[0] == "startup" {
			service, err = manager.SetStartup(*mode)
		} else {
			service, err = manager.ServiceSettings()
		}
		if args[0] == "move-storage" && errors.Is(err, daemon.ErrServiceBusy) {
			return daemon.ErrStorageBusy
		}
		if err != nil {
			return err
		}
		location, err := manager.StorageLocation()
		if err != nil {
			return err
		}
		if args[0] == "move-storage" {
			if !service.Installed || !filepath.IsAbs(*parent) {
				return daemon.ErrStorageTarget
			}
			location, err = manager.RelocateStorage(*parent, config)
			if err != nil {
				return err
			}
		}
		result = struct {
			daemon.ServiceSettings
			StoragePath string `json:"storage_path"`
			LogPath     string `json:"log_path"`
		}{service, location, manager.LogPath}
	case "logs":
		result, err = manager.ServiceLogs(*cursor)
	case "service-action":
		settings, settingsErr := manager.ServiceSettings()
		if settingsErr != nil || !settings.Supported || !settings.Installed {
			return daemon.ErrServiceUnavailable
		}
		ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		defer cancel()
		switch *action {
		case "start":
			err = manager.Start(ctx)
		case "stop":
			err = manager.Stop(ctx)
		case "restart":
			err = manager.Restart(ctx)
		default:
			return errors.New("invalid daemon action")
		}
		result = map[string]bool{"ok": err == nil}
	default:
		return errors.New("unsupported local service request")
	}
	if err != nil {
		return err
	}
	return json.NewEncoder(output).Encode(result)
}

// Keep the base path in the identity, allowing only equivalent hostname casing,
// default ports and trailing slashes (the renderer/main use URL for these).
func sameLocalCenter(left, right string) bool {
	a, errA := url.Parse(left)
	b, errB := url.Parse(right)
	if errA != nil || errB != nil {
		return false
	}
	port := func(u *url.URL) string {
		if p := u.Port(); p != "" {
			return p
		}
		if u.Scheme == "https" {
			return "443"
		}
		return "80"
	}
	return a.Scheme == b.Scheme && strings.EqualFold(a.Hostname(), b.Hostname()) && port(a) == port(b) && strings.TrimRight(a.EscapedPath(), "/") == strings.TrimRight(b.EscapedPath(), "/")
}
