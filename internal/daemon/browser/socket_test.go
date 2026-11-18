package browser

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"image"
	"image/png"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	scope "openfox/internal/browser"
	"openfox/internal/runtime"
)

func TestPrivateSocketNativeImageAndDisconnectedHost(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "ofb-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	path := filepath.Join(dir, "browser.sock")
	c := &fakeCenter{binding: scope.Binding{ID: bindingID, OrganizationID: org, ConversationID: conversation, OwnerUserID: owner, DeviceID: device, HostInstanceID: hostID, HostEpoch: 1, Revision: 1, LeaseExpiresAt: time.Now().Add(time.Minute)}}
	b := New(c, device)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ended := make(chan error, 1)
	go func() { ended <- b.Serve(ctx, path) }()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-ended:
			if err != nil {
				t.Error(err)
			}
		case <-time.After(2 * time.Second):
			t.Error("socket server did not stop")
		}
	})
	var conn net.Conn
	deadline := time.Now().Add(2 * time.Second)
	for {
		conn, err = net.DialTimeout("unix", path, 100*time.Millisecond)
		if err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal(err)
		}
		time.Sleep(time.Millisecond)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("socket is not private")
	}
	if err := New(c, device).Serve(ctx, path); err == nil {
		t.Fatal("active listener was replaced")
	}
	first := frame{Type: "attach", Attach: &attachInput{OrganizationID: org, BindingID: bindingID, ClaimSecret: "fixture", Identity: scope.Identity{HostInstanceID: hostID, HostEpoch: 1, Revision: 1}}}
	if err := writeFrame(conn, first); err != nil {
		t.Fatal(err)
	}
	attached, err := readFrame(conn, 8192)
	if err != nil || attached.Type != "attached" || attached.Binding.ID != bindingID {
		t.Fatalf("attach: %+v %v", attached, err)
	}
	if err := writeFrame(conn, frame{Type: "grant", ID: "grant-1", Grant: &Grant{RunID: runID, Generation: 1, Epoch: 1, Origins: []string{"https://example.test"}}}); err != nil {
		t.Fatal(err)
	}
	ack, err := readFrame(conn, 8192)
	if err != nil || ack.Type != "grant-result" || ack.Error != "" {
		t.Fatalf("grant: %+v %v", ack, err)
	}
	run := Run{OrganizationID: org, ConversationID: conversation, ActorID: owner, ID: runID, Generation: 1}
	result := make(chan runtime.ToolResult, 1)
	go func() {
		result <- b.Call(ctx, run, "openfox_browser_observe", "screenshot", json.RawMessage(`{"action":"screenshot","tab_id":"one"}`))
	}()
	request, err := readFrame(conn, maxFrame)
	if err != nil || request.Type != "execute" || request.Request.RunID != runID || request.Request.ControlEpoch != 1 {
		t.Fatalf("execute: %+v %v", request, err)
	}
	var data bytes.Buffer
	_ = png.Encode(&data, image.NewRGBA(image.Rect(0, 0, 2, 2)))
	if err := writeFrame(conn, frame{Type: "result", ID: request.ID, Result: &runtime.ToolResult{Text: "snapshot", OK: true, Image: &runtime.ToolImage{MIMEType: "image/png", Data: data.Bytes()}}}); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-result:
		if !got.OK || got.Image == nil || !bytes.Equal(got.Image.Data, data.Bytes()) {
			t.Fatal("native image lost")
		}
	case <-time.After(time.Second):
		t.Fatal("missing result")
	}
	_ = conn.Close()
	deadline = time.Now().Add(time.Second)
	for {
		b.mu.Lock()
		n := len(b.hosts)
		b.mu.Unlock()
		if n == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("disconnected host retained")
		}
		time.Sleep(time.Millisecond)
	}
	if result := act(b, run, "later"); result.Text != "UNAVAILABLE" {
		t.Fatal(result)
	}
}

func TestLocalProtocolRejectsOversizedAndUnknownFields(t *testing.T) {
	var prefix [4]byte
	binary.BigEndian.PutUint32(prefix[:], maxFrame+1)
	if _, err := readFrame(bytes.NewReader(prefix[:]), maxFrame); err == nil {
		t.Fatal("oversized allocation accepted")
	}
	for _, body := range []string{`{"version":2,"type":"ping"}`, `{"version":1,"type":"ping","eval":"code"}`, `{"version":1,"type":"ping"} {}`} {
		binary.BigEndian.PutUint32(prefix[:], uint32(len(body)))
		if _, err := readFrame(bytes.NewReader(append(prefix[:], []byte(body)...)), maxFrame); err == nil {
			t.Fatal("invalid local frame accepted")
		}
	}
}

func TestBrokerStartupCleansOnlyManagedStaleFiles(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "ofb-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	outside := t.TempDir()
	keep := filepath.Join(outside, "keep")
	if err := os.WriteFile(keep, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(dir, "transfers")); err != nil {
		t.Fatal(err)
	}
	old := filepath.Join(dir, "downloads", "old")
	if err := os.MkdirAll(old, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(old, "shot"), []byte("old"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "unrelated"), []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ended := make(chan error, 1)
	socket := filepath.Join(dir, "browser.sock")
	go func() { ended <- New(nil, device).Serve(ctx, socket) }()
	defer func() {
		cancel()
		select {
		case err := <-ended:
			if err != nil {
				t.Error(err)
			}
		case <-time.After(3 * time.Second):
			t.Error("broker did not stop")
		}
	}()
	deadline := time.Now().Add(2 * time.Second)
	for {
		_, err := os.Lstat(filepath.Join(dir, "downloads"))
		if os.IsNotExist(err) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("stale files not cleaned")
		}
		time.Sleep(time.Millisecond)
	}
	if _, err := os.Lstat(filepath.Join(dir, "transfers")); !os.IsNotExist(err) {
		t.Fatal("stale symlink not removed")
	}
	if data, _ := os.ReadFile(keep); string(data) != "keep" {
		t.Fatal("cleanup followed symlink")
	}
	if data, _ := os.ReadFile(filepath.Join(dir, "unrelated")); string(data) != "keep" {
		t.Fatal("cleanup removed unrelated data")
	}
	if err := os.Mkdir(filepath.Join(dir, "downloads"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "downloads", "active"), []byte("new"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := New(nil, device).Serve(context.Background(), socket); err == nil {
		t.Fatal("second broker accepted")
	}
	if data, _ := os.ReadFile(filepath.Join(dir, "downloads", "active")); string(data) != "new" {
		t.Fatal("second broker cleaned active data")
	}
}

func TestSocketDoesNotReplaceRegularFilesOrUsePublicDirectory(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "ofb-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	path := filepath.Join(dir, "browser.sock")
	if err := os.WriteFile(path, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := New(nil, device).Serve(context.Background(), path); err == nil {
		t.Fatal("regular file overwritten")
	}
	if data, _ := os.ReadFile(path); string(data) != "keep" {
		t.Fatal("existing data modified")
	}
	_ = os.Chmod(dir, 0755)
	if err := New(nil, device).Serve(context.Background(), filepath.Join(dir, "other.sock")); err == nil {
		t.Fatal("public socket directory accepted")
	}
}
