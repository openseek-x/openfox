package integrations

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	"openfox/internal/integrations/azuredevops"
)

func azureHookTestMetadata() azuredevops.HookMetadata {
	p := azuredevops.HookPublisher{ID: "tfs", Inputs: []azuredevops.HookInputDescriptor{{ID: "projectId", Validation: azuredevops.HookInputValidation{Required: true}}}}
	for _, event := range append(append([]string{}, azureGitEvents...), azureBoardsEvents...) {
		e := azuredevops.HookEvent{ID: event, PublisherID: "tfs", ResourceVersions: []string{azureEventVersion(event)}}
		if strings.HasPrefix(event, "git.") {
			e.Inputs = []azuredevops.HookInputDescriptor{{ID: "repository"}}
		}
		p.Events = append(p.Events, e)
	}
	return azuredevops.HookMetadata{Publishers: []azuredevops.HookPublisher{p}, Consumers: []azuredevops.HookConsumer{{ID: "webHooks", Actions: []azuredevops.HookAction{{ID: "httpRequest", ConsumerID: "webHooks", EventTypes: []string{"*"}, AllowResourceVersionOverride: true, Inputs: []azuredevops.HookInputDescriptor{{ID: "url"}, {ID: "basicAuthUsername"}, {ID: "basicAuthPassword", Confidential: true}}}}}}}
}
func azureEventTestBody(event string) map[string]any {
	resource := map[string]any{"id": int64(7), "pushId": int64(8), "pullRequestId": int64(9), "repository": map[string]any{"id": azureTestIdentityApp, "project": map[string]any{"id": azureTestApp}}, "url": "https://untrusted.invalid/ignored", "fields": map[string]any{"System.AssignedTo": "untrusted"}}
	return map[string]any{"id": azureTestSubject, "eventType": event, "publisherId": "tfs", "resourceVersion": azureEventVersion(event), "resourceContainers": map[string]any{"account": map[string]any{"id": azureTestAccount}, "project": map[string]any{"id": azureTestApp}}, "resource": resource}
}
func TestAzureHookOfficialEventVersionsAndEnvelope(t *testing.T) {
	for _, event := range append(append([]string{}, azureGitEvents...), azureBoardsEvents...) {
		t.Run(event, func(t *testing.T) {
			body, _ := json.Marshal(azureEventTestBody(event))
			out, e := parseAzureEvent(body)
			if e != nil || out.EventType != event || out.ProjectID != azureTestApp || out.AccountID != azureTestAccount {
				t.Fatalf("event %v %#v", e, out)
			}
			want := int64(7)
			if event == "git.push" {
				want = 8
			} else if strings.HasPrefix(event, "git.") {
				want = 9
			}
			if out.ObjectID != want {
				t.Fatal("wrong object ID")
			}
			s := Service{PublicURL: "https://openfox.example"}
			repo := ""
			if strings.HasPrefix(event, "git.") {
				repo = azureTestIdentityApp
			}
			if e = s.validateAzureHook(azureHookTestMetadata(), azureTestApp, repo, event); e != nil {
				t.Fatalf("documented event metadata rejected: %v", e)
			}
		})
	}
	for _, tc := range []struct {
		name   string
		mutate func(map[string]any)
	}{
		{"unknown_version", func(v map[string]any) { v["resourceVersion"] = "99.0" }},
		{"different_project", func(v map[string]any) {
			v["resource"].(map[string]any)["repository"].(map[string]any)["project"] = map[string]any{"id": azureTestSubject}
		}},
		{"non_uuid_account", func(v map[string]any) {
			v["resourceContainers"].(map[string]any)["account"] = map[string]any{"id": "same-name"}
		}},
		{"negative_id", func(v map[string]any) { v["resource"].(map[string]any)["pushId"] = -1 }},
		{"string_id", func(v map[string]any) { v["resource"].(map[string]any)["pushId"] = "8" }},
		{"fraction_id", func(v map[string]any) { v["resource"].(map[string]any)["pushId"] = 1.5 }},
		{"overflow_id", func(v map[string]any) { v["resource"].(map[string]any)["pushId"] = int64(1) << 40 }},
		{"unknown_publisher", func(v map[string]any) { v["publisherId"] = "other" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			v := azureEventTestBody("git.push")
			tc.mutate(v)
			body, _ := json.Marshal(v)
			if _, e := parseAzureEvent(body); e == nil {
				t.Fatal("invalid event accepted")
			}
		})
	}
	if _, e := parseAzureEvent([]byte(strings.Repeat(" ", AzureWebhookBodyLimit+1))); !errors.Is(e, ErrInvalidRequest) {
		t.Fatal("event body bound")
	}
}
func TestAzureHookOwnerMatchingAndSecretAuthentication(t *testing.T) {
	h := azureHookRecord{AzureHookView: AzureHookView{ID: azureTestSubject, ProjectID: azureTestApp, RepositoryID: azureTestIdentityApp, EventType: "git.push", Generation: 4}, RemoteGeneration: 3, ResourceVersion: "2.0"}
	s := Service{PublicURL: "https://openfox.example"}
	callback, _ := s.azureHookURL(h.ID, h.RemoteGeneration)
	original := azuredevops.HookSubscription{ID: azureTestTenant, PublisherID: "tfs", ConsumerID: "webHooks", ConsumerActionID: "httpRequest", ProjectID: h.ProjectID, RepositoryID: h.RepositoryID, EventType: h.EventType, ResourceVersion: h.ResourceVersion, CallbackURL: callback}
	if !azureHookOwned(original, h, callback) {
		t.Fatal("exact owned hook rejected")
	}
	for _, change := range []func(*azuredevops.HookSubscription){func(v *azuredevops.HookSubscription) { v.CallbackURL += "/other" }, func(v *azuredevops.HookSubscription) { v.CallbackURL = strings.ReplaceAll(callback, "/3", "/4") }, func(v *azuredevops.HookSubscription) { v.ProjectID = azureTestAccount }, func(v *azuredevops.HookSubscription) { v.RepositoryID = azureTestAccount }, func(v *azuredevops.HookSubscription) { v.EventType = "git.pullrequest.created" }, func(v *azuredevops.HookSubscription) { v.ResourceVersion = "1.0" }, func(v *azuredevops.HookSubscription) { v.ConsumerID = "other" }} {
		v := original
		change(&v)
		if azureHookOwned(v, h, callback) {
			t.Fatal("unowned subscription matched")
		}
	}
	matches, e := s.matchAzureHookSubscriptions(h, []azuredevops.HookSubscription{original, original})
	if e != nil || len(matches) != 2 {
		t.Fatal("ambiguous ownership must retain both matches")
	}
	if !azureBasicMatches(h.ID, "fixture-secret", azureHookUsername(h.ID), "fixture-secret") || azureBasicMatches(h.ID, "fixture-secret", azureHookUsername(azureTestAccount), "fixture-secret") || azureBasicMatches(h.ID, "fixture-secret", azureHookUsername(h.ID), "wrong") {
		t.Fatal("basic authentication mismatch")
	}
	for _, base := range []string{"http://openfox.example", "https://user:secret@openfox.example", "https://openfox.example?secret=x", "https://openfox.example#fragment"} {
		s.PublicURL = base
		if _, e = s.azureHookURL(h.ID, 3); !errors.Is(e, ErrAzureHookHTTPS) {
			t.Fatalf("unsafe callback %s", base)
		}
	}
}
func TestAzureHookOptionalSubscriptionMustMatch(t *testing.T) {
	wire := azureEventTestBody("git.push")
	wire["subscriptionId"] = azureTestTenant
	raw, _ := json.Marshal(wire)
	event, e := parseAzureEvent(raw)
	if e != nil {
		t.Fatal(e)
	}
	h := azureHookRecord{AzureHookView: AzureHookView{SubscriptionID: azureTestTenant, ProjectID: event.ProjectID, RepositoryID: event.RepositoryID, EventType: event.EventType}, ResourceVersion: event.ResourceVersion}
	if !azureEventMatchesHook(event, h, event.AccountID) {
		t.Fatal("exact subscription rejected")
	}
	event.SubscriptionID = azureTestAccount
	if azureEventMatchesHook(event, h, event.AccountID) {
		t.Fatal("other subscription accepted")
	}
	event.SubscriptionID = ""
	if !azureEventMatchesHook(event, h, event.AccountID) {
		t.Fatal("documented missing subscription rejected")
	}
	wire["subscriptionId"] = "not-a-uuid"
	raw, _ = json.Marshal(wire)
	if _, e = parseAzureEvent(raw); e == nil {
		t.Fatal("invalid subscription ID accepted")
	}
}
func TestAzureHookUnknownNeverBecomesBlindCreate(t *testing.T) {
	for _, tc := range []struct {
		name, previous string
		submitted      bool
		sub            string
		cause          error
		state, repair  string
	}{
		{"timeout", "CREATING", true, "", context.DeadlineExceeded, "CREATE_UNKNOWN", "CREATE_UNKNOWN"},
		{"late_fence", "CREATING", true, "", ErrVersionConflict, "CREATE_UNKNOWN", "CREATE_UNKNOWN"},
		{"server_unknown", "CREATING", true, "", &azuredevops.Error{HTTPStatus: 500, Unknown: true}, "CREATE_UNKNOWN", "CREATE_UNKNOWN"},
		{"reconcile_permission_loss", "CREATE_UNKNOWN", false, "", &azuredevops.Error{HTTPStatus: 403}, "CREATE_UNKNOWN", "CREATE_UNKNOWN"},
		{"rejected_create", "CREATING", true, "", &azuredevops.Error{HTTPStatus: 403}, "NEEDS_ATTENTION", "CREATE_PENDING"},
		{"metadata_denied_before_post", "CREATING", false, "", &azuredevops.Error{HTTPStatus: 403}, "NEEDS_ATTENTION", "CREATE_PENDING"},
		{"success_then_fence", "CREATING", true, azureTestTenant, ErrVersionConflict, "NEEDS_ATTENTION", "CREATE_UNKNOWN"},
		{"delete_unknown", "DELETE_UNKNOWN", false, "", &azuredevops.Error{HTTPStatus: 403}, "DELETE_UNKNOWN", "CREATE_UNKNOWN"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			state, code := azureHookFailureState(tc.previous, tc.submitted, tc.sub, tc.cause)
			if state != tc.state {
				t.Fatalf("state=%s code=%s", state, code)
			}
			h := azureHookRecord{AzureHookView: AzureHookView{State: state, LastError: code, SubscriptionID: tc.sub}}
			if got := azureHookRepairState(h); got != tc.repair {
				t.Fatalf("unsafe repair %s (%s)", got, code)
			}
		})
	}
	for _, code := range []string{"connection_changed", "permission_denied", "create_result_unknown", "duplicate_owned_hooks", "remote_hook_disabled"} {
		h := azureHookRecord{AzureHookView: AzureHookView{State: "NEEDS_ATTENTION", LastError: code}}
		if azureHookRepairState(h) != "CREATE_UNKNOWN" {
			t.Fatalf("lost uncertainty after %s", code)
		}
	}
}
func TestAzureHookTLSMetadataCreateAndReconcile(t *testing.T) {
	metadata := azureHookTestMetadata()
	var posts, lists atomic.Int32
	h := azureHookRecord{AzureHookView: AzureHookView{ID: azureTestSubject, ProjectID: azureTestApp, RepositoryID: azureTestIdentityApp, EventType: "git.push"}, RemoteGeneration: 1, ResourceVersion: "2.0"}
	var callback string
	wire := func() map[string]any {
		return map[string]any{"id": azureTestTenant, "publisherId": "tfs", "consumerId": "webHooks", "consumerActionId": "httpRequest", "eventType": "git.push", "resourceVersion": "2.0", "status": "enabled", "publisherInputs": map[string]string{"projectId": h.ProjectID, "repository": h.RepositoryID}, "consumerInputs": map[string]string{"url": callback, "basicAuthUsername": azureHookUsername(h.ID), "basicAuthPassword": "fixture-hook-secret"}}
	}
	s := azureOAuthFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Fixture-Origin") != "https://dev.azure.com" {
			t.Error("wrong host")
		}
		switch r.URL.Path {
		case "/fixture-org/_apis/hooks/publishers":
			azureOAuthReply(w, map[string]any{"value": metadata.Publishers})
		case "/fixture-org/_apis/hooks/consumers":
			azureOAuthReply(w, map[string]any{"value": metadata.Consumers})
		case "/fixture-org/_apis/hooks/subscriptions":
			if r.Method == "POST" {
				posts.Add(1)
				var body map[string]any
				if json.NewDecoder(r.Body).Decode(&body) != nil {
					t.Error("bad body")
				}
				inputs := body["consumerInputs"].(map[string]any)
				if inputs["url"] != callback || inputs["basicAuthPassword"] != "fixture-hook-secret" || body["resourceVersion"] != "2.0" {
					t.Error("wrong hook configuration")
				}
				azureOAuthReply(w, wire())
			} else {
				lists.Add(1)
				azureOAuthReply(w, map[string]any{"count": 1, "value": []any{wire()}})
			}
		default:
			t.Errorf("unexpected %s", r.URL.Path)
			w.WriteHeader(404)
		}
	})
	s.PublicURL = "https://openfox.example"
	callback, _ = s.azureHookURL(h.ID, h.RemoteGeneration)
	m, e := fetchAzureHookMetadata(context.Background(), s.Azure, "fixture-token", "fixture-org")
	if e != nil {
		t.Fatal(e)
	}
	if e = s.validateAzureHook(m, h.ProjectID, h.RepositoryID, h.EventType); e != nil {
		t.Fatal(e)
	}
	created, e := s.Azure.CreateHook(context.Background(), "fixture-token", "fixture-org", azuredevops.HookCreateInput{ProjectID: h.ProjectID, RepositoryID: h.RepositoryID, EventType: h.EventType, ResourceVersion: h.ResourceVersion, CallbackURL: callback, BasicUsername: azureHookUsername(h.ID), BasicPassword: "fixture-hook-secret"}, m)
	if e != nil {
		t.Fatal(e)
	}
	subs, e := s.Azure.HookSubscriptions(context.Background(), "fixture-token", "fixture-org")
	if e != nil {
		t.Fatal(e)
	}
	matches, e := s.matchAzureHookSubscriptions(h, subs)
	if e != nil || len(matches) != 1 || matches[0].ID != created.ID || posts.Load() != 1 || lists.Load() != 1 {
		t.Fatalf("reconcile %v %d", e, len(matches))
	}
	raw, _ := json.Marshal(matches)
	if strings.Contains(string(raw), "fixture-hook-secret") || strings.Contains(string(raw), azureHookUsername(h.ID)) {
		t.Fatal("hook secrets in output")
	}
}
