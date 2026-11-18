package integrations

import (
	"bytes"
	"slices"
	"testing"

	"openfox/internal/secrets"
)

func TestTelegramCatalogAndConfiguration(t *testing.T) {
	p, ok := Lookup(ProviderTelegram)
	if !ok || !p.Bindable || p.Availability != AvailabilityAvailable || !slices.Equal(p.Capabilities, []string{capTaskAssistant, capAgentBinding, capPersonal}) {
		t.Fatalf("unexpected capabilities: %+v", p)
	}
	box, err := secrets.New(bytes.Repeat([]byte{41}, secrets.KeySize))
	if err != nil {
		t.Fatal(err)
	}
	for _, base := range []string{"", "http://localhost:4206", "https://user:secret@example.test", "https://example.test?tenant=x", "https://example.test#fragment"} {
		if v := (Service{Box: box, PublicURL: base}).providerView(p, ProviderSummary{}); v.Configured || v.Bindable {
			t.Fatalf("invalid webhook deployment marked configured: %q", base)
		}
	}
	if v := (Service{PublicURL: "https://openfox.example.test"}).providerView(p, ProviderSummary{}); v.Configured || v.Bindable {
		t.Fatal("encryption is required")
	}
	if v := (Service{Box: box, PublicURL: "https://openfox.example.test"}).providerView(p, ProviderSummary{}); !v.Configured || !v.Bindable || v.OAuthRedirectURI != "" {
		t.Fatalf("configured Telegram has no OAuth redirect: %+v", v)
	}
	if IsIMProvider(ProviderTelegram) {
		t.Fatal("Telegram must not enter the generic Feishu-compatible handlers")
	}
}
