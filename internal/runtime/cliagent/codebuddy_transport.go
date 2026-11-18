package cliagent

import (
	"bytes"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"time"
	"unicode"

	"golang.org/x/net/http/httpproxy"
)

const codeBuddyMaxCABytes = 4 << 20

// The supervisor's first preflight precedes workspace preparation. Defer only
// relative CA paths until Start knows the actual native working directory.
func codeBuddyCAEnvironment(env map[string]string, workingDirectory string) map[string]string {
	resolved := mergeCodeBuddyEnvironment(env, nil)
	for _, name := range []string{"SSL_CERT_FILE", "NODE_EXTRA_CA_CERTS"} {
		if path := resolved[name]; path != "" && !filepath.IsAbs(path) {
			if workingDirectory == "" {
				delete(resolved, name)
			} else {
				resolved[name] = filepath.Join(workingDirectory, path)
			}
		}
	}
	return resolved
}

// Proxy configuration is frozen from this Run's effective environment. Neither
// ProxyFromEnvironment's process cache nor global environment mutation is used.
func codeBuddyProxyConfig(env map[string]string) (*httpproxy.Config, error) {
	values := make([]string, 3)
	for i, name := range []string{"HTTP_PROXY", "HTTPS_PROXY", "NO_PROXY"} {
		upper, lower := env[name], env[strings.ToLower(name)]
		if upper != "" && lower != "" && upper != lower {
			return nil, fmt.Errorf("CodeBuddy conflicting %s aliases", name)
		}
		value := lower
		if value == "" {
			value = upper
		}
		if strings.IndexFunc(value, unicode.IsControl) >= 0 {
			return nil, fmt.Errorf("CodeBuddy invalid %s configuration", name)
		}
		if i < 2 && value != "" {
			// httpproxy accepts host[:port], but silently ignores malformed URLs.
			// Validate first so a bad proxy never becomes an unintended direct call.
			if !strings.Contains(value, "://") {
				value = "http://" + value
			}
			u, err := url.Parse(value)
			if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" ||
				u.Opaque != "" || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") ||
				strings.TrimSpace(value) != value {
				return nil, fmt.Errorf("CodeBuddy invalid %s HTTP/HTTPS proxy", name)
			}
			if port := u.Port(); port != "" {
				if n, err := net.LookupPort("tcp", port); err != nil || n < 1 || n > 65535 {
					return nil, fmt.Errorf("CodeBuddy invalid %s proxy port", name)
				}
			}
		}
		values[i] = value
	}
	return &httpproxy.Config{HTTPProxy: values[0], HTTPSProxy: values[1], NoProxy: values[2]}, nil
}

func codeBuddyModelClient(env map[string]string) (*http.Client, error) {
	proxy, err := codeBuddyProxyConfig(env)
	if err != nil {
		return nil, err
	}
	// SystemCertPool returns a copy of the daemon's cached device trust baseline.
	// Explicit Run PEM files append to that copy, never to process/global roots.
	roots, err := x509.SystemCertPool()
	if err != nil {
		return nil, fmt.Errorf("CodeBuddy system trust could not be loaded")
	}
	for _, name := range []string{"SSL_CERT_FILE", "NODE_EXTRA_CA_CERTS"} {
		if env[name] == "" {
			continue
		}
		if err := appendCodeBuddyCA(roots, env[name]); err != nil {
			return nil, fmt.Errorf("CodeBuddy %s must be a readable regular PEM certificate file of at most 4 MiB", name)
		}
	}
	proxyForURL := proxy.ProxyFunc()
	transport := &http.Transport{
		Proxy:               func(r *http.Request) (*url.URL, error) { return proxyForURL(r.URL) },
		DialContext:         (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		TLSClientConfig:     &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: roots},
		TLSHandshakeTimeout: 10 * time.Second, ResponseHeaderTimeout: 30 * time.Second,
		MaxResponseHeaderBytes: 64 << 10, MaxIdleConns: 2, MaxIdleConnsPerHost: 2,
		IdleConnTimeout: 30 * time.Second, ForceAttemptHTTP2: true,
	}
	return &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}, nil
}

func appendCodeBuddyCA(roots *x509.CertPool, path string) error {
	f, err := openCodeBuddyCAFile(path)
	if err != nil {
		return err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > codeBuddyMaxCABytes {
		return fmt.Errorf("invalid CA file")
	}
	data, err := io.ReadAll(io.LimitReader(f, codeBuddyMaxCABytes+1))
	if err != nil || len(data) == 0 || len(data) > codeBuddyMaxCABytes {
		return fmt.Errorf("invalid CA file")
	}
	count := 0
	for len(bytes.TrimSpace(data)) != 0 {
		block, rest := pem.Decode(data)
		if block == nil || block.Type != "CERTIFICATE" || len(block.Headers) != 0 {
			return fmt.Errorf("invalid CA PEM")
		}
		certs, err := x509.ParseCertificates(block.Bytes)
		if err != nil || len(certs) == 0 {
			return fmt.Errorf("invalid CA certificate")
		}
		for _, cert := range certs {
			roots.AddCert(cert)
			count++
		}
		data = rest
	}
	if count == 0 {
		return fmt.Errorf("empty CA file")
	}
	return nil
}
