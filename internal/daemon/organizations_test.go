package daemon

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
)

// 阶段 12e（D-09）：本机多组织记录的新旧兼容与刷新。

func TestLoadOrganizationsLegacyConfigIsSingleOrganization(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "config")
	config := testConfig(dir)
	path := filepath.Join(dir, DeviceConfigFileName)
	if err := WriteDeviceConfig(path, config); err != nil {
		t.Fatal(err)
	}
	original, _ := os.ReadFile(path)
	if got := LoadOrganizations(path, config); len(got) != 1 || got[0] != "org-1" {
		t.Fatalf("legacy organizations = %v", got)
	}
	// 损坏的旁路文件：仍视为单组织。
	if err := WritePrivateFile(OrganizationsPath(path), []byte("{not json")); err != nil {
		t.Fatal(err)
	}
	if got := LoadOrganizations(path, config); len(got) != 1 {
		t.Fatalf("corrupt sidecar organizations = %v", got)
	}
	second := "11111111-2222-3333-4444-555555555555"
	if err := WriteOrganizations(path, []string{second, "ORG-1", second, "not-a-uuid"}); err != nil {
		t.Fatal(err)
	}
	got := LoadOrganizations(path, config)
	if len(got) != 2 || got[0] != "org-1" || got[1] != second {
		t.Fatalf("merged organizations = %v", got)
	}
	if !OrganizationsContain(got, "11111111-2222-3333-4444-555555555555") || OrganizationsContain(got, "") {
		t.Fatal("OrganizationsContain mismatch")
	}
	// config.json 从不被改写（旧版本 daemon 回退后仍能读取，只认第一个组织）。
	current, _ := os.ReadFile(path)
	if !bytes.Equal(original, current) {
		t.Fatal("config.json was rewritten")
	}
	if loaded, err := LoadDeviceConfig(path); err != nil || loaded.OrganizationID != "org-1" {
		t.Fatalf("legacy reader after sidecar = %#v, %v", loaded, err)
	}
}

type fakeOrganizationsClient struct {
	ids []string
	err error
}

func (c fakeOrganizationsClient) DeviceOrganizations(context.Context, string) ([]string, error) {
	return c.ids, c.err
}

func TestOrganizationRefresherWritesSidecarAndNotifies(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "config")
	config := testConfig(dir)
	path := filepath.Join(dir, DeviceConfigFileName)
	if err := WriteDeviceConfig(path, config); err != nil {
		t.Fatal(err)
	}
	ids := []string{"11111111-2222-3333-4444-555555555555", "aaaaaaaa-2222-3333-4444-555555555555"}
	var notified []string
	refresher := &OrganizationRefresher{ConfigPath: path, Client: fakeOrganizationsClient{ids: ids},
		Token: func(context.Context) (string, error) { return "t", nil }, OnChange: func(next []string) { notified = next }}
	if _, err := refresher.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(notified) != 2 {
		t.Fatalf("notified = %v", notified)
	}
	if got := LoadOrganizations(path, config); len(got) != 3 {
		t.Fatalf("after refresh = %v", got)
	}
	unsupported := &OrganizationRefresher{ConfigPath: path, Client: fakeOrganizationsClient{err: ErrOrganizationsUnsupported},
		Token: func(context.Context) (string, error) { return "t", nil }}
	done := make(chan struct{})
	go func() { unsupported.Run(context.Background()); close(done) }()
	<-done // 旧版中心：Run 立即停止，不反复请求。
}
