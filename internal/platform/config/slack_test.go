package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSlackSecretConfiguration(t *testing.T) {
	for _, key := range []string{"OPENFOX_SLACK_CLIENT_ID", "OPENFOX_SLACK_CLIENT_SECRET_FILE", "OPENFOX_SLACK_SIGNING_SECRET_FILE"} {
		t.Setenv(key, "")
	}
	if e := validateIntegrations(IntegrationsConfig{}); e != nil {
		t.Fatal(e)
	}
	dir := t.TempDir()
	client, sign := filepath.Join(dir, "client"), filepath.Join(dir, "sign")
	if e := os.WriteFile(client, []byte("synthetic-client-secret"), 0600); e != nil {
		t.Fatal(e)
	}
	if e := os.WriteFile(sign, []byte("synthetic-signing-secret"), 0600); e != nil {
		t.Fatal(e)
	}
	t.Setenv("OPENFOX_SLACK_CLIENT_ID", "123.456")
	t.Setenv("OPENFOX_SLACK_CLIENT_SECRET_FILE", client)
	if _, e := Load(); e == nil {
		t.Fatal("partial Slack configuration accepted")
	}
	t.Setenv("OPENFOX_SLACK_SIGNING_SECRET_FILE", sign)
	c, e := Load()
	if e != nil {
		t.Fatal(e)
	}
	if c.Integrations.SlackClientSecret != "synthetic-client-secret" || c.Integrations.SlackSigningSecret != "synthetic-signing-secret" {
		t.Fatal("private files not loaded")
	}
	if e = os.Chmod(sign, 0644); e != nil {
		t.Fatal(e)
	}
	if _, e = Load(); e == nil || strings.Contains(e.Error(), "synthetic-signing-secret") {
		t.Fatal("unsafe permissions or secret exposure", e)
	}
	if e = os.Chmod(sign, 0600); e != nil {
		t.Fatal(e)
	}
	link := filepath.Join(dir, "link")
	if e = os.Symlink(sign, link); e != nil {
		t.Fatal(e)
	}
	t.Setenv("OPENFOX_SLACK_SIGNING_SECRET_FILE", link)
	if _, e = Load(); e == nil {
		t.Fatal("secret symlink accepted")
	}
}
