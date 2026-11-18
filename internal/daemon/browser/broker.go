// Package browser owns the local, per-Run browser control boundary. Browser
// credentials and contents never pass through the center authorization API.
package browser

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"net/url"
	"sync"
	"time"

	scope "openfox/internal/browser"
	"openfox/internal/membership"
	"openfox/internal/runtime"
)

const MaxHosts = 24
const MaxQueue = 16
const MaxCallsPerRun = 256
const MaxArguments = 256 * 1024

type Center interface {
	ClaimBrowser(context.Context, string, string, string, scope.Identity) (scope.Binding, error)
	HeartbeatBrowser(context.Context, string, string, scope.Identity) (scope.Binding, error)
	AuthorizeBrowser(context.Context, string, string, scope.AuthorizationInput) (scope.Authorization, error)
}

type Run struct {
	OrganizationID   string
	ConversationID   string
	ActorID          string
	ID               string
	Generation       int64
	ClientMessageIDs []string
	// Invoke wraps host execution with Run-owned file staging. It is assigned
	// by the supervisor, never decoded from model or socket input.
	Invoke func(context.Context, Host, Request) runtime.ToolResult
}

type Grant struct {
	RunID           string   `json:"run_id,omitempty"`
	Generation      int64    `json:"generation,omitempty"`
	ClientMessageID string   `json:"client_message_id,omitempty"`
	Epoch           int64    `json:"epoch"`
	Origins         []string `json:"origins"`
}

type Request struct {
	ID             string          `json:"id"`
	Tool           string          `json:"tool"`
	Arguments      json.RawMessage `json:"arguments"`
	RunID          string          `json:"run_id"`
	Generation     int64           `json:"generation"`
	ControlEpoch   int64           `json:"control_epoch"`
	ReadOnly       bool            `json:"read_only"`
	Origins        []string        `json:"origins"`
	LeaseExpiresAt time.Time       `json:"lease_expires_at"`
	Files          []StagedFile    `json:"files,omitempty"`
}

type StagedFile struct {
	Path   string `json:"path"`
	Name   string `json:"name"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

type Host interface {
	Execute(context.Context, Request) runtime.ToolResult
	Close()
}

type Broker struct {
	Center   Center
	DeviceID string
	mu       sync.Mutex
	hosts    map[string]*binding
}

type binding struct {
	scope       scope.Binding
	host        Host
	grant       Grant
	grantCtx    context.Context
	grantCancel context.CancelFunc
	ctx         context.Context
	cancel      context.CancelFunc
	queue       chan struct{}
	serial      chan struct{}
	// Only mutations are retained. Each result is metadata-sized; screenshots
	// are observations and are never retained in the deduplication cache.
	calls map[string]map[string]*call
}

type call struct {
	hash   [32]byte
	done   chan struct{}
	result runtime.ToolResult
}

func New(center Center, device string) *Broker {
	return &Broker{Center: center, DeviceID: device, hosts: make(map[string]*binding)}
}

func denied(code string) runtime.ToolResult { return runtime.TextResult(code, false) }

func (b *Broker) Attach(ctx context.Context, org, id, secret string, identity scope.Identity, host Host) (scope.Binding, error) {
	if b.Center == nil || host == nil || !membership.ValidateUUID(org) || !membership.ValidateUUID(id) || !identity.Valid() {
		return scope.Binding{}, scope.ErrInvalid
	}
	check, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	verified, err := b.Center.ClaimBrowser(check, org, id, secret, identity)
	if err != nil {
		return scope.Binding{}, err
	}
	if verified.OrganizationID != org || verified.ID != id || verified.DeviceID != b.DeviceID || verified.HostInstanceID != identity.HostInstanceID || verified.HostEpoch != identity.HostEpoch || verified.Revision != identity.Revision || !verified.LeaseExpiresAt.After(time.Now()) {
		return scope.Binding{}, scope.ErrUnavailable
	}
	b.mu.Lock()
	if _, exists := b.hosts[id]; exists {
		b.mu.Unlock()
		return scope.Binding{}, scope.ErrConflict
	}
	var replaced []*binding
	for _, old := range b.hosts {
		if old.scope.OrganizationID == org && old.scope.ConversationID == verified.ConversationID && old.scope.OwnerUserID == verified.OwnerUserID {
			replaced = append(replaced, old)
		}
	}
	if len(b.hosts)-len(replaced) >= MaxHosts {
		b.mu.Unlock()
		return scope.Binding{}, scope.ErrConflict
	}
	for _, old := range replaced {
		old.cancel()
		delete(b.hosts, old.scope.ID)
	}
	hostCtx, stop := context.WithCancel(ctx)
	b.hosts[id] = &binding{scope: verified, host: host, ctx: hostCtx, cancel: stop, queue: make(chan struct{}, MaxQueue+1), serial: make(chan struct{}, 1), calls: make(map[string]map[string]*call)}
	b.mu.Unlock()
	for _, old := range replaced {
		old.host.Close()
	}
	return verified, nil
}

func (b *Broker) Detach(id string, host Host) {
	b.mu.Lock()
	entry := b.hosts[id]
	if entry != nil && entry.host == host {
		delete(b.hosts, id)
		entry.cancel()
	}
	b.mu.Unlock()
	if entry != nil && entry.host == host {
		host.Close()
	}
}

func (b *Broker) Heartbeat(ctx context.Context, id string, host Host) error {
	b.mu.Lock()
	entry := b.hosts[id]
	if entry == nil || entry.host != host {
		b.mu.Unlock()
		return scope.ErrUnavailable
	}
	current := entry.scope
	b.mu.Unlock()
	check, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	updated, err := b.Center.HeartbeatBrowser(check, current.OrganizationID, id, scope.Identity{HostInstanceID: current.HostInstanceID, HostEpoch: current.HostEpoch, Revision: current.Revision})
	if err != nil {
		b.Detach(id, host)
		return err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.hosts[id] != entry || updated.ID != id || updated.Revision != current.Revision || updated.HostEpoch != current.HostEpoch || !updated.LeaseExpiresAt.After(time.Now()) {
		return scope.ErrUnavailable
	}
	entry.scope.LeaseExpiresAt = updated.LeaseExpiresAt
	return nil
}

func (b *Broker) SetGrant(id string, host Host, grant Grant) error {
	if grant.Epoch < 1 || len(grant.Origins) > 32 {
		return scope.ErrInvalid
	}
	if grant.RunID != "" && (!membership.ValidateUUID(grant.RunID) || grant.Generation < 1) {
		return scope.ErrInvalid
	}
	if grant.ClientMessageID != "" && !membership.ValidateUUID(grant.ClientMessageID) {
		return scope.ErrInvalid
	}
	if (grant.RunID != "" && grant.ClientMessageID != "") || ((grant.RunID != "" || grant.ClientMessageID != "") && len(grant.Origins) == 0) {
		return scope.ErrInvalid
	}
	for _, origin := range grant.Origins {
		u, err := url.Parse(origin)
		if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") || len(origin) > 2048 {
			return scope.ErrInvalid
		}
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	entry := b.hosts[id]
	if entry == nil || entry.host != host || !entry.scope.LeaseExpiresAt.After(time.Now()) {
		return scope.ErrUnavailable
	}
	if grant.Epoch <= entry.grant.Epoch {
		return scope.ErrConflict
	}
	if entry.grantCancel != nil {
		entry.grantCancel()
	}
	entry.grantCtx, entry.grantCancel = context.WithCancel(entry.ctx)
	entry.grant = grant
	return nil
}

func classify(tool string, args json.RawMessage) (operation string, timeout time.Duration, canonical []byte, err error) {
	if len(args) > MaxArguments || !json.Valid(args) {
		return "", 0, nil, scope.ErrInvalid
	}
	decoder := json.NewDecoder(bytes.NewReader(args))
	decoder.UseNumber()
	var value map[string]any
	if decoder.Decode(&value) != nil || value == nil {
		return "", 0, nil, scope.ErrInvalid
	}
	canonical, err = json.Marshal(value)
	if err != nil {
		return "", 0, nil, err
	}
	action, _ := value["action"].(string)
	operation, timeout = "act", 10*time.Second
	switch tool {
	case "openfox_browser_tabs":
		switch action {
		case "list":
			operation = "observe"
		case "open":
			timeout = 30 * time.Second
		case "focus", "close":
		default:
			err = scope.ErrInvalid
		}
	case "openfox_browser_observe":
		operation = "observe"
		switch action {
		case "snapshot", "frames", "find", "screenshot", "console":
		case "wait":
			timeout = 30 * time.Second
		default:
			err = scope.ErrInvalid
		}
	case "openfox_browser_act":
		switch action {
		case "navigate", "back", "forward", "reload":
			timeout = 30 * time.Second
		case "stop", "click", "hover", "fill", "press", "select", "check", "scroll", "dialog":
		default:
			err = scope.ErrInvalid
		}
	case "openfox_browser_files":
		operation, timeout = "files", 60*time.Second
		switch action {
		case "list":
			operation = "observe"
		case "upload", "get":
		default:
			err = scope.ErrInvalid
		}
	default:
		err = scope.ErrInvalid
	}
	return
}

func (b *Broker) Call(ctx context.Context, run Run, tool, id string, args json.RawMessage) runtime.ToolResult {
	if len(id) < 1 || len(id) > 128 || run.Generation < 1 {
		return denied("INVALID_REQUEST")
	}
	operation, timeout, canonical, err := classify(tool, args)
	if err != nil {
		return denied("INVALID_REQUEST")
	}
	b.mu.Lock()
	var entry *binding
	for _, candidate := range b.hosts {
		if candidate.scope.OrganizationID == run.OrganizationID && candidate.scope.ConversationID == run.ConversationID && candidate.scope.OwnerUserID == run.ActorID {
			entry = candidate
			break
		}
	}
	if entry == nil || entry.ctx.Err() != nil || !entry.scope.LeaseExpiresAt.After(time.Now()) {
		b.mu.Unlock()
		return denied("UNAVAILABLE")
	}
	grant := entry.grant
	if !grantMatches(grant, run) {
		b.mu.Unlock()
		return denied("CONTROL_REVOKED")
	}
	runKey := run.ID + "/" + jsonNumber(run.Generation)
	var saved *call
	if operation != "observe" {
		entries := entry.calls[runKey]
		if entries == nil {
			entries = make(map[string]*call)
			entry.calls[runKey] = entries
		}
		hash := sha256.Sum256(append([]byte(tool+"\n"), canonical...))
		if previous := entries[id]; previous != nil {
			b.mu.Unlock()
			if previous.hash != hash {
				return denied("CALL_CONFLICT")
			}
			select {
			case <-previous.done:
				return previous.result
			case <-ctx.Done():
				return denied("OUTCOME_UNKNOWN")
			case <-entry.ctx.Done():
				return denied("OUTCOME_UNKNOWN")
			}
		}
		if len(entries) >= MaxCallsPerRun {
			b.mu.Unlock()
			return denied("LIMIT_EXCEEDED")
		}
		saved = &call{hash: hash, done: make(chan struct{})}
		entries[id] = saved
	}
	b.mu.Unlock()
	result := b.execute(ctx, entry, grant, run, operation, tool, id, canonical, timeout)
	if saved != nil {
		// Mutation replies never contain screenshots; retain only a small,
		// deterministic acknowledgement for uncertain/repeated calls.
		if result.Image != nil {
			result = denied("INVALID_RESULT")
		}
		result.Text = runtime.OutputPreview(result.Text, 4096)
		saved.result = result
		close(saved.done)
	}
	return result
}

func jsonNumber(value int64) string { raw, _ := json.Marshal(value); return string(raw) }
func grantMatches(grant Grant, run Run) bool {
	return grant.Epoch > 0 && ((grant.RunID == run.ID && grant.Generation == run.Generation) || (grant.RunID == "" && grant.ClientMessageID != ""))
}

func (b *Broker) execute(ctx context.Context, entry *binding, grant Grant, run Run, operation, tool, id string, args json.RawMessage, timeout time.Duration) runtime.ToolResult {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	b.mu.Lock()
	grantCtx := entry.grantCtx
	validGrant := entry.grant.Epoch == grant.Epoch && grantCtx != nil
	b.mu.Unlock()
	if !validGrant {
		return denied("CONTROL_REVOKED")
	}
	stop := context.AfterFunc(grantCtx, cancel)
	defer stop()
	select {
	case entry.queue <- struct{}{}:
		defer func() { <-entry.queue }()
	default:
		return denied("LIMIT_EXCEEDED")
	}
	select {
	case entry.serial <- struct{}{}:
		defer func() { <-entry.serial }()
	case <-ctx.Done():
		return denied("TIMEOUT")
	}
	b.mu.Lock()
	current := entry.scope
	valid := b.hosts[current.ID] == entry && current.LeaseExpiresAt.After(time.Now()) && entry.grant.Epoch == grant.Epoch && grantMatches(entry.grant, run)
	b.mu.Unlock()
	if !valid || ctx.Err() != nil {
		return denied("CONTROL_REVOKED")
	}
	ctx, leaseCancel := context.WithDeadline(ctx, current.LeaseExpiresAt)
	defer leaseCancel()
	check, stopCheck := context.WithTimeout(ctx, 5*time.Second)
	authorized, err := b.Center.AuthorizeBrowser(check, run.OrganizationID, run.ID, scope.AuthorizationInput{Identity: scope.Identity{HostInstanceID: current.HostInstanceID, HostEpoch: current.HostEpoch, Revision: current.Revision}, BindingID: current.ID, Generation: run.Generation, ClientMessageID: grant.ClientMessageID, Operation: operation})
	stopCheck()
	if err != nil {
		return denied("FORBIDDEN")
	}
	// Heartbeats can advance the expiry while authorization is in flight.
	verified := authorized.Binding
	if authorized.RunID != run.ID || authorized.Generation != run.Generation || verified.ID != current.ID || verified.OrganizationID != current.OrganizationID || verified.DeviceID != current.DeviceID || verified.ConversationID != run.ConversationID || verified.OwnerUserID != run.ActorID || verified.HostInstanceID != current.HostInstanceID || verified.HostEpoch != current.HostEpoch || verified.Revision != current.Revision || !verified.LeaseExpiresAt.After(time.Now()) || (authorized.ReadOnly && operation != "observe") {
		return denied("FORBIDDEN")
	}
	b.mu.Lock()
	valid = b.hosts[current.ID] == entry && entry.grant.Epoch == grant.Epoch && grantMatches(entry.grant, run)
	if valid && entry.grant.RunID == "" {
		entry.grant.RunID = run.ID
		entry.grant.Generation = run.Generation
		entry.grant.ClientMessageID = ""
	}
	b.mu.Unlock()
	if !valid || ctx.Err() != nil {
		return denied("CONTROL_REVOKED")
	}
	request := Request{ID: id, Tool: tool, Arguments: args, RunID: run.ID, Generation: run.Generation, ControlEpoch: grant.Epoch, ReadOnly: authorized.ReadOnly, Origins: append([]string(nil), grant.Origins...), LeaseExpiresAt: current.LeaseExpiresAt}
	invoke := run.Invoke
	if invoke == nil {
		invoke = func(ctx context.Context, host Host, request Request) runtime.ToolResult {
			return host.Execute(ctx, request)
		}
	}
	result := invoke(ctx, entry.host, request).Bounded()
	b.mu.Lock()
	valid = b.hosts[current.ID] == entry && entry.grant.Epoch == grant.Epoch
	b.mu.Unlock()
	if ctx.Err() != nil || !valid {
		return denied("OUTCOME_UNKNOWN")
	}
	return result
}

func (b *Broker) EndRun(run Run) {
	b.mu.Lock()
	type notification struct {
		host  Host
		epoch int64
	}
	var revoked []notification
	for _, entry := range b.hosts {
		pendingMatch := false
		if entry.grant.RunID == "" && entry.scope.OrganizationID == run.OrganizationID && entry.scope.ConversationID == run.ConversationID && entry.scope.OwnerUserID == run.ActorID {
			for _, id := range run.ClientMessageIDs {
				if id != "" && entry.grant.ClientMessageID == id {
					pendingMatch = true
					break
				}
			}
		}
		if (entry.grant.RunID == run.ID && entry.grant.Generation == run.Generation) || pendingMatch {
			if entry.grantCancel != nil {
				entry.grantCancel()
			}
			entry.grant = Grant{Epoch: entry.grant.Epoch + 1}
			revoked = append(revoked, notification{entry.host, entry.grant.Epoch})
		}
		delete(entry.calls, run.ID+"/"+jsonNumber(run.Generation))
	}
	b.mu.Unlock()
	for _, item := range revoked {
		if receiver, ok := item.host.(interface{ Revoke(int64) }); ok {
			receiver.Revoke(item.epoch)
		}
	}
}

func (b *Broker) Close() {
	b.mu.Lock()
	hosts := b.hosts
	b.hosts = make(map[string]*binding)
	b.mu.Unlock()
	for _, entry := range hosts {
		entry.cancel()
		entry.host.Close()
	}
}

var ErrProtocol = errors.New("browser: invalid local protocol")
