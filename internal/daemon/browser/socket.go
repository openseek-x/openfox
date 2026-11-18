package browser

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	scope "openfox/internal/browser"
	"openfox/internal/runtime"
)

const maxFrame = 6 * 1024 * 1024 // One bounded image plus JSON/base64 overhead.

type attachInput struct {
	OrganizationID string `json:"organization_id"`
	BindingID      string `json:"binding_id"`
	ClaimSecret    string `json:"claim_secret"`
	scope.Identity
}
type frame struct {
	Version int                 `json:"version"`
	Type    string              `json:"type"`
	ID      string              `json:"id,omitempty"`
	Attach  *attachInput        `json:"attach,omitempty"`
	Grant   *Grant              `json:"grant,omitempty"`
	Request *Request            `json:"request,omitempty"`
	Result  *runtime.ToolResult `json:"result,omitempty"`
	Binding *scope.Binding      `json:"binding,omitempty"`
	Error   string              `json:"error,omitempty"`
}

func readFrame(reader io.Reader, limit uint32) (frame, error) {
	var length [4]byte
	if _, err := io.ReadFull(reader, length[:]); err != nil {
		return frame{}, err
	}
	size := binary.BigEndian.Uint32(length[:])
	if size == 0 || size > limit {
		return frame{}, ErrProtocol
	}
	payload := make([]byte, size)
	if _, err := io.ReadFull(reader, payload); err != nil {
		return frame{}, err
	}
	var value frame
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if !json.Valid(payload) || decoder.Decode(&value) != nil || value.Version != 1 || len(value.ID) > 128 {
		return frame{}, ErrProtocol
	}
	return value, nil
}

func writeFrame(writer io.Writer, value frame) error {
	value.Version = 1
	payload, err := json.Marshal(value)
	if err != nil {
		return err
	}
	if len(payload) > maxFrame {
		return ErrProtocol
	}
	var length [4]byte
	binary.BigEndian.PutUint32(length[:], uint32(len(payload)))
	_, err = io.Copy(writer, io.MultiReader(bytes.NewReader(length[:]), bytes.NewReader(payload)))
	return err
}

type socketHost struct {
	conn     net.Conn
	writeMu  sync.Mutex
	mu       sync.Mutex
	pending  map[string]chan runtime.ToolResult
	sequence atomic.Uint64
	closed   chan struct{}
	once     sync.Once
}

func (h *socketHost) send(value frame) error {
	h.writeMu.Lock()
	defer h.writeMu.Unlock()
	_ = h.conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
	if err := writeFrame(h.conn, value); err != nil {
		h.Close()
		return err
	}
	return nil
}
func (h *socketHost) Close() { h.once.Do(func() { close(h.closed); _ = h.conn.Close() }) }
func (h *socketHost) Revoke(epoch int64) {
	_ = h.send(frame{Type: "revoked", Grant: &Grant{Epoch: epoch}})
}
func (h *socketHost) Execute(ctx context.Context, request Request) runtime.ToolResult {
	id := strconv.FormatUint(h.sequence.Add(1), 10)
	reply := make(chan runtime.ToolResult, 1)
	h.mu.Lock()
	h.pending[id] = reply
	h.mu.Unlock()
	defer func() { h.mu.Lock(); delete(h.pending, id); h.mu.Unlock() }()
	if ctx.Err() != nil {
		return denied("CONTROL_REVOKED")
	}
	if h.send(frame{Type: "execute", ID: id, Request: &request}) != nil {
		return denied("OUTCOME_UNKNOWN")
	}
	select {
	case result := <-reply:
		return result
	case <-h.closed:
		return denied("OUTCOME_UNKNOWN")
	case <-ctx.Done():
		_ = h.send(frame{Type: "cancel", ID: id})
		return denied("OUTCOME_UNKNOWN")
	}
}

// Serve uses a dedicated private directory. It never replaces an active
// listener or follows an existing symlink. There is no TCP control endpoint.
func (b *Broker) Serve(ctx context.Context, path string) error {
	if !filepath.IsAbs(path) {
		return ErrProtocol
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	info, err := os.Lstat(dir)
	if err != nil || !info.IsDir() || info.Mode().Perm()&0077 != 0 {
		return errors.New("browser: private socket directory required")
	}
	if info, err = os.Lstat(path); err == nil {
		if info.Mode()&os.ModeSocket == 0 {
			return errors.New("browser: refusing to replace non-socket")
		}
		connection, dialErr := net.DialTimeout("unix", path, 250*time.Millisecond)
		if dialErr == nil {
			_ = connection.Close()
			return errors.New("browser: listener already active")
		}
		if !errors.Is(dialErr, syscall.ECONNREFUSED) {
			return dialErr
		}
		if err = os.Remove(path); err != nil {
			return err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	listener, err := net.Listen("unix", path)
	if err != nil {
		return err
	}
	defer listener.Close()
	defer os.Remove(path)
	if err = os.Chmod(path, 0600); err != nil {
		return err
	}
	// No host has been accepted yet. A new broker cannot inherit old grants,
	// so their managed uploads/downloads are no longer live. OpenRoot keeps
	// cleanup beneath this private directory and does not follow symlinks.
	root, err := os.OpenRoot(dir)
	if err != nil {
		return err
	}
	for _, name := range []string{"transfers", "downloads"} {
		if err := root.RemoveAll(name); err != nil {
			_ = root.Close()
			return err
		}
	}
	if err := root.Close(); err != nil {
		return err
	}
	stop := context.AfterFunc(ctx, func() { _ = listener.Close() })
	defer stop()
	var connections sync.WaitGroup
	defer func() { cancel(); b.Close(); connections.Wait() }()
	slots := make(chan struct{}, MaxHosts)
	for {
		conn, err := listener.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		select {
		case slots <- struct{}{}:
		default:
			_ = conn.Close()
			continue
		}
		connections.Add(1)
		go func() { defer connections.Done(); defer func() { <-slots }(); b.serveConnection(ctx, conn) }()
	}
}

func (b *Broker) serveConnection(parent context.Context, conn net.Conn) {
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	host := &socketHost{conn: conn, pending: make(map[string]chan runtime.ToolResult), closed: make(chan struct{})}
	defer host.Close()
	stop := context.AfterFunc(ctx, host.Close)
	defer stop()
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	first, err := readFrame(conn, 8192)
	if err != nil || first.Type != "attach" || first.Attach == nil {
		return
	}
	a := first.Attach
	verified, err := b.Attach(ctx, a.OrganizationID, a.BindingID, a.ClaimSecret, a.Identity, host)
	if err != nil {
		_ = host.send(frame{Type: "error", Error: "UNAVAILABLE"})
		return
	}
	defer b.Detach(verified.ID, host)
	if host.send(frame{Type: "attached", Binding: &verified}) != nil {
		return
	}
	go func() {
		ticker := time.NewTicker(10 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-host.closed:
				return
			case <-ticker.C:
				if err := b.Heartbeat(ctx, verified.ID, host); err != nil {
					host.Close()
					return
				}
			}
		}
	}()
	for {
		_ = conn.SetReadDeadline(time.Now().Add(35 * time.Second))
		value, err := readFrame(conn, maxFrame)
		if err != nil {
			return
		}
		switch value.Type {
		case "ping":
			if host.send(frame{Type: "pong", ID: value.ID}) != nil {
				return
			}
		case "grant":
			if value.Grant == nil {
				return
			}
			code := ""
			if b.SetGrant(verified.ID, host, *value.Grant) != nil {
				code = "CONTROL_REVOKED"
			}
			if host.send(frame{Type: "grant-result", ID: value.ID, Error: code}) != nil {
				return
			}
		case "result":
			if value.Result == nil {
				return
			}
			host.mu.Lock()
			reply := host.pending[value.ID]
			host.mu.Unlock()
			if reply != nil {
				select {
				case reply <- *value.Result:
				default:
					return
				}
			}
		default:
			return
		}
	}
}
