package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestBitbucketConsumerConfiguration(t *testing.T) {
	pairs := []struct{ id, file string }{{"OPENFOX_BITBUCKET_ORG_CLIENT_ID", "OPENFOX_BITBUCKET_ORG_CLIENT_SECRET_FILE"}, {"OPENFOX_BITBUCKET_PERSONAL_CLIENT_ID", "OPENFOX_BITBUCKET_PERSONAL_CLIENT_SECRET_FILE"}}
	for _, pair := range pairs {
		t.Setenv(pair.id, "")
		t.Setenv(pair.file, "")
	}
	cfg, err := Load()
	if err != nil || cfg.Integrations.BitbucketOrgClientID != "" || cfg.Integrations.BitbucketPersonalClientID != "" {
		t.Fatalf("empty apps: %v", err)
	}
	secret := filepath.Join(t.TempDir(), "consumer.secret")
	if err = os.WriteFile(secret, []byte(" fixture-secret\n"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, pair := range pairs {
		t.Setenv(pair.id, "fixture-app")
		if _, err = Load(); err == nil {
			t.Fatal("accepted incomplete consumer")
		}
		t.Setenv(pair.file, secret)
		if _, err = Load(); err != nil {
			t.Fatalf("independent configured consumer: %v", err)
		}
		t.Setenv(pair.id, "")
		t.Setenv(pair.file, "")
	}
	for _, pair := range pairs {
		t.Setenv(pair.id, "same-consumer")
		t.Setenv(pair.file, secret)
	}
	if _, err = Load(); err == nil {
		t.Fatal("same consumer would over-authorize identity binding")
	}
	t.Setenv(pairs[1].id, "identity-consumer")
	cfg, err = Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Integrations.BitbucketOrgClientSecret != "fixture-secret" || cfg.Integrations.BitbucketPersonalClientSecret != "fixture-secret" {
		t.Fatal("secret loading")
	}
	if err = os.Chmod(secret, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err = Load(); err == nil {
		t.Fatal("accepted public secret file")
	}
}
