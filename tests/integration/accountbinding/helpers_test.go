package accountbinding

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	neturl "net/url"
	"strings"
	"testing"
)

const testPassword = "account binding password"

type httpClient struct {
	t      *testing.T
	base   string
	client *http.Client
	csrf   string
}

func httpLogin(t *testing.T, base, email string) *httpClient {
	t.Helper()
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	c := &httpClient{t: t, base: base, client: &http.Client{Jar: jar}}
	payload := fmt.Sprintf(`{"email":%q,"password":%q,"client_kind":"web"}`, email, testPassword)
	response, err := c.client.Post(base+"/v1/auth/login", "application/json", strings.NewReader(payload))
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusCreated {
		t.Fatalf("login %s = %d", email, response.StatusCode)
	}
	parsed, _ := neturl.Parse(base)
	for _, cookie := range jar.Cookies(parsed) {
		if cookie.Name == "openfox_csrf" {
			c.csrf = cookie.Value
		}
	}
	if c.csrf == "" {
		t.Fatal("login did not set the CSRF cookie")
	}
	return c
}

func (c *httpClient) do(method, path string, body any) (int, string) {
	c.t.Helper()
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			c.t.Fatal(err)
		}
		reader = bytes.NewReader(encoded)
	}
	request, err := http.NewRequest(method, c.base+path, reader)
	if err != nil {
		c.t.Fatal(err)
	}
	if reader != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	if method != http.MethodGet {
		request.Header.Set("X-CSRF-Token", c.csrf)
	}
	response, err := c.client.Do(request)
	if err != nil {
		c.t.Fatal(err)
	}
	defer response.Body.Close()
	payload, _ := io.ReadAll(response.Body)
	return response.StatusCode, string(payload)
}

func (c *httpClient) json(want int, method, path string, body, target any) {
	c.t.Helper()
	status, payload := c.do(method, path, body)
	if status != want {
		c.t.Fatalf("%s %s = %d %s, want %d", method, path, status, payload, want)
	}
	if target != nil {
		if err := json.Unmarshal([]byte(payload), target); err != nil {
			c.t.Fatal(err)
		}
	}
}

func (c *httpClient) expect(want int, code, method, path string, body any) {
	c.t.Helper()
	status, payload := c.do(method, path, body)
	if status != want || (code != "" && !strings.Contains(payload, `"`+code+`"`)) {
		c.t.Fatalf("%s %s = %d %s, want %d %s", method, path, status, payload, want, code)
	}
}
