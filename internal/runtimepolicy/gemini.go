package runtimepolicy

import (
	"fmt"
	"strings"
	"unicode"
)

const Gemini = "gemini"

// GeminiEnvironmentAllowed describes configuration accepted from Agent/R06
// storage. The adapter still builds its native environment from an empty map.
func GeminiEnvironmentAllowed(name string) bool {
	if strings.HasPrefix(name, "GEMINI_") {
		return name == "GEMINI_API_KEY" || name == "GEMINI_MODEL"
	}
	if strings.HasPrefix(name, "GOOGLE_") || strings.HasPrefix(name, "NODE_") {
		return false
	}
	switch strings.ToUpper(name) {
	case "HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "NO_PROXY", "SSL_CERT_FILE", "SSL_CERT_DIR":
		return false
	}
	return true
}

func ValidateGeminiModes(approval, collaboration, reasoning string, args []string) error {
	if approval != "" && approval != FollowRuntime && approval != ReadOnly {
		return fmt.Errorf("Gemini supports only FOLLOW_RUNTIME and READ_ONLY permissions")
	}
	if !ValidCollaborationMode(NormalizeCollaborationMode(collaboration)) {
		return fmt.Errorf("Gemini collaboration mode is not supported")
	}
	if NormalizeReasoningEffort(reasoning) != ReasoningDefault || len(args) != 0 {
		return fmt.Errorf("Gemini supports DEFAULT reasoning only, without custom arguments or native extensions")
	}
	return nil
}

// ValidGeminiModel permits a model identifier, never a URL or an API route.
// Its limit is the existing public model contract, not a larger vendor limit.
func ValidGeminiModel(model string) bool {
	if !ValidModel(model) || model == DefaultModel || model == "auto" {
		return false
	}
	for i, r := range model {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' {
			continue
		}
		if i == 0 || !strings.ContainsRune("._-", r) {
			return false
		}
	}
	return true
}

// ResolveGeminiModel runs after the existing model/environment precedence.
// Errors never include potentially secret configuration values.
func ResolveGeminiModel(model string, env map[string]string) (string, error) {
	for name := range env {
		if !GeminiEnvironmentAllowed(name) || ReservedEnvironment(name) {
			return "", fmt.Errorf("Gemini environment variable %s is not supported", name)
		}
	}
	key := env["GEMINI_API_KEY"]
	if key == "" || len(key) > 8192 || strings.TrimSpace(key) != key || strings.IndexFunc(key, unicode.IsControl) >= 0 {
		return "", fmt.Errorf("Gemini requires a nonempty GEMINI_API_KEY of at most 8192 bytes without surrounding whitespace or control characters")
	}
	if model == "" || model == DefaultModel {
		model = env["GEMINI_MODEL"]
	}
	if !ValidGeminiModel(model) {
		return "", fmt.Errorf("Gemini requires a concrete model or GEMINI_MODEL (at most 128 ASCII letters, digits, dots, underscores or hyphens, starting with a letter or digit)")
	}
	return model, nil
}
