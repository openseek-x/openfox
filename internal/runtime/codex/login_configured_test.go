package codex

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"

	"openfox/internal/runtime"
)

func TestDetectConfiguredAPIKeyDoesNotClaimAuthentication(t *testing.T) {
	for _, tc := range []struct {
		name, output       string
		exit               int
		unsupported, ready bool
		login              runtime.LoginStatus
	}{
		{"configured", "API key configured - SYNTH***ONLY\nAuthentication is checked when you send a request.", 0, false, true, runtime.LoginUnknown},
		{"configured-no-preview", "API key configured", 0, false, true, runtime.LoginUnknown},
		{"empty-preview", "API key configured - ", 0, false, false, runtime.LoginUnknown},
		{"nonzero", "API key configured - SYNTH***ONLY", 1, false, false, runtime.LoginUnknown},
		{"unknown-prefix", "Notice: API key configured - SYNTH***ONLY", 0, false, false, runtime.LoginUnknown},
		{"not-first-line", "Unknown\nAPI key configured - SYNTH***ONLY", 0, false, false, runtime.LoginUnknown},
		{"logged-out", "Not logged in", 0, false, false, runtime.LoginLoggedOut},
		{"existing-login", "Logged in using ChatGPT", 0, false, true, runtime.LoginLoggedIn},
		{"version-denied", "API key configured - SYNTH***ONLY", 0, true, false, runtime.LoginUnknown},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t, "success")
			script := strings.Replace(fakeAppServer, "if [ \"$mode\" = \"logged_out\" ]; then echo 'Not logged in'; else echo 'Logged in using fixture'; fi\n    exit 0", fmt.Sprintf("printf '%%s\\n' '%s'\n    exit %d", tc.output, tc.exit), 1)
			if err := os.WriteFile(f.adapter.Executable, []byte(script), 0700); err != nil {
				t.Fatal(err)
			}
			if tc.unsupported {
				f.adapter.SupportedVersions = []string{"other"}
			}
			got, err := f.adapter.Detect(context.Background())
			if err != nil || got.Login != tc.login || got.Runnable != tc.ready {
				t.Fatal(got, err)
			}
			if strings.Contains(got.Reason, "SYNTH") {
				t.Fatal("masked credential preview leaked")
			}
			if tc.ready && tc.login == runtime.LoginUnknown && !strings.Contains(got.Reason, "authentication is verified when") {
				t.Fatal(got.Reason)
			}
			if tc.unsupported && got.Reason != "Codex version is not in the supported version matrix" {
				t.Fatal(got.Reason)
			}
		})
	}
}
