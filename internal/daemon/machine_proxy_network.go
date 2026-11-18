package daemon

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/url"
	"sync"
	"time"
)

var ErrMachineProxyRequest = errors.New("machine_proxy_request_failed")

// Status contains no endpoint or credentials and is safe for local status.
type MachineProxyStatus struct {
	LoadedRevision     *uint64 `json:"loaded_revision,omitempty"`
	ConnectionRevision *uint64 `json:"connection_revision,omitempty"`
	Problem            string  `json:"problem,omitempty"`
}

// One current transport is retained. Replaced transports finish existing
// requests/connections; their idle sockets are closed, never active Runs.
type MachineProxyNetwork struct {
	Store     MachineProxyStore
	base      *http.Transport
	mu        sync.Mutex
	transport *http.Transport
	key       string
	status    MachineProxyStatus
}

func NewMachineProxyNetwork(store MachineProxyStore) *MachineProxyNetwork {
	return &MachineProxyNetwork{Store: store, base: http.DefaultTransport.(*http.Transport).Clone()}
}

func (n *MachineProxyNetwork) Status() MachineProxyStatus {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.status
}

func (n *MachineProxyNetwork) snapshot() (*http.Transport, uint64, error) {
	p, err := n.Store.read()
	n.mu.Lock()
	defer n.mu.Unlock()
	if err != nil {
		n.status.Problem = err.Error()
		return nil, 0, err
	}
	endpoint := ""
	if p.Effective != nil {
		endpoint = p.Effective.URL()
	}
	key := p.Mode + "/" + endpoint
	if n.transport == nil || n.key != key {
		t := n.base.Clone()
		t.DialContext = (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext
		t.TLSHandshakeTimeout, t.ResponseHeaderTimeout, t.IdleConnTimeout = 10*time.Second, 30*time.Second, 30*time.Second
		if p.Mode != "DISABLED" {
			t.Proxy = nil
			if endpoint != "" {
				proxyURL, _ := url.Parse(endpoint)
				t.Proxy = func(r *http.Request) (*url.URL, error) {
					host := r.URL.Hostname()
					if host == "localhost" || host == "127.0.0.1" || host == "::1" {
						return nil, nil
					}
					return proxyURL, nil
				}
			}
		}
		if n.transport != nil {
			n.transport.CloseIdleConnections()
		}
		n.transport, n.key = t, key
	}
	revision := p.Revision
	n.status.LoadedRevision, n.status.Problem = &revision, ""
	return n.transport, revision, nil
}

func (n *MachineProxyNetwork) Close() {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.transport != nil {
		n.transport.CloseIdleConnections()
	}
}

func (n *MachineProxyNetwork) Client() *http.Client {
	return &http.Client{Transport: n, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

func (n *MachineProxyNetwork) RoundTrip(r *http.Request) (*http.Response, error) {
	t, _, err := n.snapshot()
	if err != nil {
		return nil, err
	}
	resp, err := t.RoundTrip(r)
	if err != nil {
		return nil, machineProxyNetworkError(r.Context())
	}
	return resp, nil
}

func machineProxyNetworkError(ctx context.Context) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	// A proxy host may occur without its full URL in a transport error.
	// Keep it out of Run results and center-visible diagnostics.
	return ErrMachineProxyRequest
}

func (n *MachineProxyNetwork) Dial(ctx context.Context, rawURL string, headers http.Header) (MessageConnection, error) {
	t, revision, err := n.snapshot()
	if err != nil {
		return nil, err
	}
	c, err := (WebSocketDialer{HTTPClient: &http.Client{Transport: t, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}).Dial(ctx, rawURL, headers)
	if err != nil {
		if errors.Is(err, ErrUnauthorized) {
			return nil, ErrUnauthorized
		}
		return nil, machineProxyNetworkError(ctx)
	}
	n.mu.Lock()
	n.status.ConnectionRevision = &revision
	n.mu.Unlock()
	return c, nil
}
