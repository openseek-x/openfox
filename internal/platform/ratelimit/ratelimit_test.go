package ratelimit

import (
	"fmt"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

func TestLimiterAllowsWithinWindowAndResets(t *testing.T) {
	now := time.Date(2026, 10, 25, 8, 0, 0, 0, time.UTC)
	limiter := New(2, time.Minute, 10, func() time.Time { return now })
	for index := 0; index < 2; index++ {
		if !limiter.Allow("203.0.113.1").Allowed {
			t.Fatalf("request %d rejected within limit", index)
		}
	}
	decision := limiter.Allow("203.0.113.1")
	if decision.Allowed || decision.RetryAfter != time.Minute {
		t.Fatalf("third request = %#v, want rejected with 1m retry", decision)
	}
	if !limiter.Allow("203.0.113.2").Allowed {
		t.Fatal("independent key was throttled")
	}
	now = now.Add(time.Minute)
	if !limiter.Allow("203.0.113.1").Allowed {
		t.Fatal("key was not reset after the window elapsed")
	}
}

func TestLimiterEvictsOldestInsteadOfRejectingNewKeys(t *testing.T) {
	now := time.Date(2026, 10, 25, 8, 0, 0, 0, time.UTC)
	limiter := New(1, time.Hour, 64, func() time.Time { return now })
	for index := 0; index < 64; index++ {
		now = now.Add(time.Second)
		limiter.Allow(fmt.Sprintf("key-%02d", index))
	}
	// 键表已满且没有过期项：新客户端仍应被放行，而不是被拒绝（防全站 DoS）。
	now = now.Add(time.Second)
	if !limiter.Allow("newcomer").Allowed {
		t.Fatal("full table rejected a new legitimate key")
	}
	if len(limiter.entries) > 64 {
		t.Fatalf("entries = %d, want bounded by max keys", len(limiter.entries))
	}
	if _, kept := limiter.entries["key-00"]; kept {
		t.Fatal("oldest window was not evicted")
	}
	if _, kept := limiter.entries["key-63"]; !kept {
		t.Fatal("newest window was evicted before older ones")
	}
	// 一次淘汰一批（64/8=8 个），后续若干个新键不再触发淘汰。
	before := len(limiter.entries)
	for index := 0; index < 4; index++ {
		limiter.Allow(fmt.Sprintf("extra-%d", index))
	}
	if len(limiter.entries) != before+4 {
		t.Fatalf("entries = %d after batch eviction, want %d", len(limiter.entries), before+4)
	}
}

func TestLimiterExpiredEntriesAreReclaimedFirst(t *testing.T) {
	now := time.Date(2026, 10, 25, 8, 0, 0, 0, time.UTC)
	limiter := New(1, time.Minute, 2, func() time.Time { return now })
	limiter.Allow("a")
	limiter.Allow("b")
	now = now.Add(time.Minute)
	if !limiter.Allow("c").Allowed || len(limiter.entries) != 1 {
		t.Fatalf("expired entries not reclaimed: %d entries", len(limiter.entries))
	}
}

func TestLimiterDisabledAndConcurrent(t *testing.T) {
	if !New(0, time.Minute, 10, nil).Allow("x").Allowed {
		t.Fatal("disabled limiter rejected a request")
	}
	limiter := New(50, time.Minute, 10, nil)
	var allowed int
	var mu sync.Mutex
	var wg sync.WaitGroup
	for index := 0; index < 200; index++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if limiter.Allow("same").Allowed {
				mu.Lock()
				allowed++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if allowed != 50 {
		t.Fatalf("allowed = %d under concurrency, want exactly 50", allowed)
	}
}

func TestClientIPHonorsForwardedOnlyFromTrustedProxy(t *testing.T) {
	trusted, err := ParseTrustedProxies("10.0.0.0/8, 127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	direct := httptest.NewRequest("POST", "/", nil)
	direct.RemoteAddr = "198.51.100.7:5000"
	direct.Header.Set("X-Forwarded-For", "1.2.3.4")
	if got := ClientIP(direct, trusted); got != "198.51.100.7" {
		t.Fatalf("untrusted peer = %q, want spoofed header ignored", got)
	}
	proxied := httptest.NewRequest("POST", "/", nil)
	proxied.RemoteAddr = "10.1.2.3:443"
	proxied.Header.Set("X-Forwarded-For", "9.9.9.9, 203.0.113.9, 10.0.0.5")
	if got := ClientIP(proxied, trusted); got != "203.0.113.9" {
		t.Fatalf("proxied = %q, want rightmost untrusted hop", got)
	}
	noHeader := httptest.NewRequest("POST", "/", nil)
	noHeader.RemoteAddr = "127.0.0.1:1"
	if got := ClientIP(noHeader, trusted); got != "127.0.0.1" {
		t.Fatalf("trusted peer without header = %q", got)
	}
	garbage := httptest.NewRequest("POST", "/", nil)
	garbage.RemoteAddr = "10.0.0.2:1"
	garbage.Header.Set("X-Forwarded-For", "not-an-ip")
	if got := ClientIP(garbage, trusted); got == "10.0.0.2" {
		t.Fatalf("malformed header = %q, must not fall back to the shared proxy bucket", got)
	}
	if _, err := ParseTrustedProxies("bad/99"); err == nil {
		t.Fatal("invalid prefix accepted")
	}
}

func TestClientIPHandlesPortsBracketsAndMultipleHeaderLines(t *testing.T) {
	trusted, _ := ParseTrustedProxies("10.0.0.0/8")
	cases := []struct {
		name  string
		lines []string
		want  string
	}{
		{"ipv4 with port", []string{"203.0.113.9:5678"}, "203.0.113.9"},
		{"bracketed ipv6 with port", []string{"[2001:db8::1]:443"}, "2001:db8::/64"},
		{"bracketed ipv6", []string{"[2001:db8::2]"}, "2001:db8::/64"},
		// 代理另起一行追加时，客户端伪造的首行不能被采信。
		{"proxy appends a new line", []string{"6.6.6.6", "203.0.113.7"}, "203.0.113.7"},
		{"empty hops are skipped", []string{"203.0.113.8, "}, "203.0.113.8"},
	}
	for _, test := range cases {
		request := httptest.NewRequest("POST", "/", nil)
		request.RemoteAddr = "10.0.0.1:1"
		for _, line := range test.lines {
			request.Header.Add("X-Forwarded-For", line)
		}
		if got := ClientIP(request, trusted); got != test.want {
			t.Errorf("%s: ClientIP = %q, want %q", test.name, got, test.want)
		}
	}
}

func TestClientIPGroupsIPv6ByPrefix64(t *testing.T) {
	first := httptest.NewRequest("POST", "/", nil)
	first.RemoteAddr = "[2001:db8:1:2::a]:1"
	second := httptest.NewRequest("POST", "/", nil)
	second.RemoteAddr = "[2001:db8:1:2:ffff::b]:1"
	other := httptest.NewRequest("POST", "/", nil)
	other.RemoteAddr = "[2001:db8:1:3::a]:1"
	if ClientIP(first, nil) != ClientIP(second, nil) {
		t.Fatalf("same /64 produced different keys: %q vs %q", ClientIP(first, nil), ClientIP(second, nil))
	}
	if ClientIP(first, nil) == ClientIP(other, nil) {
		t.Fatal("different /64 prefixes shared a key")
	}
	mapped := httptest.NewRequest("POST", "/", nil)
	mapped.RemoteAddr = "[::ffff:198.51.100.7]:1"
	if got := ClientIP(mapped, nil); got != "198.51.100.7" {
		t.Fatalf("ipv4-mapped peer = %q, want plain ipv4", got)
	}
}
