package daemon

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"openfox/internal/resourcepolicy"
)

type fakeRuntimeConfigClient struct {
	config resourcepolicy.Config
	acks   []int64
	ackErr error
}

func (c *fakeRuntimeConfigClient) RuntimeConfig(context.Context, string, string) (resourcepolicy.Config, error) {
	return c.config, nil
}

func (c *fakeRuntimeConfigClient) AckRuntimeConfig(_ context.Context, _, _ string, version int64, _ bool) error {
	c.acks = append(c.acks, version)
	return c.ackErr
}

func TestRuntimeConfigSyncAcksAndReportsRestart(t *testing.T) {
	client := &fakeRuntimeConfigClient{config: resourcepolicy.Config{ConfigVersion: 3, AppliedVersion: 2, Policy: resourcepolicy.DefaultPolicy()}}
	var published map[string]RuntimeConfigStatus
	sync := &RuntimeConfigSync{Client: client, Token: func(context.Context) (string, error) { return "t", nil },
		Publish: func(status map[string]RuntimeConfigStatus) { published = status }}
	sync.Sync(context.Background(), []string{"org-1"})
	if len(client.acks) != 1 || client.acks[0] != 3 || published["org-1"].AppliedVersion != 3 || published["org-1"].RestartRequired {
		t.Fatalf("first sync acks=%v status=%#v", client.acks, published)
	}
	// 已确认的版本不重复确认。
	client.config.AppliedVersion = 3
	sync.Sync(context.Background(), []string{"org-1"})
	if len(client.acks) != 1 {
		t.Fatalf("duplicate ack: %v", client.acks)
	}
	// 启动之后出现、且标记需重启的新版本 → restart_required。
	client.config = resourcepolicy.Config{ConfigVersion: 4, AppliedVersion: 3, RestartRequired: true, Policy: resourcepolicy.DefaultPolicy()}
	sync.Sync(context.Background(), []string{"org-1"})
	if !published["org-1"].RestartRequired || published["org-1"].AppliedVersion != 4 {
		t.Fatalf("restart status = %#v", published["org-1"])
	}
	// 需重启的状态保持到进程重启：之后出现不需要重启的新版本也不清除。
	client.config = resourcepolicy.Config{ConfigVersion: 5, AppliedVersion: 4, Policy: resourcepolicy.DefaultPolicy()}
	sync.Sync(context.Background(), []string{"org-1"})
	if !published["org-1"].RestartRequired || published["org-1"].AppliedVersion != 5 {
		t.Fatalf("restart must stay required until the daemon restarts: %#v", published["org-1"])
	}
	// 校验失败：上报 INVALID_POLICY，不确认，保留上次已应用版本。
	client.config = resourcepolicy.Config{ConfigVersion: 6, AppliedVersion: 5, Policy: resourcepolicy.Policy{}}
	sync.Sync(context.Background(), []string{"org-1"})
	if published["org-1"].Error != "INVALID_POLICY" || published["org-1"].AppliedVersion != 5 || len(client.acks) != 3 {
		t.Fatalf("invalid status = %#v acks=%v", published["org-1"], client.acks)
	}
	// 确认失败：ACK_FAILED。
	client.config = resourcepolicy.Config{ConfigVersion: 7, AppliedVersion: 5, Policy: resourcepolicy.DefaultPolicy()}
	client.ackErr = errors.New("network")
	sync.Sync(context.Background(), []string{"org-1"})
	if published["org-1"].Error != "ACK_FAILED" {
		t.Fatalf("ack failure status = %#v", published["org-1"])
	}
	// 组织被移除后不再上报它。
	sync.Sync(context.Background(), []string{})
	if _, ok := published["org-1"]; ok {
		t.Fatal("removed organization still reported")
	}
}

// 旧版中心没有设备侧运行配置端点：路由层返回不带错误码的 404 / 405，daemon 视为不支持，不上报状态（旧行为）。
func TestRuntimeConfigUnsupportedCenterIsSilent(t *testing.T) {
	for _, status := range []int{http.StatusNotFound, http.StatusMethodNotAllowed} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "not found", status)
		}))
		client := AuthClient{BaseURL: server.URL}
		if _, err := client.RuntimeConfig(context.Background(), "token", "org-1"); !errors.Is(err, ErrRuntimeConfigUnsupported) {
			t.Fatalf("status %d: err = %v", status, err)
		}
		published := map[string]RuntimeConfigStatus{"stale": {}}
		sync := &RuntimeConfigSync{Client: client, Token: func(context.Context) (string, error) { return "token", nil },
			Publish: func(next map[string]RuntimeConfigStatus) { published = next }}
		sync.Sync(context.Background(), []string{"org-1"})
		if len(published) != 0 {
			t.Fatalf("status %d: published = %#v", status, published)
		}
		server.Close()
	}
	// 带业务错误码的 404（例如授权已撤销）不是「不支持」。
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":{"code":"NOT_FOUND","message":"x"}}`))
	}))
	defer server.Close()
	if _, err := (AuthClient{BaseURL: server.URL}).RuntimeConfig(context.Background(), "token", "org-1"); err == nil || errors.Is(err, ErrRuntimeConfigUnsupported) {
		t.Fatalf("coded 404 err = %v", err)
	}
}
