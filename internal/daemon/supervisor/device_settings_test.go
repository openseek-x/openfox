package supervisor

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"openfox/internal/runtimepolicy"
)

type recordedPath struct{ value string }

func (p *recordedPath) UseDevicePath(value string) { p.value = value }

func (p *recordedPath) ValidateDevicePath(_ context.Context, value string) (string, error) {
	if value == "/invalid" {
		return "", errors.New("invalid")
	}
	return value, nil
}

func TestRuntimePathSaveRejectReloadAndReset(t *testing.T) {
	ctx := t.Context()
	db, err := OpenJournal(ctx, filepath.Join(t.TempDir(), "journal.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	path := new(recordedPath)
	s := &Supervisor{config: Config{Adapter: &fakeAdapter{}, Journal: db, DiscoveryPaths: map[string]DevicePathSettings{"cursor": path}}}
	if err := s.loadDeviceSettings(); err != nil {
		t.Fatal(err)
	}
	save := func(ver int64, value string) error {
		prefs := s.DeviceSettings().Settings
		prefs.RuntimePaths = map[string]string{"cursor": value}
		_, err := s.ConfigureDevice(ctx, runtimepolicy.DeviceSettingsCommand{Version: ver, Settings: &prefs})
		return err
	}
	if err := save(1, "/known"); err != nil {
		t.Fatal(err)
	}
	if save(2, "/invalid") == nil || save(1, "") == nil {
		t.Fatal("应拒绝无效候选和过期修改")
	}
	path.value = ""
	if err := s.loadDeviceSettings(); err != nil {
		t.Fatal(err)
	}
	if path.value != "/known" || s.DeviceSettings().Version != 2 || s.adapterFor("cursor") != nil {
		t.Fatal("重载应保留手动选择，Cursor 不可执行")
	}
	if err := save(2, ""); err != nil {
		t.Fatal(err)
	}
	if path.value != "" || len(s.DeviceSettings().Settings.RuntimePaths) != 0 {
		t.Fatal("未恢复自动发现")
	}
}
