// Package safehttp 提供带 SSRF 防护的出站 HTTP 客户端：只访问公网 https 地址，
// 解析结果与实际连接的 IP 都要校验，重定向逐跳复查，不走代理、不带 Cookie。
package safehttp

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"syscall"
	"time"
)

var (
	// ErrBlockedAddress 表示目标解析到了内网、回环、链路本地等保留地址。
	ErrBlockedAddress = errors.New("safehttp: blocked address")
	// ErrInvalidURL 表示地址格式不合法（非 https、带用户信息或片段、缺少主机等）。
	ErrInvalidURL = errors.New("safehttp: invalid url")
	// ErrTooManyRedirects 表示重定向超过 3 次。
	ErrTooManyRedirects = errors.New("safehttp: too many redirects")
)

const (
	maxRedirects          = 3
	dialTimeout           = 10 * time.Second
	tlsHandshakeTimeout   = 10 * time.Second
	responseHeaderTimeout = 20 * time.Second
)

var deniedPrefixes = mustPrefixes(
	"0.0.0.0/8", "100.64.0.0/10", "192.0.0.0/24", "192.0.2.0/24", "192.88.99.0/24", "198.18.0.0/15",
	"198.51.100.0/24", "203.0.113.0/24", "240.0.0.0/4",
	"::/96", "64:ff9b::/96", "100::/64", "2001::/32", "2001:2::/48", "2001:db8::/32", "2002::/16", "fec0::/10",
)

func mustPrefixes(values ...string) []netip.Prefix {
	prefixes := make([]netip.Prefix, len(values))
	for i, value := range values {
		prefixes[i] = netip.MustParsePrefix(value)
	}
	return prefixes
}

// IPAllowed 判断一个地址是否可以作为出站目标：只放行全局单播地址，IPv4 映射的 IPv6 按其 IPv4 地址判断。
func IPAllowed(ip net.IP) bool {
	addr, ok := netip.AddrFromSlice(ip)
	if !ok {
		return false
	}
	addr = addr.Unmap().WithZone("")
	if !addr.IsGlobalUnicast() || addr.IsLoopback() || addr.IsPrivate() || addr.IsLinkLocalUnicast() ||
		addr.IsMulticast() || addr.IsUnspecified() {
		return false
	}
	for _, prefix := range deniedPrefixes {
		if prefix.Contains(addr) {
			return false
		}
	}
	return true
}

// Options 的零值就是生产的严格配置；字段只用于测试注入。
type Options struct {
	// AllowAddr 判断地址是否放行；为空时用 IPAllowed。
	AllowAddr func(net.IP) bool
	// RootCAs 为空时用系统根证书。
	RootCAs *x509.CertPool
	// LookupIP 解析主机名；为空时用系统解析器。
	LookupIP func(ctx context.Context, host string) ([]net.IP, error)
}

func (o Options) allow(ip net.IP) bool {
	if o.AllowAddr != nil {
		return o.AllowAddr(ip)
	}
	return IPAllowed(ip)
}

// CheckURL 静态校验地址：只允许 https，主机非空，不带用户信息与片段，端口不限。
func CheckURL(raw string) (*url.URL, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" || len(raw) > 2048 {
		return nil, ErrInvalidURL
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.Fragment != "" || u.Opaque != "" {
		return nil, ErrInvalidURL
	}
	return u, nil
}

// control 是 net.Dialer.Control：在实际连接的 IP 上再校验一次（防 DNS 重绑定）。
func (o Options) control(_, address string, _ syscall.RawConn) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return ErrBlockedAddress
	}
	addr, err := netip.ParseAddr(host)
	if err != nil || !o.allow(addr.AsSlice()) {
		return ErrBlockedAddress
	}
	return nil
}

func (o Options) dialContext(ctx context.Context, network, address string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, ErrInvalidURL
	}
	var ips []net.IP
	if addr, err := netip.ParseAddr(host); err == nil {
		ips = []net.IP{addr.AsSlice()}
	} else {
		lookup := o.LookupIP
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
		return nil, &net.DNSError{Err: "no addresses", Name: host, IsNotFound: true}
	}
	// 解析出的每个地址都必须放行，避免混入内网地址。
	for _, ip := range ips {
		if !o.allow(ip) {
			return nil, ErrBlockedAddress
		}
	}
	dialer := &net.Dialer{Timeout: dialTimeout, Control: o.control}
	var lastErr error
	for _, ip := range ips {
		conn, err := dialer.DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
		if err == nil {
			return conn, nil
		}
		lastErr = err
		if errors.Is(err, ErrBlockedAddress) {
			return nil, err
		}
	}
	return nil, lastErr
}

// Client 返回带防护的客户端。整体超时由调用方的 context 控制（不设置 Client.Timeout）。
func Client(opts Options) *http.Client {
	transport := &http.Transport{
		Proxy:                  nil,
		DialContext:            opts.dialContext,
		TLSClientConfig:        &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: opts.RootCAs},
		TLSHandshakeTimeout:    tlsHandshakeTimeout,
		ResponseHeaderTimeout:  responseHeaderTimeout,
		DisableKeepAlives:      true,
		DisableCompression:     false,
		MaxResponseHeaderBytes: 64 << 10,
	}
	return &http.Client{
		Transport: transport,
		// 不带 Cookie（Jar 为空）；重定向最多 3 次，每一跳重新校验 scheme 与 host。
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) > maxRedirects {
				return ErrTooManyRedirects
			}
			if _, err := CheckURL(req.URL.String()); err != nil {
				return err
			}
			return nil
		},
	}
}
