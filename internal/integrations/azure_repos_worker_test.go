package integrations

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"openfox/internal/integrations/azuredevops"
	"openfox/internal/policy"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func azureRepoFixtureValues() (azureRepoWork, azuredevops.Project, azuredevops.Repository) {
	w := azureRepoWork{AccountID: azureTestAccount, ProjectID: azureTestApp, RepositoryID: azureTestIdentityApp, ObjectID: 17, Kind: "PULL_REQUEST", access: AzureAccess{OrganizationName: "fixture-org", AccountID: azureTestAccount, SubjectID: azureTestSubject}}
	p := azuredevops.Project{ID: w.ProjectID, Name: "Project with space", Visibility: "private", State: "wellFormed"}
	repo := azuredevops.Repository{ID: w.RepositoryID, Name: "Test Repo", Project: p}
	return w, p, repo
}
func TestAzureReposTLSVerifiedObjectsAndCompletePush(t *testing.T) {
	for _, n := range []int{0, 100, 101} {
		t.Run(fmt.Sprint(n), func(t *testing.T) {
			w, p, repo := azureRepoFixtureValues()
			w.Kind = "PUSH"
			var pages atomic.Int32
			s := azureOAuthFixture(t, func(rw http.ResponseWriter, r *http.Request) {
				if r.Header.Get("X-Fixture-Origin") != "https://dev.azure.com" || r.Header.Get("Authorization") != "Bearer fixture" {
					t.Error("untrusted request destination or token")
				}
				switch {
				case strings.Contains(r.URL.Path, "/_apis/projects/"):
					azureOAuthReply(rw, p)
				case strings.HasSuffix(r.URL.Path, "/"+w.RepositoryID):
					azureOAuthReply(rw, repo)
				case strings.HasSuffix(r.URL.Path, "/pushes/17"):
					if r.URL.Query().Get("includeCommits") != "0" {
						t.Error("push must not trust embedded prefix")
					}
					azureOAuthReply(rw, azuredevops.Push{ID: 17, Repository: repo, PushedBy: azuredevops.IdentityRef{ID: azureTestSubject}, Date: time.Now(), RefUpdates: []azuredevops.RefUpdate{}})
				case strings.HasSuffix(r.URL.Path, "/commits"):
					pages.Add(1)
					skip, _ := strconv.Atoi(r.URL.Query().Get("skip"))
					top, _ := strconv.Atoi(r.URL.Query().Get("top"))
					if top > 50 || r.URL.Query().Get("pushId") != "17" {
						t.Error("wrong bounded push query")
					}
					list := []azuredevops.Commit{}
					for i := skip; i < min(skip+top, n); i++ {
						list = append(list, azuredevops.Commit{ID: fmt.Sprintf("%040x", i+1), Comment: "OF-1 complete message"})
					}
					azureOAuthReply(rw, map[string]any{"count": len(list), "value": list})
				default:
					t.Errorf("unexpected request %s", r.URL.Path)
					rw.WriteHeader(500)
				}
			})
			out, e := fetchAzureRepoData(context.Background(), s.Azure, "fixture", w)
			if n <= 100 {
				if e != nil || len(out.Commits) != n {
					t.Fatalf("complete push: %d %v", len(out.Commits), e)
				}
			} else {
				var up *azuredevops.Error
				if !errors.As(e, &up) || up.Code != "source_limit" || len(out.Commits) != 0 {
					t.Fatalf("truncated push escaped: %d %v", len(out.Commits), e)
				}
			}
			if n == 100 && pages.Load() != 3 {
				t.Fatal("must probe 101st commit")
			}
		})
	}
	for _, wrong := range []bool{false, true} {
		t.Run(fmt.Sprintf("PR_cross_scope_%v", wrong), func(t *testing.T) {
			w, p, repo := azureRepoFixtureValues()
			s := azureOAuthFixture(t, func(rw http.ResponseWriter, r *http.Request) {
				switch {
				case strings.Contains(r.URL.Path, "/_apis/projects/"):
					azureOAuthReply(rw, p)
				case strings.HasSuffix(r.URL.Path, "/"+w.RepositoryID):
					azureOAuthReply(rw, repo)
				case strings.HasSuffix(r.URL.Path, "/pullRequests/17"):
					ref := repo
					if wrong {
						ref.Project.ID = azureTestTenant
					}
					azureOAuthReply(rw, azuredevops.PullRequest{ID: 17, Repository: ref, CreatedBy: azuredevops.IdentityRef{ID: azureTestSubject}, Title: "OF-1", Status: "active", SourceRefName: "refs/heads/work", TargetRefName: "refs/heads/main"})
				default:
					rw.WriteHeader(500)
				}
			})
			out, e := fetchAzureRepoData(context.Background(), s.Azure, "fixture", w)
			if wrong {
				if e == nil {
					t.Fatal("cross project PR accepted")
				}
			} else if e != nil || out.Pull.ID != 17 {
				t.Fatal(e)
			}
		})
	}
}
func TestAzureBacklinkTLSReconcileBeforeSideEffect(t *testing.T) {
	for _, mode := range []string{"new", "existing", "unknown_absent", "wrong_author", "deleted", "duplicate", "truncated", "revoked", "rate_before_post", "post_unknown"} {
		t.Run(mode, func(t *testing.T) {
			rw, p, repo := azureRepoFixtureValues()
			marker := azureBacklinkMarker(azureTestTenant)
			w := azureBacklinkWork{ProjectID: rw.ProjectID, RepositoryID: rw.RepositoryID, Number: 17, AuthorID: azureTestSubject, Marker: marker, Body: "[OF-1](https://openfox.example/task)\n\n" + marker, State: "SENDING", access: rw.access}
			if mode == "unknown_absent" || mode == "wrong_author" || mode == "deleted" {
				w.State = "UNKNOWN"
			}
			var posts, preflights atomic.Int32
			s := azureOAuthFixture(t, func(out http.ResponseWriter, r *http.Request) {
				switch {
				case strings.Contains(r.URL.Path, "/_apis/projects/"):
					azureOAuthReply(out, p)
				case strings.HasSuffix(r.URL.Path, "/"+rw.RepositoryID):
					azureOAuthReply(out, repo)
				case strings.HasSuffix(r.URL.Path, "/threads"):
					thread := azuredevops.Thread{ID: 3, Comments: []azuredevops.ThreadComment{{ID: 7, Content: w.Body, Author: azuredevops.IdentityRef{ID: azureTestSubject}}}}
					if r.Method == "POST" {
						posts.Add(1)
						if mode == "post_unknown" {
							out.WriteHeader(503)
							return
						}
						azureOAuthReply(out, thread)
						return
					}
					list := []azuredevops.Thread{}
					if mode == "existing" || mode == "duplicate" || mode == "wrong_author" || mode == "deleted" {
						if mode == "wrong_author" {
							thread.Comments[0].Author.ID = azureTestApp
						}
						if mode == "deleted" {
							thread.Comments[0].Deleted = true
						}
						list = append(list, thread)
					}
					if mode == "duplicate" {
						thread.ID = 4
						list = append(list, thread)
					}
					if mode == "truncated" {
						out.Header().Set("x-ms-continuationtoken", "untrusted-next")
					}
					if mode == "rate_before_post" {
						out.Header().Set("Retry-After", "60")
					}
					azureOAuthReply(out, map[string]any{"count": len(list), "value": list})
				default:
					t.Errorf("unexpected %s", r.URL.Path)
					out.WriteHeader(500)
				}
			})
			c, _ := azureSequence(s.Azure)
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			result, e := performAzureBacklink(ctx, c, "fixture", w, func(context.Context, bool) error {
				preflights.Add(1)
				if mode == "revoked" {
					return ErrForbidden
				}
				return nil
			})
			switch mode {
			case "new":
				if e != nil || posts.Load() != 1 || preflights.Load() != 1 || result.threadID != 3 || result.commentID != 7 {
					t.Fatalf("new result %#v %v calls %d/%d", result, e, posts.Load(), preflights.Load())
				}
			case "existing":
				if e != nil || posts.Load() != 0 || preflights.Load() != 0 || result.threadID != 3 {
					t.Fatalf("existing result %#v %v", result, e)
				}
			case "unknown_absent", "wrong_author", "deleted":
				if !errors.Is(e, ErrAzureBacklinkUnknown) || posts.Load() != 0 {
					t.Fatalf("must not resend unknown: %v posts %d", e, posts.Load())
				}
			case "duplicate":
				if !errors.Is(e, ErrAzureBacklinkAmbiguous) || posts.Load() != 0 {
					t.Fatalf("ambiguous match %v", e)
				}
			case "revoked":
				if !errors.Is(e, ErrForbidden) || posts.Load() != 0 {
					t.Fatalf("late permission gate %v", e)
				}
			case "post_unknown":
				var up *azuredevops.Error
				if !errors.As(e, &up) || !up.Unknown || posts.Load() != 1 {
					t.Fatalf("missing uncertain outcome %v", e)
				}
			default:
				if e == nil || posts.Load() != 0 {
					t.Fatalf("unsafe thread scan/pacing %v posts %d", e, posts.Load())
				}
			}
		})
	}
}
func TestAzureBacklinkFailureClassification(t *testing.T) {
	for _, tc := range []struct {
		name, old   string
		submitted   bool
		cause       error
		state, code string
	}{
		{"unknown_absent", "UNKNOWN", false, ErrAzureBacklinkUnknown, "UNKNOWN", "comment_result_unknown"},
		{"multiple", "SENDING", false, ErrAzureBacklinkAmbiguous, "UNKNOWN", "multiple_matching_comments"},
		{"post_timeout", "SENDING", true, context.DeadlineExceeded, "UNKNOWN", "upstream_failed"},
		{"post_unknown", "SENDING", true, &azuredevops.Error{Code: "upstream_failed", Unknown: true}, "UNKNOWN", "upstream_failed"},
		{"definitive_rate", "SENDING", true, &azuredevops.Error{Code: "rate_limited", HTTPStatus: 429}, "PENDING", "rate_limited"},
		{"before_send_revoked", "SENDING", false, ErrForbidden, "SKIPPED", "permission_denied"},
		{"old_unknown_permission", "UNKNOWN", false, ErrForbidden, "UNKNOWN", "permission_denied"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			state, code := azureBacklinkFailure(tc.old, tc.submitted, tc.cause)
			if state != tc.state || code != tc.code {
				t.Fatalf("%s/%s", state, code)
			}
		})
	}
}
func TestAzureRepoStableKeyAndDisplayURL(t *testing.T) {
	w, p, r := azureRepoFixtureValues()
	if azureRepositoryKey(w) != azureTestAccount+"/"+azureTestApp+"/"+azureTestIdentityApp {
		t.Fatal("names used as repository identity")
	}
	good := "https://dev.azure.com/fixture-org/Project%20with%20space/_git/Test%20Repo/pullrequest/17"
	if out := azureObjectURL(w.access, p, r, "pullrequest", "17", good); out != good {
		t.Fatal(out)
	}
	for _, remote := range []string{"", strings.Replace(good, "dev.azure.com", "evil.invalid", 1), strings.Replace(good, "fixture-org", "another", 1), strings.Replace(good, "/17", "/18", 1), good + "?token=secret", good + "#fragment", strings.Replace(good, "https://", "http://", 1)} {
		out := azureObjectURL(w.access, p, r, "pullrequest", "17", remote)
		if !strings.Contains(out, "/_apis/git/repositories/"+r.ID+"/pullRequests/17?api-version=7.1") {
			t.Fatalf("unverified display URL accepted %q", out)
		}
	}
	for _, tc := range []struct {
		status string
		draft  bool
		state  string
	}{{"active", false, "OPEN"}, {"active", true, "DRAFT"}, {"completed", true, "MERGED"}, {"abandoned", false, "CLOSED"}} {
		if got := azurePullState(azuredevops.PullRequest{Status: tc.status, IsDraft: tc.draft}); got != tc.state {
			t.Fatal(got)
		}
	}
}

func TestAzureRepoSpaceACLUsesDatabaseEnums(t *testing.T) {
	for _, tc := range []struct {
		visibility, role, status string
		allowed                  bool
	}{
		{"SHARED", "", "", true}, {"SHARED", "VIEWER", "ACTIVE", false}, {"SHARED", "MEMBER", "REVOKED", false},
		{"PRIVATE", "", "", false}, {"PRIVATE", "OWNER", "ACTIVE", true}, {"PRIVATE", "EDITOR", "ACTIVE", true},
		{"PRIVATE", "MEMBER", "ACTIVE", true}, {"PRIVATE", "VIEWER", "ACTIVE", false}, {"PRIVATE", "OWNER", "REVOKED", false},
	} {
		t.Run(tc.visibility+"/"+tc.role+"/"+tc.status, func(t *testing.T) {
			var member *policy.SpaceMembership
			if tc.role != "" {
				member = &policy.SpaceMembership{OrganizationID: "o", SpaceID: "s", ActorID: "u", Role: azureGitSpaceRole(tc.role), Membership: policy.MembershipState(strings.ToLower(tc.status))}
			}
			result := policy.Authorize(policy.Request{Action: policy.ActionUpdateObject, Actor: policy.Actor{OrganizationID: "o", ActorID: "u", Role: policy.OrganizationRoleAdmin, Membership: policy.MembershipActive}, Space: &policy.Space{OrganizationID: "o", ID: "s", Visibility: azureGitSpaceVisibility(tc.visibility)}, SpaceMembership: member, Object: &policy.Object{OrganizationID: "o", SpaceID: "s", ID: "t"}})
			if result.Allowed() != tc.allowed {
				t.Fatalf("allowed=%v", result.Allowed())
			}
		})
	}
}
