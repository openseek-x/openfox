package integrations

import (
	"openfox/internal/secrets"
	"testing"
)

func TestQQConfiguredRequiresHostname(t *testing.T) {
	box, err := secrets.New(make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range []struct {
		url  string
		want bool
	}{{"https://openfox.example.test", true}, {"https://openfox.example.test:8443/base", true}, {"https://:443", false}, {"http://openfox.example.test", false}, {"https://user@openfox.example.test", false}, {"https://openfox.example.test?secret=1", false}, {"https://openfox.example.test#frag", false}} {
		t.Run(v.url, func(t *testing.T) {
			if got := (Service{Box: box, PublicURL: v.url}).QQConfigured(); got != v.want {
				t.Fatalf("configured=%v want=%v", got, v.want)
			}
		})
	}
	if (Service{PublicURL: "https://openfox.example.test"}).QQConfigured() {
		t.Fatal("missing encryption enabled QQ")
	}
}
