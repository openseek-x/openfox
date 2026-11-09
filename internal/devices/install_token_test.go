package devices

import (
	"encoding/base64"
	"errors"
	"strings"
	"testing"
)

func TestInstallTokenRoundTrip(t *testing.T) {
	token, err := EncodeInstallToken("https://openfox.example.com/", "abcDEF_123-xyz")
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	if !strings.HasPrefix(token, InstallTokenPrefix) || len(token) > MaxInstallTokenLength {
		t.Fatalf("unexpected token shape: %q", token)
	}
	decoded, err := DecodeInstallToken(token)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if decoded.CenterURL != "https://openfox.example.com" || decoded.Secret != "abcDEF_123-xyz" {
		t.Fatalf("decoded = %+v", decoded)
	}
	secret, err := RegistrationSecret(token)
	if err != nil || secret != "abcDEF_123-xyz" {
		t.Fatalf("RegistrationSecret(v2) = %q, %v", secret, err)
	}
	legacy, err := RegistrationSecret("plain-legacy-token")
	if err != nil || legacy != "plain-legacy-token" {
		t.Fatalf("RegistrationSecret(legacy) = %q, %v", legacy, err)
	}
}

func TestInstallTokenRejectsMalformed(t *testing.T) {
	encode := func(raw string) string {
		return InstallTokenPrefix + base64.RawURLEncoding.EncodeToString([]byte(raw))
	}
	cases := map[string]string{
		"not base64":     InstallTokenPrefix + "***",
		"padded base64":  InstallTokenPrefix + base64.URLEncoding.EncodeToString([]byte(`{"v":2}`)),
		"not json":       encode("hello"),
		"wrong version":  encode(`{"v":1,"c":"https://a.example","s":"x"}`),
		"empty secret":   encode(`{"v":2,"c":"https://a.example","s":""}`),
		"space secret":   encode(`{"v":2,"c":"https://a.example","s":"a b"}`),
		"bad scheme":     encode(`{"v":2,"c":"ftp://a.example","s":"x"}`),
		"relative url":   encode(`{"v":2,"c":"/api","s":"x"}`),
		"userinfo url":   encode(`{"v":2,"c":"https://u:p@a.example","s":"x"}`),
		"query url":      encode(`{"v":2,"c":"https://a.example?x=1","s":"x"}`),
		"too long":       InstallTokenPrefix + strings.Repeat("A", MaxInstallTokenLength),
		"missing prefix": "ofx_v1_abc",
	}
	for name, token := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := DecodeInstallToken(token)
			if !errors.Is(err, ErrInvalidInstallToken) {
				t.Fatalf("err = %v", err)
			}
			if err != nil && strings.Contains(err.Error(), token) {
				t.Fatal("error must not echo the token")
			}
		})
	}
	if _, err := RegistrationSecret(InstallTokenPrefix + "***"); !errors.Is(err, ErrInvalidInstallToken) {
		t.Fatalf("RegistrationSecret malformed v2 err = %v", err)
	}
	if _, err := EncodeInstallToken("https://a.example", strings.Repeat("s", 200)); !errors.Is(err, ErrInvalidInstallToken) {
		t.Fatalf("oversized secret err = %v", err)
	}
}
