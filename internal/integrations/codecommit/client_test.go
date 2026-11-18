package codecommit

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"sort"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

var fixtureCredentials = Credentials{AccessKeyID: "AKIDEXAMPLE0000001", SecretAccessKey: "synthetic-secret-only", SessionToken: "synthetic-session+/=token"}

const fixtureAccount = "123456789012"
const fixtureARN = "arn:aws:iam::123456789012:user/fixture"

var fixtureTime = time.Date(2026, 11, 15, 3, 4, 5, 0, time.UTC)

func hashHex(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }
func mac(key []byte, data string) []byte {
	h := hmac.New(sha256.New, key)
	h.Write([]byte(data))
	return h.Sum(nil)
}

// Independent verification: does not invoke the AWS signing implementation.
func verifySignature(r *http.Request, body []byte, region string) error {
	service := strings.Split(r.Host, ".")[0]
	if (service != "sts" && service != "codecommit") || r.Host != service+"."+region+".amazonaws.com" || r.URL.Path != "/" || r.URL.RawQuery != "" || r.Method != "POST" {
		return errors.New("unexpected fixed endpoint")
	}
	auth := strings.TrimPrefix(r.Header.Get("Authorization"), "AWS4-HMAC-SHA256 ")
	fields := map[string]string{}
	for _, v := range strings.Split(auth, ", ") {
		p := strings.SplitN(v, "=", 2)
		if len(p) != 2 {
			return errors.New("invalid auth shape")
		}
		fields[p[0]] = p[1]
	}
	scope := "20261115/" + region + "/" + service + "/aws4_request"
	if fields["Credential"] != fixtureCredentials.AccessKeyID+"/"+scope || r.Header.Get("X-Amz-Date") != "20261115T030405Z" || r.Header.Get("X-Amz-Security-Token") != fixtureCredentials.SessionToken {
		return errors.New("credential scope/date/session mismatch")
	}
	names := strings.Split(fields["SignedHeaders"], ";")
	if !sort.StringsAreSorted(names) {
		return errors.New("unsorted signed headers")
	}
	canonical := ""
	signed := map[string]bool{}
	for _, name := range names {
		signed[name] = true
		value := r.Header.Get(name)
		if name == "host" {
			value = r.Host
		}
		canonical += name + ":" + strings.Join(strings.Fields(value), " ") + "\n"
	}
	for _, name := range []string{"content-type", "host", "x-amz-date", "x-amz-security-token"} {
		if !signed[name] {
			return fmt.Errorf("unsigned required header %s", name)
		}
	}
	if service == "codecommit" && !signed["x-amz-target"] {
		return errors.New("target not signed")
	}
	request := "POST\n/\n\n" + canonical + "\n" + fields["SignedHeaders"] + "\n" + hashHex(body)
	signInput := "AWS4-HMAC-SHA256\n20261115T030405Z\n" + scope + "\n" + hashHex([]byte(request))
	key := mac([]byte("AWS4"+fixtureCredentials.SecretAccessKey), "20261115")
	key = mac(key, region)
	key = mac(key, service)
	key = mac(key, "aws4_request")
	if fields["Signature"] != hex.EncodeToString(mac(key, signInput)) {
		return errors.New("signature mismatch")
	}
	return nil
}
func tlsClient(t *testing.T, region string, fn func(http.ResponseWriter, *http.Request, []byte)) (*Client, *atomic.Int64) {
	t.Helper()
	calls := &atomic.Int64{}
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(io.LimitReader(r.Body, 128<<10))
		if err != nil {
			t.Error(err)
			return
		}
		calls.Add(1)
		if err := verifySignature(r, body, region); err != nil {
			t.Error(err)
			http.Error(w, "bad signature", 403)
			return
		}
		fn(w, r, body)
	}))
	t.Cleanup(server.Close)
	roots := x509.NewCertPool()
	roots.AddCert(server.Certificate())
	tr := &http.Transport{TLSClientConfig: &tls.Config{RootCAs: roots, ServerName: "example.com", MinVersion: tls.VersionTLS12}, DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
		if address != "codecommit."+region+".amazonaws.com:443" && address != "sts."+region+".amazonaws.com:443" {
			return nil, errors.New("fixture denied non-fixed host")
		}
		return (&net.Dialer{Timeout: time.Second}).DialContext(ctx, network, server.Listener.Addr().String())
	}}
	t.Cleanup(tr.CloseIdleConnections)
	c, err := New(region)
	if err != nil {
		t.Fatal(err)
	}
	c.Transport = tr
	c.Now = func() time.Time { return fixtureTime }
	return c, calls
}
func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/x-amz-json-1.1")
	_ = json.NewEncoder(w).Encode(v)
}
func identityXML(account, arn string) string {
	return `<GetCallerIdentityResponse xmlns="https://sts.amazonaws.com/doc/2011-06-15/"><GetCallerIdentityResult><Arn>` + arn + `</Arn><UserId>synthetic-user</UserId><Account>` + account + `</Account></GetCallerIdentityResult></GetCallerIdentityResponse>`
}
func repoFixture(region string) Repository {
	return Repository{ID: "opaque:not-a-uuid", Name: "Repo", AccountID: fixtureAccount, ARN: "arn:aws:codecommit:" + region + ":" + fixtureAccount + ":Repo", DefaultBranch: "main"}
}
func sha(n int) string { return fmt.Sprintf("%040x", n) }
func commit(n int, parents ...int) Commit {
	p := []string{}
	for _, id := range parents {
		p = append(p, sha(id))
	}
	return Commit{ID: sha(n), TreeID: sha(999999), Message: fmt.Sprintf("commit %d", n), Parents: p, Author: Signature{Name: "fixture", Email: "fixture@example.test", Date: "1700000000 +0800"}, Committer: Signature{Name: "fixture", Date: "1700000000 +0000"}}
}

func TestFixedRegionsAndIndependentSigV4(t *testing.T) {
	if len(Regions()) != 25 {
		t.Fatal("region count")
	}
	r := Regions()
	r[0] = "changed"
	if Regions()[0] == "changed" {
		t.Fatal("mutable region list")
	}
	for _, region := range Regions() {
		t.Run(region, func(t *testing.T) {
			c, calls := tlsClient(t, region, func(w http.ResponseWriter, r *http.Request, body []byte) {
				if strings.HasPrefix(r.Host, "sts.") {
					v, e := url.ParseQuery(string(body))
					if e != nil || v.Get("Action") != "GetCallerIdentity" || v.Get("Version") != "2011-06-15" || len(v) != 2 {
						t.Error("STS request shape")
					}
					io.WriteString(w, identityXML(fixtureAccount, fixtureARN))
					return
				}
				if r.Header.Get("X-Amz-Target") != "CodeCommit_20150413.GetRepository" || string(body) != `{"repositoryName":"Repo"}` {
					t.Error("CodeCommit request shape")
				}
				writeJSON(w, map[string]any{"repositoryMetadata": repoFixture(region)})
			})
			id, e := c.Identity(context.Background(), fixtureCredentials)
			if e != nil || id.ARN != fixtureARN {
				t.Fatalf("identity: %v", e)
			}
			repo, e := c.GetRepository(context.Background(), fixtureCredentials, fixtureAccount, "Repo")
			if e != nil || repo.ID != "opaque:not-a-uuid" {
				t.Fatalf("repository: %v", e)
			}
			if calls.Load() != 2 {
				t.Fatal("request count")
			}
		})
	}
	for _, r := range []string{"", "cn-north-1", "us-gov-west-1", "us-east-1.evil.test", "US-EAST-1", "ap-southeast-5"} {
		if _, e := New(r); !errors.Is(e, ErrInvalid) {
			t.Fatalf("accepted region %q", r)
		}
	}
}
func TestExplicitCredentialsAndRedaction(t *testing.T) {
	t.Setenv("AWS_ACCESS_KEY_ID", fixtureCredentials.AccessKeyID)
	t.Setenv("AWS_SECRET_ACCESS_KEY", fixtureCredentials.SecretAccessKey)
	t.Setenv("AWS_SESSION_TOKEN", fixtureCredentials.SessionToken)
	c, calls := tlsClient(t, "us-east-1", func(w http.ResponseWriter, r *http.Request, b []byte) { t.Error("unexpected request") })
	if _, e := c.Identity(context.Background(), Credentials{}); !errors.Is(e, ErrInvalid) || calls.Load() != 0 {
		t.Fatal("credential fallback")
	}
	for _, s := range []string{fmt.Sprint(fixtureCredentials), fmt.Sprintf("%+v", fixtureCredentials), fmt.Sprintf("%#v", fixtureCredentials)} {
		if strings.Contains(s, fixtureCredentials.SecretAccessKey) || strings.Contains(s, fixtureCredentials.AccessKeyID) {
			t.Fatal("credentials leaked")
		}
	}
	b, _ := json.Marshal(fixtureCredentials)
	if string(b) != "{}" {
		t.Fatal("credential JSON leaked")
	}
}
func TestIdentityAndRepositoryRejectMismatch(t *testing.T) {
	for _, mode := range []string{"identity-account", "identity-partition", "identity-empty", "repo-account", "repo-region", "repo-case", "repo-null"} {
		t.Run(mode, func(t *testing.T) {
			c, _ := tlsClient(t, "us-east-1", func(w http.ResponseWriter, r *http.Request, b []byte) {
				if strings.HasPrefix(mode, "identity") {
					a := fixtureARN
					if mode == "identity-account" {
						a = strings.Replace(a, fixtureAccount, "999999999999", 1)
					}
					if mode == "identity-partition" {
						a = strings.Replace(a, "arn:aws:", "arn:aws-cn:", 1)
					}
					if mode == "identity-empty" {
						io.WriteString(w, `<GetCallerIdentityResponse/>`)
					} else {
						io.WriteString(w, identityXML(fixtureAccount, a))
					}
					return
				}
				repo := repoFixture("us-east-1")
				switch mode {
				case "repo-account":
					repo.AccountID = "999999999999"
				case "repo-region":
					repo.ARN = strings.Replace(repo.ARN, "us-east-1", "us-west-2", 1)
				case "repo-case":
					repo.Name = "repo"
				case "repo-null":
					writeJSON(w, map[string]any{"repositoryMetadata": nil})
					return
				}
				writeJSON(w, map[string]any{"repositoryMetadata": repo})
			})
			var e error
			if strings.HasPrefix(mode, "identity") {
				_, e = c.Identity(context.Background(), fixtureCredentials)
			} else {
				_, e = c.GetRepository(context.Background(), fixtureCredentials, fixtureAccount, "Repo")
			}
			if e == nil {
				t.Fatal("accepted mismatch")
			}
		})
	}
}
func TestPaginationAndMalformedLists(t *testing.T) {
	for _, mode := range []string{"normal", "null", "missing", "loop", "duplicate", "wrong-shape"} {
		t.Run(mode, func(t *testing.T) {
			c, _ := tlsClient(t, "us-east-1", func(w http.ResponseWriter, r *http.Request, b []byte) {
				var in map[string]any
				json.Unmarshal(b, &in)
				if _, ok := in["maxResults"]; ok {
					t.Error("ListRepositories has no maxResults")
				}
				var out any = map[string]any{"repositories": []RepositoryRef{{ID: "id:1", Name: "Repo"}}, "nextToken": "opaque+/="}
				switch mode {
				case "null":
					out = map[string]any{"repositories": nil}
				case "missing":
					out = map[string]any{}
				case "wrong-shape":
					out = []any{}
				case "duplicate":
					out = map[string]any{"repositories": []RepositoryRef{{ID: "same", Name: "Repo"}, {ID: "same", Name: "Other"}}}
				}
				if mode == "normal" && in["nextToken"] == "opaque+/=" {
					out = map[string]any{"repositories": []RepositoryRef{}}
				}
				writeJSON(w, out)
			})
			next := ""
			if mode == "loop" {
				next = "opaque+/="
			}
			p, e := c.ListRepositories(context.Background(), fixtureCredentials, next)
			if mode != "normal" {
				if !errors.Is(e, ErrIncomplete) {
					t.Fatalf("got %v", e)
				}
				return
			}
			if e != nil || len(p.Repositories) != 1 {
				t.Fatal(e)
			}
			p, e = c.ListRepositories(context.Background(), fixtureCredentials, p.NextToken)
			if e != nil || p.NextToken != "" || len(p.Repositories) != 0 {
				t.Fatal(e)
			}
		})
	}
	for _, action := range []string{"ListBranches", "ListPullRequests"} {
		t.Run(action, func(t *testing.T) {
			c, _ := tlsClient(t, "us-east-1", func(w http.ResponseWriter, r *http.Request, b []byte) {
				writeJSON(w, map[string]any{"branches": nil, "pullRequestIds": nil})
			})
			var e error
			if action == "ListBranches" {
				_, e = c.ListBranches(context.Background(), fixtureCredentials, "Repo", "")
			} else {
				_, e = c.ListPullRequests(context.Background(), fixtureCredentials, "Repo", "OPEN", "")
			}
			if !errors.Is(e, ErrIncomplete) {
				t.Fatal(e)
			}
		})
	}
}
func TestErrorClassificationNoRawSecretsOrRetries(t *testing.T) {
	for _, tt := range []struct {
		name   string
		status int
		typ    string
		want   error
	}{{"auth", 400, "ExpiredTokenException", ErrCredentials}, {"scope", 400, "AccessDeniedException", ErrForbidden}, {"kms", 400, "EncryptionKeyAccessDeniedException", ErrKMS}, {"rate", 429, "ThrottlingException", ErrRateLimited}, {"missing", 400, "CommitDoesNotExistException", ErrNotFound}, {"server", 503, "secret-token-value", ErrUnavailable}} {
		t.Run(tt.name, func(t *testing.T) {
			c, n := tlsClient(t, "us-east-1", func(w http.ResponseWriter, r *http.Request, b []byte) {
				w.Header().Set("Retry-After", "999999")
				w.WriteHeader(tt.status)
				writeJSON(w, map[string]any{"__type": tt.typ, "message": "secret-token-value"})
			})
			_, e := c.GetCommit(context.Background(), fixtureCredentials, "Repo", sha(1))
			if !errors.Is(e, tt.want) || strings.Contains(e.Error(), "secret-token-value") || n.Load() != 1 {
				t.Fatalf("error=%v calls=%d", e, n.Load())
			}
			var detail *Error
			if !errors.As(e, &detail) || detail.RetryAfter != 300*time.Second {
				t.Fatal("retry hint")
			}
		})
	}
}
func TestRedirectSizeDeadlineAndSharedBudget(t *testing.T) {
	t.Run("redirect", func(t *testing.T) {
		c, n := tlsClient(t, "us-east-1", func(w http.ResponseWriter, r *http.Request, b []byte) {
			w.Header().Set("Location", "https://evil.example/steal")
			w.WriteHeader(302)
			io.WriteString(w, `{}`)
		})
		_, e := c.ListRepositories(context.Background(), fixtureCredentials, "")
		if e == nil || n.Load() != 1 {
			t.Fatal("followed redirect")
		}
	})
	t.Run("size", func(t *testing.T) {
		c, _ := tlsClient(t, "us-east-1", func(w http.ResponseWriter, r *http.Request, b []byte) {
			io.WriteString(w, strings.Repeat(" ", MaxReadBytes+1))
		})
		_, e := c.ListRepositories(context.Background(), fixtureCredentials, "")
		if !errors.Is(e, ErrIncomplete) {
			t.Fatal(e)
		}
	})
	t.Run("shared", func(t *testing.T) {
		c, n := tlsClient(t, "us-east-1", func(w http.ResponseWriter, r *http.Request, b []byte) {
			io.WriteString(w, `{"repositories":[]}`+strings.Repeat(" ", 5<<20))
		})
		ctx, cancel := WithReadBudget(context.Background())
		defer cancel()
		deadline, _ := ctx.Deadline()
		if left := time.Until(deadline); left > 20*time.Second || left < 19*time.Second {
			t.Fatalf("deadline %v", left)
		}
		if _, e := c.ListRepositories(ctx, fixtureCredentials, ""); e != nil {
			t.Fatal(e)
		}
		if _, e := c.ListRepositories(ctx, fixtureCredentials, ""); !errors.Is(e, ErrIncomplete) || n.Load() != 2 {
			t.Fatalf("aggregate %v", e)
		}
	})
	t.Run("slow-body", func(t *testing.T) {
		c, _ := tlsClient(t, "us-east-1", func(w http.ResponseWriter, r *http.Request, b []byte) {
			w.WriteHeader(200)
			io.WriteString(w, `{"repositories":`)
			w.(http.Flusher).Flush()
			<-r.Context().Done()
		})
		ctx, cancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
		defer cancel()
		start := time.Now()
		_, e := c.ListRepositories(ctx, fixtureCredentials, "")
		if !errors.Is(e, context.DeadlineExceeded) || time.Since(start) > time.Second {
			t.Fatalf("deadline error %v", e)
		}
	})
}
func TestImmutableCommentIntentAndUnknownWrites(t *testing.T) {
	intent, e := NewCommentIntent("Repo", "7", sha(1), sha(2), "fixed-idempotency-token", "local fixture backlink")
	if e != nil {
		t.Fatal(e)
	}
	raw, _ := json.Marshal(intent)
	restored, e := ParseCommentIntent(raw)
	if e != nil || restored.Fingerprint() != intent.Fingerprint() || restored.Token() != intent.Token() {
		t.Fatal("restore changed intent")
	}
	if bytes.Contains(raw, []byte("location")) {
		t.Fatal("unexpected location")
	}
	for _, rawBad := range []string{`null`, string(raw) + ` {}`, strings.TrimSuffix(string(raw), "}") + `,"location":{}}`} {
		if _, e := ParseCommentIntent([]byte(rawBad)); !errors.Is(e, ErrInvalid) {
			t.Fatal("accepted changed shape")
		}
	}
	for _, mode := range []string{"success", "201", "500", "malformed", "disconnect", "wrong-before", "conflict"} {
		t.Run(mode, func(t *testing.T) {
			c, n := tlsClient(t, "us-east-1", func(w http.ResponseWriter, r *http.Request, b []byte) {
				if !bytes.Equal(b, raw) {
					t.Error("intent mutated")
				}
				if r.Header.Get("X-Amz-Target") != "CodeCommit_20150413.PostCommentForPullRequest" {
					t.Error("action")
				}
				switch mode {
				case "201":
					w.WriteHeader(201)
					io.WriteString(w, `{}`)
					return
				case "500":
					w.WriteHeader(500)
					writeJSON(w, map[string]any{"__type": "AccessDeniedException"})
					return
				case "malformed":
					io.WriteString(w, `{`)
					return
				case "disconnect":
					conn, _, _ := w.(http.Hijacker).Hijack()
					conn.Close()
					return
				case "conflict":
					w.WriteHeader(400)
					writeJSON(w, map[string]any{"__type": "IdempotencyParameterMismatchException"})
					return
				}
				before := sha(1)
				if mode == "wrong-before" {
					before = sha(3)
				}
				writeJSON(w, map[string]any{"repositoryName": "Repo", "pullRequestId": "7", "beforeCommitId": before, "afterCommitId": sha(2), "comment": map[string]any{"commentId": "comment-opaque", "authorArn": fixtureARN, "clientRequestToken": intent.Token(), "content": "local fixture backlink", "creationDate": 1700000000}})
			})
			r, e := c.PostCommentForPullRequest(context.Background(), fixtureCredentials, restored)
			if n.Load() != 1 {
				t.Fatal("mutation retried")
			}
			if mode == "success" {
				if e != nil || r.ID != "comment-opaque" {
					t.Fatal(e)
				}
			} else if mode == "conflict" {
				if !errors.Is(e, ErrIdempotencyConflict) {
					t.Fatal(e)
				}
			} else if !errors.Is(e, ErrUnknownResult) {
				t.Fatalf("not unknown: %v", e)
			}
		})
	}
}
func TestCreationEvidenceRequiresCompleteUniqueCreationEvent(t *testing.T) {
	for _, mode := range []string{"two-pages", "absent", "duplicate", "loop", "twenty-pages", "wrong-repo", "null"} {
		t.Run(mode, func(t *testing.T) {
			c, n := tlsClient(t, "us-east-1", func(w http.ResponseWriter, r *http.Request, b []byte) {
				var in map[string]any
				json.Unmarshal(b, &in)
				if in["pullRequestEventType"] != "PULL_REQUEST_CREATED" || in["maxResults"] != float64(100) {
					t.Error("filter")
				}
				events := []any{}
				next := ""
				event := map[string]any{"pullRequestId": "7", "pullRequestEventType": "PULL_REQUEST_CREATED", "eventDate": 1700000000, "pullRequestCreatedEventMetadata": map[string]any{"repositoryName": "Repo", "destinationCommitId": sha(1), "sourceCommitId": sha(2)}}
				switch mode {
				case "two-pages":
					if in["nextToken"] == nil {
						next = "page2"
					} else {
						events = append(events, event)
					}
				case "duplicate":
					events = append(events, event, event)
				case "loop":
					next = "same"
				case "twenty-pages":
					next = fmt.Sprint(nValue(in))
				case "wrong-repo":
					event["pullRequestCreatedEventMetadata"].(map[string]any)["repositoryName"] = "Other"
					events = append(events, event)
				case "null":
					writeJSON(w, map[string]any{"pullRequestEvents": nil})
					return
				}
				writeJSON(w, map[string]any{"pullRequestEvents": events, "nextToken": next})
			})
			proof, e := c.CreationEvidence(context.Background(), fixtureCredentials, "Repo", "7")
			if mode == "two-pages" {
				if e != nil || proof.BeforeCommitID != sha(1) || n.Load() != 2 {
					t.Fatalf("proof %v %v", proof, e)
				}
			} else if !errors.Is(e, ErrIncomplete) {
				t.Fatalf("accepted incomplete %v", e)
			}
			if mode == "twenty-pages" && n.Load() != 20 {
				t.Fatalf("page budget %d", n.Load())
			}
		})
	}
}
func nValue(in map[string]any) int {
	n := 0
	if v, ok := in["nextToken"].(string); ok {
		fmt.Sscan(v, &n)
	}
	return n + 1
}
func TestReadPullRequestAndBranches(t *testing.T) {
	c, _ := tlsClient(t, "us-east-1", func(w http.ResponseWriter, r *http.Request, b []byte) {
		switch strings.TrimPrefix(r.Header.Get("X-Amz-Target"), "CodeCommit_20150413.") {
		case "ListPullRequests":
			writeJSON(w, map[string]any{"pullRequestIds": []string{"7"}})
		case "GetPullRequest":
			writeJSON(w, map[string]any{"pullRequest": PullRequest{ID: "7", Title: "PR", Status: "OPEN", AuthorARN: fixtureARN, RevisionID: "revision", CreationDate: 1700000000, LastActivityDate: 1700000001, Targets: []PullRequestTarget{{RepositoryName: "Repo", SourceCommit: sha(2), DestinationCommit: sha(3), SourceReference: "refs/heads/feature", DestinationReference: "refs/heads/main"}}}})
		case "ListBranches":
			writeJSON(w, map[string]any{"branches": []string{"main"}})
		case "GetBranch":
			writeJSON(w, map[string]any{"branch": Branch{Name: "main", CommitID: sha(3)}})
		default:
			t.Error("unknown operation")
		}
	})
	p, e := c.ListPullRequests(context.Background(), fixtureCredentials, "Repo", "OPEN", "")
	if e != nil || !reflect.DeepEqual(p.IDs, []string{"7"}) {
		t.Fatal(e)
	}
	pr, e := c.GetPullRequest(context.Background(), fixtureCredentials, "Repo", "7")
	if e != nil || pr.Targets[0].DestinationCommit != sha(3) {
		t.Fatal(e)
	}
	bs, e := c.ListBranches(context.Background(), fixtureCredentials, "Repo", "")
	if e != nil || len(bs.Branches) != 1 {
		t.Fatal(e)
	}
	br, e := c.GetBranch(context.Background(), fixtureCredentials, "Repo", "main")
	if e != nil || br.CommitID != sha(3) {
		t.Fatal(e)
	}
	for _, id := range []string{"0", "01", "-1", "2147483648", "1.0"} {
		if _, e := PullRequestNumber(id); e == nil {
			t.Fatal("bad PR ID")
		}
	}
	if n, e := PullRequestNumber("2147483647"); e != nil || n != 2147483647 {
		t.Fatal(e)
	}
}

func TestTLSVerificationAndExplicitUnknownRecovery(t *testing.T) {
	for _, mode := range []string{"unknown-ca", "wrong-host"} {
		t.Run(mode, func(t *testing.T) {
			c, n := tlsClient(t, "us-east-1", func(w http.ResponseWriter, r *http.Request, b []byte) { t.Error("TLS failure reached application") })
			tr := c.Transport.(*http.Transport)
			if mode == "unknown-ca" {
				tr.TLSClientConfig.RootCAs = x509.NewCertPool()
			} else {
				tr.TLSClientConfig.ServerName = "wrong.example"
			}
			_, e := c.ListRepositories(context.Background(), fixtureCredentials, "")
			if !errors.Is(e, ErrUnavailable) || n.Load() != 0 {
				t.Fatalf("TLS accepted %v", e)
			}
		})
	}
	t.Run("same-immutable-intent-only", func(t *testing.T) {
		intent, _ := NewCommentIntent("Repo", "7", sha(1), sha(2), "fixed-token", "test body")
		raw, _ := intent.MarshalJSON()
		var posted [][]byte
		c, n := tlsClient(t, "us-east-1", func(w http.ResponseWriter, r *http.Request, b []byte) {
			posted = append(posted, append([]byte(nil), b...))
			if len(posted) == 1 {
				conn, _, _ := w.(http.Hijacker).Hijack()
				conn.Close()
				return
			}
			writeJSON(w, map[string]any{"repositoryName": "Repo", "pullRequestId": "7", "beforeCommitId": sha(1), "afterCommitId": sha(2), "comment": map[string]any{"commentId": "same-comment", "authorArn": fixtureARN, "clientRequestToken": "fixed-token", "content": "test body", "creationDate": 1700000000}})
		})
		if _, e := c.PostCommentForPullRequest(context.Background(), fixtureCredentials, intent); !errors.Is(e, ErrUnknownResult) || n.Load() != 1 {
			t.Fatal(e)
		}
		restored, _ := ParseCommentIntent(raw)
		if r, e := c.PostCommentForPullRequest(context.Background(), fixtureCredentials, restored); e != nil || r.ID != "same-comment" {
			t.Fatal(e)
		}
		if n.Load() != 2 || !bytes.Equal(posted[0], posted[1]) {
			t.Fatal("recovery changed intent or auto-retried")
		}
	})
}

func TestCommentIDOpaqueBoundary(t *testing.T) {
	for _, size := range []int{73, 1024, 1025} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			intent, _ := NewCommentIntent("Repo", "7", sha(1), sha(2), "fixed-token", "test body")
			c, n := tlsClient(t, "us-east-1", func(w http.ResponseWriter, r *http.Request, b []byte) {
				writeJSON(w, map[string]any{"repositoryName": "Repo", "pullRequestId": "7", "beforeCommitId": sha(1), "afterCommitId": sha(2), "comment": map[string]any{"commentId": strings.Repeat("c", size), "authorArn": fixtureARN, "content": "test body", "creationDate": 1700000000}})
			})
			result, e := c.PostCommentForPullRequest(context.Background(), fixtureCredentials, intent)
			if size <= 1024 {
				if e != nil || len(result.ID) != size {
					t.Fatal(e)
				}
			} else if !errors.Is(e, ErrUnknownResult) || result.ID != "" {
				t.Fatalf("oversized success must remain UNKNOWN: %v", e)
			}
			if n.Load() != 1 {
				t.Fatal("automatic retry")
			}
		})
	}
}
