package runtimepolicy

import (
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
	"unicode"
)

const Hermes = "hermes"

// HermesModelConfig is the frozen Run input. ContextLength is an explicit
// declaration, not a measurement or a provider capability discovered by Hermes.
type HermesModelConfig struct {
	Model, BaseURL, APIKey string
	ContextLength          int
}

func HermesEnvironmentAllowed(name string) bool {
	if !strings.HasPrefix(name, "HERMES_") {
		return true
	}
	return name == "HERMES_API_KEY" || name == "HERMES_BASE_URL" || name == "HERMES_MODEL" || name == "HERMES_CONTEXT_LENGTH"
}

func ValidateHermesModes(approval, collaboration, reasoning string, args []string) error {
	if approval != "" && approval != FollowRuntime && approval != ReadOnly {
		return fmt.Errorf("Hermes supports only FOLLOW_RUNTIME and READ_ONLY permissions")
	}
	if !ValidCollaborationMode(NormalizeCollaborationMode(collaboration)) {
		return fmt.Errorf("Hermes collaboration mode is not supported")
	}
	if NormalizeReasoningEffort(reasoning) != ReasoningDefault || len(args) != 0 {
		return fmt.Errorf("Hermes supports DEFAULT reasoning only, without custom arguments or native extensions")
	}
	return nil
}

// ResolveHermesModel never includes input values in errors: URLs and model names
// can contain accidentally pasted credentials. Only these four inputs are consumed.
func ResolveHermesModel(model string, env map[string]string) (HermesModelConfig, error) {
	var out HermesModelConfig
	for name := range env {
		if !HermesEnvironmentAllowed(name) || ReservedEnvironment(name) {
			return out, fmt.Errorf("Hermes environment variable %s is not supported", name)
		}
	}
	key := env["HERMES_API_KEY"]
	if strings.TrimSpace(key) == "" || strings.IndexFunc(key, unicode.IsControl) >= 0 {
		return out, fmt.Errorf("Hermes requires a nonempty HERMES_API_KEY without control characters")
	}
	base, err := HermesEndpoint(env["HERMES_BASE_URL"])
	if err != nil {
		return out, err
	}
	if model == "" || model == DefaultModel {
		model = env["HERMES_MODEL"]
	}
	if !ValidModel(model) || model == DefaultModel || model == "auto" || strings.HasPrefix(model, "-") {
		return out, fmt.Errorf("Hermes requires a concrete model or HERMES_MODEL")
	}
	declared := env["HERMES_CONTEXT_LENGTH"]
	if declared == "" || strings.IndexFunc(declared, func(r rune) bool { return r < '0' || r > '9' }) >= 0 {
		return out, fmt.Errorf("Hermes requires an explicit integer HERMES_CONTEXT_LENGTH; capacity is user-declared")
	}
	size, err := strconv.ParseInt(declared, 10, 32)
	if err != nil || size < 64000 {
		return out, fmt.Errorf("Hermes HERMES_CONTEXT_LENGTH must be between 64000 and 2147483647; capacity is user-declared")
	}
	return HermesModelConfig{Model: model, BaseURL: base, APIKey: key, ContextLength: int(size)}, nil
}

// HermesEndpoint accepts one unambiguous public HTTPS origin/base path. DNS and
// actual dial addresses are additionally checked by safehttp on every connection.
func HermesEndpoint(raw string) (string, error) {
	invalid := func() (string, error) {
		return "", fmt.Errorf("Hermes HERMES_BASE_URL requires a public DNS HTTPS:443 API base without credentials, query, fragment or escaped paths")
	}
	if raw == "" || len(raw) > 2048 || raw != strings.TrimSpace(raw) || strings.ContainsAny(raw, "%\\#") || strings.IndexFunc(raw, func(r rune) bool { return r < 33 || r > 126 }) >= 0 {
		return invalid()
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.Opaque != "" || (u.Port() != "" && u.Port() != "443") {
		return invalid()
	}
	host := strings.ToLower(u.Hostname())
	if strings.ToLower(u.Host) != host && strings.ToLower(u.Host) != host+":443" {
		return invalid()
	}
	if len(host) > 253 || !strings.Contains(host, ".") || net.ParseIP(host) != nil {
		return invalid()
	}
	for _, label := range strings.Split(host, ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' || strings.IndexFunc(label, func(r rune) bool { return !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-') }) >= 0 {
			return invalid()
		}
	}
	if strings.Contains(u.Path, "//") || strings.HasSuffix(strings.TrimRight(u.Path, "/"), "/chat/completions") {
		return invalid()
	}
	for _, segment := range strings.Split(u.Path, "/") {
		if segment == "." || segment == ".." || strings.IndexFunc(segment, func(r rune) bool {
			return !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("-._~", r))
		}) >= 0 {
			return invalid()
		}
	}
	u.Host = host // canonical default TLS port; no host header comes from native.
	u.Path = strings.TrimRight(u.Path, "/")
	return u.String(), nil
}
