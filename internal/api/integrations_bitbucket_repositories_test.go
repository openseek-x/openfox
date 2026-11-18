package api

import (
	"errors"
	"strings"
	"testing"
	"time"

	"openfox/internal/integrations"
	"openfox/internal/integrations/bitbucket"
	"openfox/internal/secrets"
)

func TestBitbucketRepositoryCursorScopeAndFence(t *testing.T) {
	box, e := secrets.New(make([]byte, 32))
	if e != nil {
		t.Fatal(e)
	}
	access := integrations.BitbucketAccess{WorkspaceID: "workspace", ConnectionVersion: 4, CredentialGeneration: 6}
	aad := bitbucketCursorAAD("org", "actor", "parent")
	cur, e := openBitbucketRepoCursor(box, "", aad, access)
	if e != nil {
		t.Fatal(e)
	}
	cur, eof, e := advanceBitbucketRepoCursor(cur, bitbucket.Page[bitbucket.Repository]{Values: make([]bitbucket.Repository, 100), Next: "https://api.bitbucket.org/2.0/repositories/fixture?page=2"})
	if e != nil || eof {
		t.Fatal(e, eof)
	}
	token, e := sealBitbucketRepoCursor(box, cur, aad)
	if e != nil {
		t.Fatal(e)
	}
	got, e := openBitbucketRepoCursor(box, token, aad, access)
	if e != nil || got.Count != 100 || got.Next != cur.Next {
		t.Fatalf("cursor=%+v error=%v", got, e)
	}
	for _, other := range []string{bitbucketCursorAAD("other", "actor", "parent"), bitbucketCursorAAD("org", "other", "parent"), bitbucketCursorAAD("org", "actor", "other")} {
		if _, e = openBitbucketRepoCursor(box, token, other, access); !errors.Is(e, integrations.ErrInvalidRequest) {
			t.Fatal("cross scope cursor accepted", e)
		}
	}
	changed := access
	changed.CredentialGeneration++
	if _, e = openBitbucketRepoCursor(box, token, aad, changed); !errors.Is(e, integrations.ErrVersionConflict) {
		t.Fatal(e)
	}
	changed = access
	changed.ConnectionVersion++
	if _, e = openBitbucketRepoCursor(box, token, aad, changed); !errors.Is(e, integrations.ErrVersionConflict) {
		t.Fatal(e)
	}
	cur.Expires = time.Now().Add(-time.Second).Unix()
	expired, _ := sealBitbucketRepoCursor(box, cur, aad)
	if _, e = openBitbucketRepoCursor(box, expired, aad, access); !errors.Is(e, integrations.ErrInvalidRequest) {
		t.Fatal("expired accepted")
	}
	for _, bad := range []string{token[:len(token)-4] + "AAAA", strings.Repeat("A", 32769), "unsealed-next-url"} {
		if _, e = openBitbucketRepoCursor(box, bad, aad, access); e == nil {
			t.Fatal("malformed accepted")
		}
	}
}
func TestBitbucketRepositoryCursorBounds(t *testing.T) {
	for _, tc := range []struct {
		name            string
		count, pages, n int
		next            string
		limited, failed bool
	}{
		{"terminal1000", 900, 9, 100, "", false, false},
		{"truncated1000", 900, 9, 100, "next", true, false},
		{"page20", 20, 19, 1, "next", true, false},
		{"over1000", 950, 9, 100, "", false, true},
		{"oversizedPage", 0, 0, 101, "", false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, limited, e := advanceBitbucketRepoCursor(bitbucketRepoCursor{Count: tc.count, Pages: tc.pages}, bitbucket.Page[bitbucket.Repository]{Values: make([]bitbucket.Repository, tc.n), Next: tc.next})
			if (e != nil) != tc.failed || limited != tc.limited || limited && out.Next != "" {
				t.Fatalf("out=%+v limited=%v error=%v", out, limited, e)
			}
		})
	}
	cur, _, e := advanceBitbucketRepoCursor(bitbucketRepoCursor{}, bitbucket.Page[bitbucket.Repository]{Next: "cycle"})
	if e != nil {
		t.Fatal(e)
	}
	if _, _, e = advanceBitbucketRepoCursor(cur, bitbucket.Page[bitbucket.Repository]{Next: "cycle"}); !errors.Is(e, bitbucket.ErrIncomplete) {
		t.Fatal("loop accepted", e)
	}
}

func TestBitbucketCursorMissingBoxIsConfigurationError(t *testing.T) {
	for _, raw := range []string{"", "previously-issued-cursor"} {
		if _, err := openBitbucketRepoCursor(nil, raw, "aad", integrations.BitbucketAccess{}); !errors.Is(err, integrations.ErrNotConfigured) {
			t.Fatalf("missing Box: %v", err)
		}
	}
	if _, err := sealBitbucketRepoCursor(nil, bitbucketRepoCursor{}, "aad"); !errors.Is(err, integrations.ErrNotConfigured) {
		t.Fatalf("seal missing Box: %v", err)
	}
}
