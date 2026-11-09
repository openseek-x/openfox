package daemon

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/coder/websocket"
	"openfox/internal/membership"
	"openfox/internal/platform/safehttp"
)

// Only dial the verified target in the single-use reply from the authenticated
// center. Resolve once and dial the checked IP, never loopback or metadata.
func ForwardGitLab(ctx context.Context, center string, token func(context.Context) (string, error), msg Message) {
	var payload struct {
		ID string `json:"command_id"`
	}
	if msg.Type != "gitlab.proxy" || !membership.ValidateUUID(msg.OrganizationID) || json.Unmarshal(msg.Payload, &payload) != nil || !membership.ValidateUUID(payload.ID) {
		return
	}
	u, err := url.Parse(center)
	if err != nil || !strings.HasSuffix(u.Path, "/device/connect") {
		return
	}
	u.Path = strings.TrimSuffix(u.Path, "/device/connect") + "/device/organizations/" + msg.OrganizationID + "/gitlab-proxy/" + payload.ID
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	t, err := token(ctx)
	if err != nil {
		return
	}
	ws, _, err := websocket.Dial(ctx, u.String(), &websocket.DialOptions{HTTPHeader: http.Header{"Authorization": []string{"Bearer " + t}}, HTTPClient: &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}})
	if err != nil {
		return
	}
	defer ws.CloseNow()
	ws.SetReadLimit(32768)
	kind, data, err := ws.Read(ctx)
	if err != nil || kind != websocket.MessageText || len(data) > 300 {
		return
	}
	host, port, err := net.SplitHostPort(string(data))
	if err != nil {
		return
	}
	ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil || len(ips) == 0 {
		return
	}
	for _, a := range ips {
		if a.IP.Equal(net.ParseIP("fd00:ec2::254")) || a.IP.Equal(net.ParseIP("fd00:ec2::23")) || (!a.IP.IsPrivate() && !safehttp.IPAllowed(a.IP)) {
			return
		}
	}
	dialer := net.Dialer{Timeout: 5 * time.Second}
	remote, err := dialer.DialContext(ctx, "tcp", net.JoinHostPort(ips[0].IP.String(), port))
	if err != nil {
		return
	}
	defer remote.Close()
	_ = remote.SetDeadline(time.Now().Add(25 * time.Second))
	tunnel := websocket.NetConn(ctx, ws, websocket.MessageBinary)
	done := make(chan struct{}, 2)
	copyStream := func(dst io.Writer, src io.Reader) {
		_, _ = io.CopyBuffer(dst, io.LimitReader(src, (8<<20)+1), make([]byte, 16384))
		done <- struct{}{}
	}
	go copyStream(remote, tunnel)
	go copyStream(tunnel, remote)
	select {
	case <-ctx.Done():
	case <-done:
	}
}
