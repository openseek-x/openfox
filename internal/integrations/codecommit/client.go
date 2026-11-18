package codecommit

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/aws/signer/v4"
)

var regions = strings.Fields("af-south-1 ap-east-1 ap-northeast-1 ap-northeast-2 ap-northeast-3 ap-south-1 ap-south-2 ap-southeast-1 ap-southeast-2 ap-southeast-3 ca-central-1 eu-central-1 eu-north-1 eu-south-1 eu-west-1 eu-west-2 eu-west-3 il-central-1 me-central-1 me-south-1 sa-east-1 us-east-1 us-east-2 us-west-1 us-west-2")

func Regions() []string { return append([]string(nil), regions...) }
func SupportedRegion(region string) bool {
	for _, r := range regions {
		if region == r {
			return true
		}
	}
	return false
}

// Transport and Now are server-owned test seams. Neither endpoint nor credential
// discovery is configurable; production callers use New with an allowed region.
type Client struct {
	region    string
	Transport http.RoundTripper
	Now       func() time.Time
}

func New(region string) (*Client, error) {
	if !SupportedRegion(region) {
		return nil, ErrInvalid
	}
	return &Client{region: region}, nil
}
func (c *Client) Region() string {
	if c == nil {
		return ""
	}
	return c.region
}

var transport = &http.Transport{Proxy: http.ProxyFromEnvironment, DialContext: (&net.Dialer{Timeout: 3 * time.Second, KeepAlive: 30 * time.Second}).DialContext, TLSHandshakeTimeout: 3 * time.Second, ResponseHeaderTimeout: 8 * time.Second, MaxResponseHeaderBytes: 64 << 10, MaxIdleConns: 10, MaxIdleConnsPerHost: 5, IdleConnTimeout: 30 * time.Second, DisableCompression: true}

type budgetKey struct{}
type readBudget struct{ used atomic.Int64 }

// WithReadBudget shares the 20-second/8-MiB budget across sequential reads.
// Nested calls preserve it; callers cannot reset a traversal by calling helpers.
func WithReadBudget(ctx context.Context) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithTimeout(ctx, JobTimeout)
	if ctx.Value(budgetKey{}) == nil {
		ctx = context.WithValue(ctx, budgetKey{}, &readBudget{})
	}
	return ctx, cancel
}
func (c *Client) call(ctx context.Context, creds Credentials, service, action string, body []byte, write bool) ([]byte, error) {
	if c == nil || !SupportedRegion(c.region) || creds.Validate() != nil {
		return nil, ErrInvalid
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	contentType := "application/x-amz-json-1.1"
	if service == "sts" {
		contentType = "application/x-www-form-urlencoded"
	} else if service != "codecommit" {
		return nil, ErrInvalid
	}
	endpoint := "https://" + service + "." + c.region + ".amazonaws.com/"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, ErrInvalid
	}
	req.Header.Set("Content-Type", contentType)
	if service == "codecommit" {
		req.Header.Set("X-Amz-Target", "CodeCommit_20150413."+action)
	}
	now := time.Now().UTC()
	if c.Now != nil {
		now = c.Now().UTC()
	}
	hash := sha256.Sum256(body)
	err = v4.NewSigner().SignHTTP(ctx, aws.Credentials{AccessKeyID: creds.AccessKeyID, SecretAccessKey: creds.SecretAccessKey, SessionToken: creds.SessionToken}, req, hex.EncodeToString(hash[:]), service, c.region, now)
	if err != nil {
		return nil, ErrInvalid
	}
	tr := c.Transport
	if tr == nil {
		tr = transport
	}
	// Never follow Location; a signed request must remain on its fixed origin.
	hc := &http.Client{Transport: tr, Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	res, err := hc.Do(req)
	if err != nil {
		if write {
			return nil, ErrUnknownResult
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, ErrUnavailable
	}
	defer res.Body.Close()
	limit := int64(MaxReadBytes)
	budget, _ := ctx.Value(budgetKey{}).(*readBudget)
	if budget != nil {
		limit -= budget.used.Load()
	}
	if limit < 0 {
		limit = 0
	}
	if res.StatusCode != 200 && limit > 32768 {
		limit = 32768
	}
	raw, err := io.ReadAll(io.LimitReader(res.Body, limit+1))
	if budget != nil {
		budget.used.Add(int64(len(raw)))
	}
	if err != nil || int64(len(raw)) > limit {
		if write {
			return nil, ErrUnknownResult
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, ErrIncomplete
	}
	if res.StatusCode != 200 {
		return nil, responseError(res, raw, write)
	}
	if len(raw) == 0 {
		if write {
			return nil, ErrUnknownResult
		}
		return nil, ErrIncomplete
	}
	return raw, nil
}
func (c *Client) json(ctx context.Context, creds Credentials, action string, in, out any, write bool) error {
	raw, err := json.Marshal(in)
	if err != nil {
		return ErrInvalid
	}
	raw, err = c.call(ctx, creds, "codecommit", action, raw, write)
	if err != nil {
		return err
	}
	if len(bytes.TrimSpace(raw)) == 0 || bytes.TrimSpace(raw)[0] != '{' || json.Unmarshal(raw, out) != nil {
		if write {
			return ErrUnknownResult
		}
		return ErrIncomplete
	}
	return nil
}
func responseError(res *http.Response, raw []byte, write bool) error {
	var in struct {
		Type string `json:"__type"`
		Code string `json:"code"`
	}
	_ = json.Unmarshal(raw, &in)
	code := in.Type
	if code == "" {
		code = in.Code
	}
	if code == "" {
		var x struct {
			Code string `xml:"Error>Code"`
		}
		_ = xml.Unmarshal(raw, &x)
		code = x.Code
	}
	if i := strings.LastIndex(code, "#"); i >= 0 {
		code = code[i+1:]
	}
	if i := strings.Index(code, ":"); i >= 0 {
		code = code[:i]
	}
	kind, safe := error(nil), ""
	switch code {
	case "AccessDenied", "AccessDeniedException", "UnauthorizedOperation":
		kind, safe = ErrForbidden, "permission_denied"
	case "InvalidClientTokenId", "UnrecognizedClientException", "ExpiredToken", "ExpiredTokenException", "InvalidSignatureException", "SignatureDoesNotMatch", "RequestExpired", "IncompleteSignature", "MissingAuthenticationToken":
		kind, safe = ErrCredentials, "credentials_invalid"
	case "EncryptionKeyAccessDeniedException", "EncryptionKeyDisabledException", "EncryptionKeyNotFoundException", "EncryptionKeyUnavailableException", "EncryptionIntegrityChecksFailedException", "EncryptionKeyUnavailable", "KMSAccessDeniedException":
		kind, safe = ErrKMS, "kms_unavailable"
	case "RepositoryDoesNotExistException", "PullRequestDoesNotExistException", "CommitDoesNotExistException", "BranchDoesNotExistException", "CommentDoesNotExistException":
		kind, safe = ErrNotFound, "not_found"
	case "IdempotencyParameterMismatchException":
		kind, safe = ErrIdempotencyConflict, "idempotency_conflict"
	case "Throttling", "ThrottlingException", "TooManyRequestsException", "RequestLimitExceeded":
		kind, safe = ErrRateLimited, "rate_limited"
	case "BeforeCommitIdAndAfterCommitIdAreSameException", "ClientRequestTokenRequiredException", "CommentContentRequiredException", "CommentContentSizeLimitExceededException", "InvalidCommitIdException", "InvalidPullRequestIdException", "InvalidRepositoryNameException", "RepositoryNameRequiredException", "InvalidContinuationTokenException", "InvalidMaxResultsException", "InvalidPullRequestStatusException", "PullRequestIdRequiredException", "CommitIdRequiredException", "InvalidClientRequestTokenException":
		kind, safe = ErrInvalid, "invalid_request"
	}
	if kind == nil {
		switch {
		case res.StatusCode == 429:
			kind, safe = ErrRateLimited, "rate_limited"
		case res.StatusCode == 401:
			kind, safe = ErrCredentials, "credentials_invalid"
		case res.StatusCode == 403:
			kind, safe = ErrForbidden, "permission_denied"
		case write:
			return ErrUnknownResult
		default:
			kind, safe = ErrUnavailable, "request_failed"
		}
	}
	// An HTTP 5xx or abnormal 2xx after a write never proves absence of a side effect.
	if write && (res.StatusCode >= 500 || res.StatusCode >= 200 && res.StatusCode < 300) {
		return ErrUnknownResult
	}
	var delay time.Duration
	if seconds, e := strconv.ParseInt(res.Header.Get("Retry-After"), 10, 64); e == nil && seconds > 0 {
		if seconds > 300 {
			seconds = 300
		}
		delay = time.Duration(seconds) * time.Second
	}
	return &Error{Kind: kind, Code: safe, RetryAfter: delay}
}
func (c *Client) Identity(ctx context.Context, creds Credentials) (Identity, error) {
	raw, err := c.call(ctx, creds, "sts", "GetCallerIdentity", []byte(url.Values{"Action": {"GetCallerIdentity"}, "Version": {"2011-06-15"}}.Encode()), false)
	if err != nil {
		return Identity{}, err
	}
	var out struct {
		XMLName xml.Name `xml:"GetCallerIdentityResponse"`
		Result  *struct {
			Account string `xml:"Account"`
			ARN     string `xml:"Arn"`
			UserID  string `xml:"UserId"`
		} `xml:"GetCallerIdentityResult"`
	}
	if xml.Unmarshal(raw, &out) != nil || out.Result == nil {
		return Identity{}, ErrIncomplete
	}
	r := out.Result
	if !ValidAccount(r.Account) || !validPrincipal(r.ARN, r.Account) || !opaque(r.UserID, 1024) {
		return Identity{}, ErrIdentityMismatch
	}
	return Identity{r.Account, r.ARN, r.UserID}, nil
}
func validPrincipal(arn, account string) bool {
	p := strings.SplitN(arn, ":", 6)
	return len(p) == 6 && p[0] == "arn" && p[1] == "aws" && (p[2] == "iam" || p[2] == "sts") && p[3] == "" && ValidAccount(p[4]) && (account == "" || p[4] == account) && opaque(p[5], 1024)
}
