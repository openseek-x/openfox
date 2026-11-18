package runtimepolicy

import (
	"fmt"
	"net"
	"net/url"
	"strings"
	"unicode"
)

const CodeBuddy = "codebuddy"

// CodeBuddyEnvironmentAllowed rejects native configuration/credential sources that
// the restricted adapter has not verified. Ordinary environment rules still apply.
func CodeBuddyEnvironmentAllowed(name string) bool {
	if !strings.HasPrefix(name, "CODEBUDDY_") {
		return true
	}
	return name == "CODEBUDDY_API_KEY" || name == "CODEBUDDY_BASE_URL" || name == "CODEBUDDY_MODEL"
}

func ValidateCodeBuddyModes(approval, collaboration, reasoning string, args []string) error {
	if approval != "" && approval != FollowRuntime && approval != ReadOnly {
		return fmt.Errorf("CodeBuddy supports only FOLLOW_RUNTIME and READ_ONLY permissions")
	}
	if !ValidCollaborationMode(NormalizeCollaborationMode(collaboration)) {
		return fmt.Errorf("CodeBuddy collaboration mode is not supported")
	}
	if NormalizeReasoningEffort(reasoning) != ReasoningDefault {
		return fmt.Errorf("CodeBuddy supports only DEFAULT reasoning depth")
	}
	if len(args) != 0 {
		return fmt.Errorf("CodeBuddy custom arguments, plugins and native subagents are not supported")
	}
	return nil
}

// ResolveCodeBuddyModel is used after the existing model and environment precedence
// has been applied. Errors never include values (including endpoint or model values).
func ResolveCodeBuddyModel(model string, env map[string]string) (string, error) {
	if value := env["NODE_TLS_REJECT_UNAUTHORIZED"]; value != "" && value != "1" {
		return "", fmt.Errorf("CodeBuddy cannot disable TLS certificate verification")
	}
	for name := range env {
		if !CodeBuddyEnvironmentAllowed(name) {
			return "", fmt.Errorf("CodeBuddy environment variable %s is not supported", name)
		}
	}
	key := env["CODEBUDDY_API_KEY"]
	if strings.TrimSpace(key) == "" || strings.IndexFunc(key, unicode.IsControl) >= 0 {
		return "", fmt.Errorf("CodeBuddy requires a nonempty CODEBUDDY_API_KEY")
	}
	endpoint := env["CODEBUDDY_BASE_URL"]
	u, err := url.Parse(endpoint)
	if err != nil || endpoint == "" || strings.TrimSpace(endpoint) != endpoint || strings.IndexFunc(endpoint, unicode.IsControl) >= 0 ||
		u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || strings.Contains(endpoint, "#") || u.Opaque != "" ||
		strings.HasSuffix(strings.TrimRight(u.Path, "/"), "/chat/completions") {
		return "", fmt.Errorf("CodeBuddy requires CODEBUDDY_BASE_URL as an API base URL without credentials, query or /chat/completions")
	}
	loopback := u.Hostname() == "localhost"
	if ip := net.ParseIP(u.Hostname()); ip != nil {
		loopback = ip.IsLoopback()
	}
	if u.Scheme != "https" && !(u.Scheme == "http" && loopback) {
		return "", fmt.Errorf("CodeBuddy CODEBUDDY_BASE_URL requires HTTPS (HTTP is allowed only on loopback)")
	}
	if model == "" || model == DefaultModel {
		model = env["CODEBUDDY_MODEL"]
	}
	if model == "" || model == DefaultModel || model == "auto" || strings.TrimSpace(model) != model ||
		len(model) > MaxModelLength || strings.HasPrefix(model, "-") || strings.IndexFunc(model, unicode.IsControl) >= 0 {
		return "", fmt.Errorf("CodeBuddy requires a concrete model or CODEBUDDY_MODEL; default and auto are not model IDs")
	}
	return model, nil
}
