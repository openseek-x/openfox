package hermes

import (
	"strings"
	"testing"
)

func TestStreamMaskAcrossEveryChunkBoundary(t *testing.T) {
	for _, secret := range []string{"provider-secret-canary", "本地令牌", "b"} {
		for split := 0; split <= len(secret); split++ {
			m := streamMask{secrets: []string{secret, "provider-secret-canary-more"}}
			got := m.feed("前 "+secret[:split]) + m.feed(secret[split:]+" 后") + m.finish()
			if got != "前 [REDACTED] 后" {
				t.Fatalf("split=%d got=%q", split, got)
			}
		}
	}
	m := streamMask{secrets: []string{"abcdef", "abc"}}
	got := m.feed("ab") + m.feed("cd") + m.finish()
	if strings.Contains(got, "ab") || got != "[REDACTED]" {
		t.Fatal("partial secret escaped")
	}
}
