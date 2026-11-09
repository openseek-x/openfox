package skills

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"path"
	"regexp"
	"strings"
	"syscall"
	"time"

	"openfox/internal/platform/safehttp"
)

// URL 导入。SSRF 防护要点（规格 5c 第二节 7）：
//   - 只允许 https 与 443 端口，URL 不带用户名密码；
//   - DNS 解析后任何一个地址落在回环、内网、链路本地、CGNAT、组播、未指定、保留段（含 IPv4 映射的 IPv6）都拒绝；
//   - 实际连接的 IP 在 net.Dialer.Control 里再校验一次（防 DNS 重绑定）；
//   - 重定向 ≤ 3 次，每一跳重新校验 scheme / 端口 / 主机；
//   - 整体超时 15 秒，响应体限 5 MB，不带 Cookie，不走代理，User-Agent 固定。

const (
	fetchUserAgent    = "OpenFox-SkillImporter/1"
	fetchTimeout      = 15 * time.Second
	maxRedirects      = 3
	maxTreeDirs       = 50
	githubHost        = "github.com"
	githubRawHost     = "raw.githubusercontent.com"
	githubAPIHost     = "api.github.com"
	maxGitHubListBody = 1 << 20
)

// Fetcher 下载并解析 URL 指向的技能。零值即生产配置（严格）；字段只用于测试注入。
type Fetcher struct {
	// LookupIP 解析主机名；为空时用系统解析器。
	LookupIP func(ctx context.Context, host string) ([]net.IP, error)
	// Dial 在地址校验通过后建立连接；为空时用带 Control 校验的严格拨号器。
	Dial func(ctx context.Context, network, address string) (net.Conn, error)
	// TLSConfig 为空时用默认配置（校验证书）。
	TLSConfig *tls.Config
	// Timeout 为空时 15 秒。
	Timeout time.Duration
}

// FetchResult 是下载得到的技能文件与来源主机名。
type FetchResult struct {
	Files []File
	Host  string
}

// IPAllowed 判断一个地址是否可以作为下载目标（规则在 safehttp 中统一维护）。
func IPAllowed(ip net.IP) bool { return safehttp.IPAllowed(ip) }

// checkURL 校验一个 URL（含每一跳重定向）：https、443、无用户信息、有主机；IP 字面量直接判断地址。
func checkURL(u *url.URL) error {
	if u.Scheme != "https" || u.User != nil || u.Hostname() == "" {
		return ErrURLForbidden
	}
	if port := u.Port(); port != "" && port != "443" {
		return ErrURLForbidden
	}
	if addr, err := netip.ParseAddr(strings.Trim(u.Hostname(), "[]")); err == nil && !IPAllowed(addr.AsSlice()) {
		return ErrURLForbidden
	}
	return nil
}

// ParseURL 解析并做静态校验：格式不合法 → ErrURLInvalid，目标不允许 → ErrURLForbidden。
func ParseURL(raw string) (*url.URL, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" || len(raw) > 2048 {
		return nil, ErrURLInvalid
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" {
		return nil, ErrURLInvalid
	}
	if u.Scheme != "https" {
		return nil, ErrURLForbidden
	}
	if u.Host == "" {
		return nil, ErrURLInvalid
	}
	if err := checkURL(u); err != nil {
		return nil, err
	}
	return u, nil
}

// controlAddress 是 net.Dialer.Control：在实际连接的 IP 上再校验一次。
func controlAddress(network, address string, _ syscall.RawConn) error {
	host, port, err := net.SplitHostPort(address)
	if err != nil || port != "443" {
		return ErrURLForbidden
	}
	addr, err := netip.ParseAddr(host)
	if err != nil || !IPAllowed(addr.AsSlice()) {
		return ErrURLForbidden
	}
	return nil
}

func strictDialer() *net.Dialer {
	return &net.Dialer{Timeout: 10 * time.Second, Control: controlAddress}
}

// dialContext 解析主机、校验全部解析结果后，逐个地址连接。
func (f Fetcher) dialContext(ctx context.Context, network, address string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil || port != "443" {
		return nil, ErrURLForbidden
	}
	var ips []net.IP
	if addr, err := netip.ParseAddr(host); err == nil {
		ips = []net.IP{addr.AsSlice()}
	} else {
		lookup := f.LookupIP
		if lookup == nil {
			lookup = func(ctx context.Context, host string) ([]net.IP, error) {
				addrs, err := net.DefaultResolver.LookupIPAddr(ctx, host)
				result := make([]net.IP, 0, len(addrs))
				for _, a := range addrs {
					result = append(result, a.IP)
				}
				return result, err
			}
		}
		if ips, err = lookup(ctx, host); err != nil {
			return nil, fmt.Errorf("resolve %s: %w", host, err)
		}
	}
	if len(ips) == 0 {
		return nil, fmt.Errorf("resolve %s: no addresses", host)
	}
	for _, ip := range ips {
		if !IPAllowed(ip) {
			return nil, ErrURLForbidden
		}
	}
	dial := f.Dial
	if dial == nil {
		dial = strictDialer().DialContext
	}
	var lastErr error
	for _, ip := range ips {
		conn, err := dial(ctx, network, net.JoinHostPort(ip.String(), port))
		if err == nil {
			return conn, nil
		}
		lastErr = err
		if errors.Is(err, ErrURLForbidden) {
			return nil, err
		}
	}
	return nil, lastErr
}

func (f Fetcher) client() *http.Client {
	timeout := f.Timeout
	if timeout <= 0 {
		timeout = fetchTimeout
	}
	transport := &http.Transport{
		Proxy:                  nil,
		DialContext:            f.dialContext,
		TLSClientConfig:        f.TLSConfig,
		TLSHandshakeTimeout:    10 * time.Second,
		ResponseHeaderTimeout:  timeout,
		DisableKeepAlives:      true,
		DisableCompression:     true,
		MaxResponseHeaderBytes: 64 << 10,
	}
	return &http.Client{
		Transport: transport,
		Timeout:   timeout,
		// 不带 Cookie（Jar 为空），重定向 ≤ 3 次且逐跳校验。
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) > maxRedirects {
				return errTooManyRedirects
			}
			return checkURL(req.URL)
		},
	}
}

var errTooManyRedirects = errors.New("too many redirects")

// get 下载一个 URL，响应体最多读 limit 字节；超过 limit 返回 ErrTooLarge。
func (f Fetcher) get(ctx context.Context, client *http.Client, u *url.URL, limit int64, accept string) ([]byte, error) {
	if err := checkURL(u); err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, ErrURLInvalid
	}
	req.Header.Set("User-Agent", fetchUserAgent)
	if accept != "" {
		req.Header.Set("Accept", accept)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, classifyFetchError(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, FetchFailed(fmt.Sprintf("download failed with HTTP %d", resp.StatusCode))
	}
	if resp.ContentLength > limit {
		return nil, ErrTooLarge
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, classifyFetchError(err)
	}
	if int64(len(body)) > limit {
		return nil, ErrTooLarge
	}
	return body, nil
}

// classifyFetchError 把网络错误映射为接口错误：目标被拒绝保持 400，其余为 422 并给出可展示的原因。
func classifyFetchError(err error) error {
	switch {
	case errors.Is(err, ErrURLForbidden):
		return ErrURLForbidden
	case errors.Is(err, errTooManyRedirects):
		return FetchFailed("too many redirects")
	case errors.Is(err, context.DeadlineExceeded):
		return FetchFailed("download timed out")
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return FetchFailed("download timed out")
	}
	return FetchFailed("download failed")
}

// Fetch 下载 rawURL 指向的技能：.zip、单个 SKILL.md / .md，或 GitHub 的 blob / tree 地址。
func (f Fetcher) Fetch(ctx context.Context, rawURL string) (FetchResult, error) {
	u, err := ParseURL(rawURL)
	if err != nil {
		return FetchResult{}, err
	}
	timeout := f.Timeout
	if timeout <= 0 {
		timeout = fetchTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	client := f.client()
	defer client.CloseIdleConnections()

	result := FetchResult{Host: strings.ToLower(u.Hostname())}
	target, tree, err := GitHubTarget(u)
	if err != nil {
		return FetchResult{}, err
	}
	if tree != nil {
		result.Files, err = f.fetchGitHubTree(ctx, client, tree)
		return result, err
	}
	body, err := f.get(ctx, client, target, MaxTotalBytes, "")
	if err != nil {
		return FetchResult{}, err
	}
	if isZip(u.Path, body) {
		result.Files, err = UnzipFiles(body)
		return result, err
	}
	result.Files = []File{{Path: SkillFile, Content: body}}
	return result, nil
}

func isZip(urlPath string, body []byte) bool {
	return strings.HasSuffix(strings.ToLower(urlPath), ".zip") || bytes.HasPrefix(body, []byte("PK\x03\x04"))
}

// GitHubTree 描述要通过 contents 接口列出的 GitHub 目录。
type GitHubTree struct {
	Owner, Repo, Ref, Dir string
}

var githubNamePattern = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

// GitHubTarget 转换 GitHub 地址：
//
//	github.com/<o>/<r>/blob/<ref>/<path> → raw.githubusercontent.com/<o>/<r>/<ref>/<path>
//	github.com/<o>/<r>/tree/<ref>/<dir>  → 目录（走 api.github.com contents 接口）
//
// 其他地址原样返回（直链）。
func GitHubTarget(u *url.URL) (*url.URL, *GitHubTree, error) {
	host := strings.ToLower(u.Hostname())
	if host != githubHost && host != "www."+githubHost {
		return u, nil, nil
	}
	segments := strings.Split(strings.Trim(u.EscapedPath(), "/"), "/")
	if len(segments) < 4 || (segments[2] != "blob" && segments[2] != "tree") {
		return u, nil, nil
	}
	decoded := make([]string, len(segments))
	for i, segment := range segments {
		value, err := url.PathUnescape(segment)
		if err != nil {
			return nil, nil, ErrURLInvalid
		}
		decoded[i] = value
	}
	owner, repo, kind, ref := decoded[0], decoded[1], decoded[2], decoded[3]
	if !githubNamePattern.MatchString(owner) || !githubNamePattern.MatchString(repo) || ref == "" {
		return nil, nil, ErrURLInvalid
	}
	rest := decoded[4:]
	for _, segment := range rest {
		if segment == "" || segment == "." || segment == ".." {
			return nil, nil, ErrURLInvalid
		}
	}
	if kind == "tree" {
		return nil, &GitHubTree{Owner: owner, Repo: repo, Ref: ref, Dir: strings.Join(rest, "/")}, nil
	}
	if len(rest) == 0 {
		return nil, nil, ErrURLInvalid
	}
	raw := &url.URL{Scheme: "https", Host: githubRawHost, Path: "/" + owner + "/" + repo + "/" + ref + "/" + strings.Join(rest, "/")}
	return raw, nil, nil
}

type githubItem struct {
	Name        string  `json:"name"`
	Path        string  `json:"path"`
	Type        string  `json:"type"`
	Size        int64   `json:"size"`
	DownloadURL *string `json:"download_url"`
}

// fetchGitHubTree 递归列出目录并逐个下载文件：≤ 100 个文件、合计 ≤ 5 MB。
func (f Fetcher) fetchGitHubTree(ctx context.Context, client *http.Client, tree *GitHubTree) ([]File, error) {
	var files []File
	var total int64
	dirs := 0
	var walk func(dir string) error
	walk = func(dir string) error {
		if dirs++; dirs > maxTreeDirs {
			return ErrTooLarge
		}
		listURL := &url.URL{Scheme: "https", Host: githubAPIHost,
			Path: "/repos/" + tree.Owner + "/" + tree.Repo + "/contents" + prefixSlash(dir), RawQuery: "ref=" + url.QueryEscape(tree.Ref)}
		body, err := f.get(ctx, client, listURL, maxGitHubListBody, "application/vnd.github+json")
		if err != nil {
			return err
		}
		var items []githubItem
		if err := json.Unmarshal(body, &items); err != nil {
			return FetchFailed("the GitHub path is not a directory")
		}
		for _, item := range items {
			switch item.Type {
			case "dir":
				if err := walk(item.Path); err != nil {
					return err
				}
			case "file":
				if len(files) >= MaxFiles || total+item.Size > MaxTotalBytes {
					return ErrTooLarge
				}
				if item.DownloadURL == nil {
					return FetchFailed("a GitHub file has no download address")
				}
				downloadURL, err := url.Parse(*item.DownloadURL)
				if err != nil || !strings.EqualFold(downloadURL.Hostname(), githubRawHost) {
					return FetchFailed("a GitHub file has an unexpected download address")
				}
				content, err := f.get(ctx, client, downloadURL, MaxTotalBytes-total, "")
				if err != nil {
					return err
				}
				total += int64(len(content))
				relative := strings.TrimPrefix(strings.TrimPrefix(item.Path, tree.Dir), "/")
				if tree.Dir == "" {
					relative = item.Path
				}
				files = append(files, File{Path: path.Clean(relative), Content: content})
			default:
				return ErrInvalid
			}
		}
		return nil
	}
	if err := walk(tree.Dir); err != nil {
		return nil, err
	}
	return files, nil
}

func prefixSlash(dir string) string {
	if dir == "" {
		return ""
	}
	return "/" + dir
}
