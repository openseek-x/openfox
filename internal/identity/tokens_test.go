package identity

import (
	"bytes"
	"regexp"
	"slices"
	"strings"
	"testing"
)

func TestNewPersonalAccessTokenFormat(t *testing.T) {
	alphanumeric := regexp.MustCompile(`^[A-Za-z0-9]+$`)
	for i := 0; i < 64; i++ {
		secret, prefix, hash, err := NewPersonalAccessToken()
		if err != nil {
			t.Fatal(err)
		}
		if !tokenSecretPattern.MatchString(secret) {
			t.Fatalf("secret %q does not match the token format", secret)
		}
		random := secret[len(PersonalAccessTokenPrefix):]
		if !alphanumeric.MatchString(random) || len(random) != tokenSecretLength {
			t.Fatalf("random part %q must be %d alphanumeric characters", random, tokenSecretLength)
		}
		if prefix != PersonalAccessTokenPrefix+random[:4] {
			t.Fatalf("prefix %q does not match the secret", prefix)
		}
		if len(hash) != 32 || !bytes.Equal(hash, HashPersonalAccessToken(secret)) {
			t.Fatalf("hash must be the 32-byte SHA-256 of the secret")
		}
	}
}

func TestPersonalAccessTokenSecretPattern(t *testing.T) {
	body := strings.Repeat("0", tokenSecretLength)
	cases := map[string]bool{
		PersonalAccessTokenPrefix + body: true,
		"":                               false,
		PersonalAccessTokenPrefix:        false,
		PersonalAccessTokenPrefix + body[:tokenSecretLength-1]:       false,
		PersonalAccessTokenPrefix + body + "0":                       false,
		PersonalAccessTokenPrefix + body[:tokenSecretLength-1] + "-": false,
		PersonalAccessTokenPrefix + body[:tokenSecretLength-1] + "_": false,
		"ofx_sess_" + body: false,
	}
	for input, want := range cases {
		if got := tokenSecretPattern.MatchString(input); got != want {
			t.Fatalf("pattern match %q = %v, want %v", input, got, want)
		}
	}
}

func TestIsPersonalAccessToken(t *testing.T) {
	cases := map[string]bool{
		"ofx_pat_abcd": true,
		"OFX_PAT_abcd": false,
		"ofx_patabcd":  false,
		"":             false,
	}
	for input, want := range cases {
		if got := IsPersonalAccessToken(input); got != want {
			t.Fatalf("IsPersonalAccessToken(%q) = %v, want %v", input, got, want)
		}
	}
}

func TestPersonalAccessTokenExpiryDaysAllowList(t *testing.T) {
	for _, days := range []int{30, 90, 365} {
		if !slices.Contains(personalAccessTokenExpiryDays, days) {
			t.Fatalf("%d days must be allowed", days)
		}
	}
	for _, days := range []int{0, 1, 29, 31, 366, -30} {
		if slices.Contains(personalAccessTokenExpiryDays, days) {
			t.Fatalf("%d days must be rejected", days)
		}
	}
}
