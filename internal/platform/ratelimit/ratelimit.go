// Package ratelimit provides a bounded, in-process fixed-window limiter keyed
// by an opaque string (for example a client IP). It protects unauthenticated
// endpoints such as email-code issuance from request floods.
//
// The limiter is per process: with N server replicas the effective limit is up
// to N times the configured value. Durable, cross-replica limits remain the
// responsibility of the reverse proxy or a shared store.
package ratelimit

import (
	"net"
	"net/http"
	"net/netip"
	"sort"
	"strings"
	"sync"
	"time"
)

// Limiter counts requests per key in fixed windows. Memory is bounded by
// MaxKeys: when the table is full the oldest windows are evicted in a batch,
// so a flood of distinct keys cannot lock out legitimate new clients.
type Limiter struct {
	limit   int
	window  time.Duration
	maxKeys int
	now     func() time.Time

	mu      sync.Mutex
	entries map[string]*entry
}

type entry struct {
	windowStart time.Time
	count       int
}

// Decision reports whether a request may proceed and, when rejected, how long
// the caller should wait before retrying.
type Decision struct {
	Allowed    bool
	RetryAfter time.Duration
}

// New returns a limiter allowing limit requests per window for each key.
// A non-positive limit disables limiting (every request is allowed).
func New(limit int, window time.Duration, maxKeys int, now func() time.Time) *Limiter {
	if now == nil {
		now = time.Now
	}
	if maxKeys <= 0 {
		maxKeys = 10000
	}
	return &Limiter{
		limit: limit, window: window, maxKeys: maxKeys, now: now,
		entries: make(map[string]*entry),
	}
}

// Allow records one request for key and returns the decision.
func (l *Limiter) Allow(key string) Decision {
	if l == nil || l.limit <= 0 || l.window <= 0 {
		return Decision{Allowed: true}
	}
	now := l.now()
	l.mu.Lock()
	defer l.mu.Unlock()

	current, ok := l.entries[key]
	if ok && now.Sub(current.windowStart) >= l.window {
		current.windowStart, current.count = now, 0
	}
	if !ok {
		if len(l.entries) >= l.maxKeys {
			l.evictLocked(now)
		}
		current = &entry{windowStart: now}
		l.entries[key] = current
	}
	if current.count >= l.limit {
		return Decision{Allowed: false, RetryAfter: current.windowStart.Add(l.window).Sub(now)}
	}
	current.count++
	return Decision{Allowed: true}
}

// evictLocked 在键表满时回收空间：先删除所有已过期窗口；仍不足时按窗口开始时间
// 淘汰最旧的一批（约 1/8），使全表扫描摊薄到每 maxKeys/8 个新键一次。
// 被淘汰的键相当于提前重置计数，这是为避免「拒绝所有新客户端」所接受的折中。
func (l *Limiter) evictLocked(now time.Time) {
	for key, value := range l.entries {
		if now.Sub(value.windowStart) >= l.window {
			delete(l.entries, key)
		}
	}
	target := l.maxKeys - l.maxKeys/8
	if target >= l.maxKeys {
		target = l.maxKeys - 1
	}
	if len(l.entries) <= target {
		return
	}
	type aged struct {
		key   string
		start time.Time
	}
	items := make([]aged, 0, len(l.entries))
	for key, value := range l.entries {
		items = append(items, aged{key, value.windowStart})
	}
	sort.Slice(items, func(a, b int) bool { return items[a].start.Before(items[b].start) })
	for _, item := range items[:len(items)-target] {
		delete(l.entries, item.key)
	}
}

// ClientIP resolves the client key used for rate limiting.
//
// X-Forwarded-For is honored only when the direct peer is one of the trusted
// proxy prefixes; otherwise a client could spoof the header to evade limits.
// All header lines are joined in order (proxies may append a new line rather
// than a comma), and the rightmost untrusted hop is used. Hops may carry a
// port or IPv6 brackets. An unparseable rightmost untrusted hop yields a
// dedicated key instead of the proxy address, so one malformed client cannot
// put every user behind that proxy into the same bucket.
//
// IPv6 clients are keyed by their /64 prefix: a single subscriber usually
// controls a whole /64, so per-address keys would let one client rotate
// through unlimited addresses.
func ClientIP(r *http.Request, trusted []netip.Prefix) string {
	peer := remoteIP(r.RemoteAddr)
	if !peer.IsValid() {
		return "invalid-peer"
	}
	if !isTrusted(peer, trusted) {
		return keyFor(peer)
	}
	var hops []string
	for _, line := range r.Header.Values("X-Forwarded-For") {
		hops = append(hops, strings.Split(line, ",")...)
	}
	for index := len(hops) - 1; index >= 0; index-- {
		raw := strings.TrimSpace(hops[index])
		if raw == "" {
			continue
		}
		candidate := parseHop(raw)
		if !candidate.IsValid() {
			return "invalid-forwarded"
		}
		if !isTrusted(candidate, trusted) {
			return keyFor(candidate)
		}
	}
	return keyFor(peer)
}

// parseHop accepts "ip", "ip:port", "[ipv6]" and "[ipv6]:port".
func parseHop(raw string) netip.Addr {
	if address, err := netip.ParseAddr(raw); err == nil {
		return address.Unmap()
	}
	if addrPort, err := netip.ParseAddrPort(raw); err == nil {
		return addrPort.Addr().Unmap()
	}
	if strings.HasPrefix(raw, "[") && strings.HasSuffix(raw, "]") {
		if address, err := netip.ParseAddr(raw[1 : len(raw)-1]); err == nil {
			return address.Unmap()
		}
	}
	return netip.Addr{}
}

func keyFor(address netip.Addr) string {
	address = address.WithZone("")
	if address.Is4() {
		return address.String()
	}
	prefix, err := address.Prefix(64)
	if err != nil {
		return address.String()
	}
	return prefix.String()
}

// ParseTrustedProxies parses a comma-separated list of CIDR prefixes or bare
// addresses.
func ParseTrustedProxies(value string) ([]netip.Prefix, error) {
	var prefixes []netip.Prefix
	for _, raw := range strings.Split(value, ",") {
		item := strings.TrimSpace(raw)
		if item == "" {
			continue
		}
		if strings.Contains(item, "/") {
			prefix, err := netip.ParsePrefix(item)
			if err != nil {
				return nil, err
			}
			prefixes = append(prefixes, prefix.Masked())
			continue
		}
		address, err := netip.ParseAddr(item)
		if err != nil {
			return nil, err
		}
		address = address.Unmap()
		prefixes = append(prefixes, netip.PrefixFrom(address, address.BitLen()))
	}
	return prefixes, nil
}

func remoteIP(remoteAddr string) netip.Addr {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		host = remoteAddr
	}
	address, err := netip.ParseAddr(host)
	if err != nil {
		return netip.Addr{}
	}
	return address.Unmap()
}

func isTrusted(address netip.Addr, trusted []netip.Prefix) bool {
	for _, prefix := range trusted {
		if prefix.Contains(address) {
			return true
		}
	}
	return false
}
