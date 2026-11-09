package zentao

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strconv"
	"strings"
	"time"

	"openfox/internal/platform/safehttp"
)

// PersonalCommentClient uses ZenTao's authenticated JSON controller. REST v1
// tokens do not authorize action.comment. A fresh, private cookie jar prevents
// one member's session from being used for another member's comment.
type PersonalCommentClient struct {
	credentials Credentials
	http        *http.Client
}

func ConnectPersonal(ctx context.Context, in Credentials, opts safehttp.Options) (*PersonalCommentClient, error) {
	u, e := safehttp.CheckURL(in.Site)
	if e != nil || u.RawQuery != "" || in.Account == "" || in.Password == "" || len(in.Account) > 200 || len(in.Password) > 2048 || len(in.BasicUser) > 200 || len(in.BasicPassword) > 2048 || strings.ContainsAny(in.BasicUser, ":\r\n") || (in.BasicUser == "") != (in.BasicPassword == "") {
		return nil, ErrInput
	}
	in.Site = strings.TrimRight(u.String(), "/")
	client := safehttp.Client(opts)
	client.Timeout = 10 * time.Second
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	client.Jar, _ = cookiejar.New(nil)
	c := &PersonalCommentClient{in, client}
	var result struct {
		Status string
		User   struct{ Account string }
	}
	if e = c.call(ctx, url.Values{"m": {"user"}, "f": {"login"}, "t": {"json"}}, url.Values{"account": {in.Account}, "password": {in.Password}}, &result); e != nil {
		return nil, e
	}
	if result.Status != "success" || result.User.Account != in.Account {
		return nil, ErrAuth
	}
	return c, nil
}
func (c *PersonalCommentClient) call(ctx context.Context, q, form url.Values, out any) error {
	req, e := http.NewRequestWithContext(ctx, http.MethodPost, c.credentials.Site+"/index.php?"+q.Encode(), strings.NewReader(form.Encode()))
	if e != nil {
		return ErrInput
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	if c.credentials.BasicUser != "" {
		req.SetBasicAuth(c.credentials.BasicUser, c.credentials.BasicPassword)
	}
	resp, e := c.http.Do(req)
	if e != nil {
		return ErrUpstream
	}
	defer resp.Body.Close()
	if resp.StatusCode == 401 || resp.StatusCode == 403 {
		return ErrAuth
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return ErrUpstream
	}
	data, e := io.ReadAll(io.LimitReader(resp.Body, (1<<20)+1))
	if e != nil || len(data) > 1<<20 || json.Unmarshal(data, out) != nil {
		return ErrUpstream
	}
	return nil
}
func (c *PersonalCommentClient) CreateComment(ctx context.Context, kind, id, body string) (string, error) {
	types := map[string]string{"stories": "story", "bugs": "bug", "tasks": "task"}
	n, e := strconv.ParseUint(id, 10, 63)
	if types[kind] == "" || e != nil || n == 0 || strconv.FormatUint(n, 10) != id || strings.TrimSpace(body) == "" || len(body) > 64000 {
		return "", ErrInput
	}
	var result struct {
		Status string
		Data   json.Number
	}
	if e = c.call(ctx, url.Values{"m": {"action"}, "f": {"comment"}, "t": {"json"}, "objectType": {types[kind]}, "objectID": {id}}, url.Values{"comment": {body}}, &result); e != nil {
		return "", e
	}
	n2, e := result.Data.Int64()
	if result.Status != "success" || e != nil || n2 <= 0 {
		return "", ErrUpstream
	}
	return result.Data.String(), nil
}
