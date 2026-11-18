// Package gitcode implements the documented AtomGit/GitCode API v5. It is PAT
// only: OAuth, personal account mapping and Git clone credentials are excluded.
package gitcode

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"openfox/internal/integrations/gitapi"
)

const API = "https://api.atomgit.com/api/v5"
const PageSize = 100
const MaxPages = 50
const maxBody = 8 << 20

var (
	ErrForbidden   = errors.New("gitcode: capability denied")
	ErrInvalid     = errors.New("gitcode: invalid response or parameter")
	ErrIncomplete  = errors.New("gitcode: incomplete listing")
	ErrUnsupported = errors.New("gitcode: unsupported capability")
)

// Client has a fixed API origin. Transport is server-owned, only for controlled
// HTTP/TLS tests; it is never derived from a request or stored connection.
type Client struct {
	http    *http.Client
	mu      sync.Mutex
	budgets map[[32]byte]*budget
}
type budget struct {
	inflight, minuteCount, hourCount int
	minute, hour, last               time.Time
}

func New(transport http.RoundTripper) *Client {
	if transport == nil {
		transport = &http.Transport{DialContext: (&net.Dialer{Timeout: 5 * time.Second}).DialContext,
			TLSHandshakeTimeout: 5 * time.Second, ResponseHeaderTimeout: 15 * time.Second,
			MaxIdleConns: 20, MaxIdleConnsPerHost: 4, IdleConnTimeout: 30 * time.Second}
	}
	return &Client{http: &http.Client{Transport: transport, Timeout: 15 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, budgets: make(map[[32]byte]*budget)}
}

// A credential has one conservative budget across its repositories/connections.
// The map is bounded and retains no token. There is no automatic POST retry.
func (c *Client) enter(token string) (func(), error) {
	now := time.Now()
	key := sha256.Sum256([]byte(token))
	c.mu.Lock()
	defer c.mu.Unlock()
	b := c.budgets[key]
	if b == nil {
		for k, old := range c.budgets {
			if old.inflight == 0 && now.Sub(old.last) > time.Hour {
				delete(c.budgets, k)
			}
		}
		if len(c.budgets) >= 256 {
			return nil, &gitapi.ErrRateLimited{ResetAt: now.Add(time.Minute)}
		}
		b = &budget{minute: now, hour: now}
		c.budgets[key] = b
	}
	if now.Sub(b.minute) >= time.Minute {
		b.minute = now
		b.minuteCount = 0
	}
	if now.Sub(b.hour) >= time.Hour {
		b.hour = now
		b.hourCount = 0
	}
	b.last = now
	if b.hourCount >= 3600 {
		return nil, &gitapi.ErrRateLimited{ResetAt: b.hour.Add(time.Hour)}
	}
	if b.minuteCount >= 360 {
		return nil, &gitapi.ErrRateLimited{ResetAt: b.minute.Add(time.Minute)}
	}
	if b.inflight >= 2 {
		return nil, &gitapi.ErrRateLimited{ResetAt: now.Add(time.Second)}
	}
	b.inflight++
	b.minuteCount++
	b.hourCount++
	return func() { c.mu.Lock(); b.inflight--; c.mu.Unlock() }, nil
}

func validToken(s string) bool {
	return len(s) > 0 && len(s) <= 4096 && !strings.ContainsAny(s, " \t\r\n\x00")
}
func ValidKey(s string) bool {
	return len(s) > 0 && len(s) <= 255 && strings.TrimSpace(s) == s && !strings.ContainsAny(s, "\r\n\x00")
}
func segment(s string) bool {
	if s == "" || len(s) > 255 || s == "." || s == ".." {
		return false
	}
	for _, v := range s {
		if !(v >= 'a' && v <= 'z' || v >= 'A' && v <= 'Z' || v >= '0' && v <= '9' || v == '_' || v == '-' || v == '.') {
			return false
		}
	}
	return true
}
func SplitRepository(path string) (string, string, error) {
	p := strings.Split(path, "/")
	if len(p) != 2 || !segment(p[0]) || !segment(p[1]) {
		return "", "", ErrInvalid
	}
	return p[0], p[1], nil
}
func repoPath(owner, repo string) (string, error) {
	if !segment(owner) || !segment(repo) {
		return "", ErrInvalid
	}
	return "/repos/" + owner + "/" + repo, nil
}
func pageQuery(page int) (url.Values, error) {
	if page < 1 || page > MaxPages {
		return nil, ErrIncomplete
	}
	return url.Values{"page": {strconv.Itoa(page)}, "per_page": {strconv.Itoa(PageSize)}}, nil
}

func RetryAt(value string, now time.Time) time.Time {
	d := time.Minute
	if n, e := strconv.Atoi(value); e == nil && n >= 0 {
		d = time.Duration(min(n, 3600)) * time.Second
	} else if t, e := http.ParseTime(value); e == nil {
		d = t.Sub(now)
	}
	return now.Add(max(time.Second, min(d, time.Hour)))
}

func (c *Client) request(ctx context.Context, token, method, path string, q url.Values, input, output any) error {
	if !validToken(token) || !strings.HasPrefix(path, "/") {
		return ErrInvalid
	}
	var payload []byte
	if input != nil {
		var err error
		payload, err = json.Marshal(input)
		if err != nil || len(payload) > maxBody {
			return ErrInvalid
		}
	}
	if q == nil {
		q = url.Values{}
	}
	req, err := http.NewRequestWithContext(ctx, method, API+path+"?"+q.Encode(), bytes.NewReader(payload))
	if err != nil {
		return ErrInvalid
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "OpenFox")
	if input != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	release, err := c.enter(token)
	if err != nil {
		return err
	}
	defer release()
	resp, err := c.http.Do(req)
	unknown := method == http.MethodPost
	failure := func() error {
		if unknown {
			return gitapi.ErrUnknownResult
		}
		return gitapi.ErrRequestFailed
	}
	if err != nil {
		return failure()
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case 401:
		return gitapi.ErrTokenRevoked
	case 403:
		return ErrForbidden
	case 404:
		return gitapi.ErrNotFound
	case 429:
		return &gitapi.ErrRateLimited{ResetAt: RetryAt(resp.Header.Get("Retry-After"), time.Now())}
	}
	if resp.StatusCode >= 500 {
		return failure()
	}
	if resp.StatusCode != http.StatusOK && !(method == http.MethodDelete && resp.StatusCode == http.StatusNoContent) {
		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			return failure()
		}
		return gitapi.ErrRequestFailed
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody+1))
	if err != nil || len(body) > maxBody {
		return failure()
	}
	if output == nil {
		return nil
	}
	if json.Unmarshal(body, output) != nil {
		return failure()
	}
	return nil
}

// responseList rejects null and non-array responses. Only an actual [] can
// establish that a pagination/reconciliation window is complete and empty.
type responseList[T any] []T

func (v *responseList[T]) UnmarshalJSON(b []byte) error {
	b = bytes.TrimSpace(b)
	if len(b) == 0 || b[0] != '[' {
		return ErrInvalid
	}
	type plain []T
	return json.Unmarshal(b, (*plain)(v))
}

// WebURL accepts display links only on the four documented fixed hosts.
func WebURL(raw, path, suffix string) bool {
	u, e := url.Parse(raw)
	if e != nil || u.Scheme != "https" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Port() != "" {
		return false
	}
	if u.Host != "atomgit.com" && u.Host != "gitcode.com" && u.Host != "api.atomgit.com" && u.Host != "api.gitcode.com" {
		return false
	}
	return u.Path == "/"+path+suffix && u.RawPath == ""
}
