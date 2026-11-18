// Package reposource defines an Agent's automatically managed repository source.
package reposource

import (
	"strings"
	"unicode"

	"openfox/internal/platform/safehttp"
)

type Repository struct {
	URL           string         `json:"url"`
	Branch        string         `json:"base_branch"`
	ConnectionID  string         `json:"connection_id,omitempty"`
	Authorization *Authorization `json:"authorization,omitempty"`
}

// Authorization is decrypted only in the authenticated device run response.
// It is never part of a saved repository, worktree, cache manifest or runtime environment.
type Authorization struct {
	Scope          string `json:"scope"`
	Username       string `json:"username"`
	Token          string `json:"token"`
	PrivateNetwork bool   `json:"private_network,omitempty"`
}

func (r Repository) Identity() any {
	scope := ""
	if r.Authorization != nil {
		scope = r.Authorization.Scope
	}
	return struct {
		URL    string `json:"url"`
		Branch string `json:"base_branch"`
		Scope  string `json:"authorization_scope,omitempty"`
	}{r.URL, r.Branch, scope}
}

func Valid(r Repository) bool {
	u, err := safehttp.CheckURL(r.URL)
	return err == nil && u.RawQuery == "" && !u.ForceQuery && u.Path != "" && len(r.Branch) <= 255 && !strings.HasPrefix(r.Branch, "-") && strings.IndexFunc(r.Branch, unicode.IsControl) < 0
}
