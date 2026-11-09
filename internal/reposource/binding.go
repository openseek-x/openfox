package reposource

import (
	"context"
	"errors"
	"net/url"
	"strings"

	"github.com/jackc/pgx/v5"
	"openfox/internal/membership"
)

var ErrBinding = errors.New("repository connection is unavailable or not authorized")

// Only an organization administrator can grant an organization credential.
// Matching the instance prevents a configured URL from exfiltrating the token.
func CheckBinding(ctx context.Context, tx pgx.Tx, connection, rawURL string, canBind bool) error {
	if connection == "" {
		return nil
	}
	if !canBind || !membership.ValidateUUID(connection) {
		return ErrBinding
	}
	var provider, instance string
	e := tx.QueryRow(ctx, `SELECT provider,COALESCE(settings->>'instance_url','') FROM openfox.integration_connections WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid AND scope='ORGANIZATION' AND status='ACTIVE' FOR SHARE`, connection).Scan(&provider, &instance)
	if e != nil {
		return ErrBinding
	}
	if !MatchesInstance(provider, instance, rawURL) {
		return ErrBinding
	}
	return nil
}
func MatchesInstance(provider, instance, rawURL string) bool {
	switch provider {
	case "github":
		instance = "https://github.com"
	case "gitlab_cn":
		instance = "https://gitlab.cn"
	case "gitlab":
	default:
		return false
	}
	repo, e := url.Parse(rawURL)
	base, be := url.Parse(instance)
	if e != nil || be != nil || !Valid(Repository{URL: rawURL}) || base.Scheme != "https" || !strings.EqualFold(base.Host, repo.Host) {
		return false
	}
	prefix := strings.TrimRight(base.Path, "/") + "/"
	path, ok := strings.CutPrefix(repo.Path, prefix)
	if !ok || len(strings.Split(strings.TrimSuffix(path, ".git"), "/")) < 2 {
		return false
	}
	for _, part := range strings.Split(path, "/") {
		if part == "" || part == "." || part == ".." {
			return false
		}
	}
	return true
}
