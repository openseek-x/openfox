package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

func proxyStoreFixture(t *testing.T) (MachineProxyStore, DeviceConfig) {
	t.Helper()
	dir := t.TempDir()
	c := DeviceConfig{Version: 1, CenterURL: "https://CENTER.example:443/base/", WSSURL: "wss://center.example/base/api/v1/device/connect", DeviceID: "device", OrganizationID: "org", KeyID: "key", PrivateKeyFile: filepath.Join(dir, "key"), TokenFile: filepath.Join(dir, "token"), WorkspaceRoot: filepath.Join(dir, "workspace"), CenterEpoch: 1}
	p := filepath.Join(dir, DeviceConfigFileName)
	if err := WriteDeviceConfig(p, c); err != nil {
		t.Fatal(err)
	}
	return MachineProxyStore{ConfigPath: p, Scope: ProxyScope(c)}, c
}

func proxyChoice(s MachineProxyStore, revision uint64, port int) MachineProxySave {
	return MachineProxySave{ExpectedRevision: revision, ScopeToken: s.Scope.Token(), Mode: "CUSTOM", Effective: &MachineProxyEffective{Mode: "proxy", Protocol: "http", Host: "127.0.0.1", Port: port}}
}

func TestMachineProxyStoreCASAndIsolation(t *testing.T) {
	s, config := proxyStoreFixture(t)
	if env, err := s.Environment(); env != nil || err != nil {
		t.Fatal(env, err)
	}
	if s.Scope.CenterURL != "https://center.example/base" {
		t.Fatal(s.Scope)
	}
	ctx := context.Background()
	var wg sync.WaitGroup
	var successes, conflicts atomic.Int32
	for _, port := range []int{12345, 12346} {
		wg.Add(1)
		go func(port int) {
			defer wg.Done()
			_, err := s.Save(ctx, proxyChoice(s, 0, port))
			if err == nil {
				successes.Add(1)
			} else if errors.Is(err, ErrMachineProxyConflict) {
				conflicts.Add(1)
			} else {
				t.Errorf("save: %v", err)
			}
		}(port)
	}
	wg.Wait()
	if successes.Load() != 1 || conflicts.Load() != 1 {
		t.Fatal(successes.Load(), conflicts.Load())
	}
	v := s.View()
	duplicate, err := s.Save(ctx, proxyChoice(s, 0, v.Effective.Port))
	if err != nil || duplicate.Revision != 1 {
		t.Fatal(duplicate, err)
	}
	info, err := os.Stat(filepath.Join(filepath.Dir(s.ConfigPath), MachineProxyFileName))
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal(info, err)
	}
	env, err := s.Environment()
	if err != nil || len(env) != 8 || env["HTTP_PROXY"] != env["http_proxy"] || env["ALL_PROXY"] != "" || env["NO_PROXY"] != "localhost,127.0.0.1,::1" {
		t.Fatal(env, err)
	}
	// A newly registered identity cannot read the previous registration's host.
	config.KeyID = "replacement"
	if err := WriteDeviceConfig(s.ConfigPath, config); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Environment(); !errors.Is(err, ErrMachineProxyScope) {
		t.Fatal(err)
	}
	replacement := MachineProxyStore{ConfigPath: s.ConfigPath, Scope: ProxyScope(config)}
	v = replacement.View()
	if v.Problem != "machine_proxy_registration_changed" || v.Effective != nil || v.Revision != 1 {
		t.Fatal(v)
	}
	if _, err := replacement.Save(ctx, MachineProxySave{ExpectedRevision: v.Revision, ScopeToken: v.ScopeToken, Mode: "DISABLED"}); err != nil {
		t.Fatal(err)
	}
	if env, err := replacement.Environment(); env != nil || err != nil {
		t.Fatal(env, err)
	}
}

func TestMachineProxyFollowCannotOverwriteCustom(t *testing.T) {
	s, _ := proxyStoreFixture(t)
	ctx := context.Background()
	follow := MachineProxySave{ScopeToken: s.Scope.Token(), Mode: "FOLLOW_DESKTOP", Effective: &MachineProxyEffective{Mode: "direct"}, SourceFingerprint: strings.Repeat("a", 64)}
	if _, err := s.Save(ctx, follow); err != nil {
		t.Fatal(err)
	}
	env, err := s.Environment()
	if err != nil || len(env) != 8 {
		t.Fatal(env, err)
	}
	for _, v := range env {
		if v != "" {
			t.Fatal(env)
		}
	}
	if _, err := s.Save(ctx, proxyChoice(s, 1, 12345)); err != nil {
		t.Fatal(err)
	}
	follow.ExpectedRevision, follow.FollowUpdate = 1, true
	if _, err := s.Save(ctx, follow); !errors.Is(err, ErrMachineProxyConflict) {
		t.Fatal(err)
	}
	if s.View().Mode != "CUSTOM" {
		t.Fatal(s.View())
	}
}

func TestMachineProxyRejectsUnsafeAndInvalidConfiguration(t *testing.T) {
	s, _ := proxyStoreFixture(t)
	path := filepath.Join(filepath.Dir(s.ConfigPath), MachineProxyFileName)
	for _, mutate := range []func(*MachineProxySave){
		func(v *MachineProxySave) { v.Effective.Host = "user:password@host" },
		func(v *MachineProxySave) { v.Effective.Protocol = "socks5" },
		func(v *MachineProxySave) { v.Effective.Port = 65536 },
		func(v *MachineProxySave) { v.Effective.Host = "[not-ip]" },
		func(v *MachineProxySave) { v.Mode = "FOLLOW_DESKTOP" },
		func(v *MachineProxySave) { v.ScopeToken = strings.Repeat("f", 64) },
	} {
		in := proxyChoice(s, 0, 12345)
		mutate(&in)
		if _, err := s.Save(context.Background(), in); !errors.Is(err, ErrMachineProxyInvalid) {
			t.Fatal(err)
		}
	}
	for _, content := range []string{"{}", strings.Repeat("x", MaxMachineProxyBytes+1)} {
		if err := os.WriteFile(path, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := s.Environment(); !errors.Is(err, ErrMachineProxyInvalid) {
			t.Fatal(err)
		}
		if v := s.View(); v.Problem == "" || v.Effective != nil {
			t.Fatal(v)
		}
	}
	// An explicit repair is allowed; auto-follow may not repair malformed state.
	if _, err := s.Save(context.Background(), proxyChoice(s, 0, 12345)); err != nil {
		t.Fatal(err)
	}
	var stored MachineProxyPolicy
	b, _ := os.ReadFile(path)
	json.Unmarshal(b, &stored)
	if stored.Revision != 1 {
		t.Fatal(stored)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(filepath.Dir(path), "untouched")
	os.WriteFile(target, []byte("unchanged"), 0600)
	os.Symlink(target, path)
	if _, err := s.Save(context.Background(), proxyChoice(s, 0, 12345)); !errors.Is(err, ErrMachineProxyInvalid) {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(target); string(b) != "unchanged" {
		t.Fatal(string(b))
	}
}

func TestMachineProxyNetworkSnapshotAndFailure(t *testing.T) {
	s, _ := proxyStoreFixture(t)
	ctx := context.Background()
	var callsA, callsB atomic.Int32
	makeProxy := func(counter *atomic.Int32) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			counter.Add(1)
			if r.URL.Host != "synthetic.invalid" {
				t.Errorf("target %s", r.URL.Host)
			}
			io.WriteString(w, "fixture")
		}))
	}
	a, b := makeProxy(&callsA), makeProxy(&callsB)
	defer a.Close()
	defer b.Close()
	port := func(raw string) int { u, _ := url.Parse(raw); n, _ := strconv.Atoi(u.Port()); return n }
	if _, err := s.Save(ctx, proxyChoice(s, 0, port(a.URL))); err != nil {
		t.Fatal(err)
	}
	n := NewMachineProxyNetwork(s)
	defer n.Close()
	old, rev, err := n.snapshot()
	if err != nil || rev != 1 {
		t.Fatal(rev, err)
	}
	request := func(client *http.Client) error {
		resp, err := client.Get("http://synthetic.invalid/path")
		if err == nil {
			io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
		}
		return err
	}
	if err := request(n.Client()); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Save(ctx, proxyChoice(s, 1, port(b.URL))); err != nil {
		t.Fatal(err)
	}
	if err := request(n.Client()); err != nil {
		t.Fatal(err)
	}
	if err := request(&http.Client{Transport: old}); err != nil {
		t.Fatal(err)
	}
	if callsA.Load() != 2 || callsB.Load() != 1 {
		t.Fatal(callsA.Load(), callsB.Load())
	}
	if v := n.Status(); v.LoadedRevision == nil || *v.LoadedRevision != 2 {
		t.Fatal(v)
	}
	b.Close()
	if err := request(n.Client()); err == nil || !errors.Is(err, ErrMachineProxyRequest) || strings.Contains(err.Error(), b.URL) {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(filepath.Dir(s.ConfigPath), MachineProxyFileName), []byte("bad"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := request(n.Client()); !errors.Is(err, ErrMachineProxyInvalid) {
		t.Fatal(err)
	}
	if callsA.Load() != 2 || callsB.Load() != 1 {
		t.Fatal("unexpected fallback")
	}
}
