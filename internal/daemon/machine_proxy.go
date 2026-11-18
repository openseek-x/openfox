package daemon

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const MachineProxyFileName = "machine-proxy.json"
const MaxMachineProxyBytes = 16 << 10

var (
	ErrMachineProxyInvalid  = errors.New("machine_proxy_invalid")
	ErrMachineProxyScope    = errors.New("machine_proxy_registration_changed")
	ErrMachineProxyConflict = errors.New("machine_proxy_conflict")
	ErrMachineProxyBusy     = errors.New("machine_proxy_busy")
	machineProxyHost        = regexp.MustCompile(`^(?:[a-zA-Z0-9.-]{1,253}|\[[a-fA-F0-9:]{2,45}\])$`)
	machineProxyFingerprint = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

type MachineProxyScope struct {
	CenterURL      string `json:"center_url"`
	OrganizationID string `json:"organization_id"`
	DeviceID       string `json:"device_id"`
	KeyID          string `json:"key_id"`
}

func ProxyScope(config DeviceConfig) MachineProxyScope {
	u, _ := url.Parse(config.CenterURL)
	center := config.CenterURL
	if u != nil {
		u.Host = strings.ToLower(u.Host)
		if (u.Scheme == "https" && u.Port() == "443") || (u.Scheme == "http" && u.Port() == "80") {
			u.Host = u.Hostname()
			if strings.Contains(u.Host, ":") {
				u.Host = "[" + u.Host + "]"
			}
		}
		center = strings.TrimRight(u.String(), "/")
	}
	return MachineProxyScope{center, config.OrganizationID, config.DeviceID, config.KeyID}
}

func (s MachineProxyScope) Token() string {
	b, _ := json.Marshal(s)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// Effective is a non-secret snapshot, not a claim about native private settings.
type MachineProxyEffective struct {
	Mode     string `json:"mode"`
	Protocol string `json:"protocol,omitempty"`
	Host     string `json:"host,omitempty"`
	Port     int    `json:"port,omitempty"`
}

func (e MachineProxyEffective) valid() bool {
	if e.Mode == "direct" {
		return e.Protocol == "" && e.Host == "" && e.Port == 0
	}
	if e.Mode != "proxy" || (e.Protocol != "http" && e.Protocol != "https") || !machineProxyHost.MatchString(e.Host) || e.Port < 1 || e.Port > 65535 {
		return false
	}
	if strings.HasPrefix(e.Host, "[") && net.ParseIP(strings.Trim(e.Host, "[]")) == nil {
		return false
	}
	return true
}

func (e MachineProxyEffective) URL() string {
	if e.Mode != "proxy" {
		return ""
	}
	return e.Protocol + "://" + net.JoinHostPort(strings.Trim(e.Host, "[]"), strconv.Itoa(e.Port))
}

type MachineProxyPolicy struct {
	SchemaVersion     int                    `json:"schema_version"`
	Revision          uint64                 `json:"revision"`
	Scope             MachineProxyScope      `json:"scope"`
	Mode              string                 `json:"mode"`
	Effective         *MachineProxyEffective `json:"effective,omitempty"`
	SourceFingerprint string                 `json:"source_fingerprint,omitempty"`
	UpdatedAt         time.Time              `json:"updated_at"`
}

func (p MachineProxyPolicy) valid() bool {
	if p.SchemaVersion != 1 || p.Revision == 0 || p.Revision >= 1<<53 || p.UpdatedAt.IsZero() {
		return false
	}
	return validMachineProxyChoice(p.Mode, p.Effective, p.SourceFingerprint)
}

func validMachineProxyChoice(mode string, effective *MachineProxyEffective, fingerprint string) bool {
	switch mode {
	case "DISABLED":
		return effective == nil && fingerprint == ""
	case "CUSTOM":
		return effective != nil && effective.valid() && effective.Mode == "proxy" && fingerprint == ""
	case "FOLLOW_DESKTOP":
		return effective != nil && effective.valid() && machineProxyFingerprint.MatchString(fingerprint)
	default:
		return false
	}
}

// Only the owner-authorized local CLI returns endpoint-bearing views. General
// daemon status and center heartbeats never carry this structure.
type MachineProxyView struct {
	Revision          uint64                 `json:"revision"`
	ScopeToken        string                 `json:"scope_token"`
	Mode              string                 `json:"mode"`
	Effective         *MachineProxyEffective `json:"effective,omitempty"`
	SourceFingerprint string                 `json:"source_fingerprint,omitempty"`
	UpdatedAt         *time.Time             `json:"updated_at,omitempty"`
	Problem           string                 `json:"problem,omitempty"`
}

type MachineProxySave struct {
	ExpectedRevision  uint64                 `json:"expected_revision"`
	ScopeToken        string                 `json:"scope_token"`
	Mode              string                 `json:"mode"`
	Effective         *MachineProxyEffective `json:"effective,omitempty"`
	SourceFingerprint string                 `json:"source_fingerprint,omitempty"`
	FollowUpdate      bool                   `json:"follow_update,omitempty"`
}

// ConfigPath comes from the fixed registration location, never the renderer.
// Scope is captured once by a running daemon to reject registration replacement.
type MachineProxyStore struct {
	ConfigPath string
	Scope      MachineProxyScope
}

func (s MachineProxyStore) currentScope() (MachineProxyScope, error) {
	c, err := LoadDeviceConfig(s.ConfigPath)
	if err != nil || ProxyScope(c) != s.Scope {
		return MachineProxyScope{}, ErrMachineProxyScope
	}
	return s.Scope, nil
}

func (s MachineProxyStore) read() (MachineProxyPolicy, error) {
	if _, err := s.currentScope(); err != nil {
		return MachineProxyPolicy{}, err
	}
	b, err := readPrivateFile(filepath.Join(filepath.Dir(s.ConfigPath), MachineProxyFileName), MaxMachineProxyBytes)
	if errors.Is(err, os.ErrNotExist) {
		return MachineProxyPolicy{SchemaVersion: 1, Scope: s.Scope, Mode: "DISABLED"}, nil
	}
	if err != nil {
		return MachineProxyPolicy{}, ErrMachineProxyInvalid
	}
	var p MachineProxyPolicy
	d := json.NewDecoder(strings.NewReader(string(b)))
	d.DisallowUnknownFields()
	if d.Decode(&p) != nil || d.Decode(new(any)) != io.EOF || !p.valid() {
		return MachineProxyPolicy{}, ErrMachineProxyInvalid
	}
	if p.Scope != s.Scope {
		return p, ErrMachineProxyScope
	}
	return p, nil
}

func (s MachineProxyStore) View() MachineProxyView {
	p, err := s.read()
	v := MachineProxyView{Revision: p.Revision, ScopeToken: s.Scope.Token(), Mode: "DISABLED"}
	if err != nil {
		v.Problem = err.Error()
		return v
	}
	v.Mode, v.Effective, v.SourceFingerprint = p.Mode, p.Effective, p.SourceFingerprint
	if !p.UpdatedAt.IsZero() {
		v.UpdatedAt = &p.UpdatedAt
	}
	return v
}

func (s MachineProxyStore) Save(ctx context.Context, in MachineProxySave) (MachineProxyView, error) {
	if !validMachineProxyChoice(in.Mode, in.Effective, in.SourceFingerprint) || in.ScopeToken != s.Scope.Token() || in.ExpectedRevision >= 1<<53 {
		return MachineProxyView{}, ErrMachineProxyInvalid
	}
	unlock, err := lockMachineProxy(ctx, filepath.Join(filepath.Dir(s.ConfigPath), "machine-proxy.lock"))
	if err != nil {
		return MachineProxyView{}, err
	}
	defer unlock()
	if _, err := s.currentScope(); err != nil {
		return MachineProxyView{}, err
	}
	p, readErr := s.read()
	// Invalid/superseded settings can only be replaced by an explicit owner save;
	// automatic FOLLOW updates never repair or take over a registration.
	if in.FollowUpdate && (readErr != nil || p.Mode != "FOLLOW_DESKTOP" || in.Mode != "FOLLOW_DESKTOP") {
		return MachineProxyView{}, ErrMachineProxyConflict
	}
	if readErr == nil && p.Mode == in.Mode && equalMachineProxyEffective(p.Effective, in.Effective) && p.SourceFingerprint == in.SourceFingerprint {
		return s.View(), nil
	}
	if p.Revision != in.ExpectedRevision {
		return MachineProxyView{}, ErrMachineProxyConflict
	}
	if p.Revision+1 >= 1<<53 {
		return MachineProxyView{}, ErrMachineProxyInvalid
	}
	next := MachineProxyPolicy{SchemaVersion: 1, Revision: p.Revision + 1, Scope: s.Scope, Mode: in.Mode, Effective: in.Effective, SourceFingerprint: in.SourceFingerprint, UpdatedAt: time.Now().UTC()}
	b, err := json.Marshal(next)
	if err != nil || len(b) > MaxMachineProxyBytes {
		return MachineProxyView{}, ErrMachineProxyInvalid
	}
	if _, err := s.currentScope(); err != nil {
		return MachineProxyView{}, err
	}
	if err := WritePrivateFile(filepath.Join(filepath.Dir(s.ConfigPath), MachineProxyFileName), b); err != nil {
		return MachineProxyView{}, ErrMachineProxyInvalid
	}
	return s.View(), nil
}

func equalMachineProxyEffective(a, b *MachineProxyEffective) bool {
	return a == nil && b == nil || a != nil && b != nil && *a == *b
}

var MachineProxyEnvironmentKeys = []string{"HTTP_PROXY", "http_proxy", "HTTPS_PROXY", "https_proxy", "ALL_PROXY", "all_proxy", "NO_PROXY", "no_proxy"}

// Environment is sampled once per Run. nil preserves all existing settings.
func (s MachineProxyStore) Environment() (map[string]string, error) {
	p, err := s.read()
	if err != nil {
		return nil, err
	}
	if p.Mode == "DISABLED" {
		return nil, nil
	}
	env := map[string]string{}
	for _, name := range MachineProxyEnvironmentKeys {
		env[name] = ""
	}
	if p.Effective.Mode == "proxy" {
		for _, name := range []string{"HTTP_PROXY", "http_proxy", "HTTPS_PROXY", "https_proxy"} {
			env[name] = p.Effective.URL()
		}
		env["NO_PROXY"], env["no_proxy"] = "localhost,127.0.0.1,::1", "localhost,127.0.0.1,::1"
	}
	return env, nil
}
