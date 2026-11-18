package integrations

import (
	"slices"
	"testing"
)

func TestBitbucketCatalogAndIndependentConfiguration(t *testing.T) {
	p, ok := Lookup(ProviderBitbucket)
	if !ok || p.Availability != AvailabilityAvailable || !slices.Equal(p.Capabilities, []string{capPRLink, capPersonal}) {
		t.Fatalf("unexpected Bitbucket capabilities: %+v", p)
	}
	s, _ := bbFixture(t)
	view := s.providerView(p, ProviderSummary{})
	if !view.Configured || !view.Bindable || view.OAuthRedirectURI != s.RedirectURIFor(ProviderBitbucket) || !s.BitbucketIdentityConfigured() {
		t.Fatalf("configured consumers not exposed: %+v", view)
	}
	s.BitbucketIdentity = nil
	if !s.providerView(p, ProviderSummary{}).Bindable || s.BitbucketIdentityConfigured() {
		t.Fatal("personal configuration must not disable organization authorization")
	}
	s.Bitbucket = nil
	if s.providerView(p, ProviderSummary{}).Bindable || s.providerView(p, ProviderSummary{}).Configured {
		t.Fatal("missing organization consumer must disable new organization authorization")
	}
	// Dedicated UUID repository control must not enter the generic Issue/OAuth
	// client factory, even when the product catalog is available.
	if IsGitProvider(ProviderBitbucket) {
		t.Fatal("Bitbucket must keep its dedicated repository and OAuth handlers")
	}
}
