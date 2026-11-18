package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestZohoOAuthFile(t *testing.T) {
	const good = `{"region":"US","organization":{"client_id":"org-id","client_secret":"org-secret"},"identity":{"client_id":"identity-id","client_secret":"identity-secret"}}`
	for _, tc := range []struct {
		name, raw string
		mode      os.FileMode
		ok        bool
	}{
		{"valid", good, 0600, true}, {"group-readable", good, 0640, false}, {"wrong-region", strings.Replace(good, `"US"`, `"EU"`, 1), 0600, false}, {"same-app", strings.Replace(good, "identity-id", "org-id", 1), 0600, false}, {"unknown-key", strings.Replace(good, `"region"`, `"unknown"`, 1), 0600, false}, {"trailing", good + `{}`, 0600, false}, {"oversize", strings.Repeat("x", 16385), 0600, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := filepath.Join(t.TempDir(), "apps.json")
			if e := os.WriteFile(p, []byte(tc.raw), tc.mode); e != nil {
				t.Fatal(e)
			}
			_, e := readZohoOAuthApps(p)
			if (e == nil) != tc.ok {
				t.Fatalf("accepted=%v", e == nil)
			}
			if e != nil && strings.Contains(e.Error(), "org-secret") {
				t.Fatal("secret in error")
			}
		})
	}
	p := filepath.Join(t.TempDir(), "apps.json")
	_ = os.WriteFile(p, []byte(good), 0600)
	link := p + ".link"
	_ = os.Symlink(p, link)
	if _, e := readZohoOAuthApps(link); e == nil {
		t.Fatal("symlink accepted")
	}
	if _, e := readZohoOAuthApps("relative.json"); e == nil {
		t.Fatal("relative path accepted")
	}
}
