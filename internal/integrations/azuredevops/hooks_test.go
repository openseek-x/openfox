package azuredevops

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
)

func metadataFixture() HookMetadata {
	return HookMetadata{
		Publishers: []HookPublisher{{ID: "tfs", Inputs: []HookInputDescriptor{{ID: "projectId", Validation: HookInputValidation{DataType: "guid", Required: true}}}, Events: []HookEvent{{ID: "git.push", PublisherID: "tfs", ResourceVersions: []string{"1.0"}, Inputs: []HookInputDescriptor{{ID: "repository"}}}, {ID: "workitem.updated", PublisherID: "tfs", ResourceVersions: []string{"1.0"}}}}},
		Consumers:  []HookConsumer{{ID: "webHooks", Actions: []HookAction{{ID: "httpRequest", ConsumerID: "webHooks", EventTypes: []string{"*"}, AllowResourceVersionOverride: true, Inputs: []HookInputDescriptor{{ID: "url", Validation: HookInputValidation{DataType: "uri", Required: true}}, {ID: "basicAuthUsername", Validation: HookInputValidation{DataType: "string", MaxLength: 256}}, {ID: "basicAuthPassword", Confidential: true, Validation: HookInputValidation{DataType: "string", MaxLength: 256}}}}}}},
	}
}
func hookInputFixture() HookCreateInput {
	return HookCreateInput{ProjectID: projectID, RepositoryID: repoID, EventType: "git.push", ResourceVersion: "1.0", CallbackURL: "https://openfox.example/hooks/owner-generation", BasicUsername: "fixture-username-secret", BasicPassword: "fixture-password-secret"}
}
func hookWireFixture(in HookCreateInput) hookSubscriptionWire {
	return hookSubscriptionWire{ID: accountID, PublisherID: "tfs", EventType: in.EventType, ResourceVersion: in.ResourceVersion, ConsumerID: "webHooks", ConsumerActionID: "httpRequest", Status: "enabled", PublisherInputs: map[string]string{"projectId": in.ProjectID, "repository": in.RepositoryID}, ConsumerInputs: map[string]string{"url": in.CallbackURL, "basicAuthUsername": in.BasicUsername, "basicAuthPassword": in.BasicPassword}}
}
func TestHookMetadataCreateAndSecretFreeReconciliation(t *testing.T) {
	in := hookInputFixture()
	metadata := metadataFixture()
	var posted atomic.Int32
	c := fixture(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Test-Origin") != "https://dev.azure.com" || r.URL.Query().Get("api-version") != "7.1" {
			t.Error("origin/version")
		}
		switch r.URL.Path {
		case "/org/_apis/hooks/publishers":
			jsonReply(w, map[string]any{"value": metadata.Publishers})
		case "/org/_apis/hooks/consumers":
			jsonReply(w, map[string]any{"value": metadata.Consumers})
		case "/org/_apis/hooks/subscriptions":
			if r.Method == "POST" {
				posted.Add(1)
				var body hookSubscriptionWire
				decodeFixture(t, r, &body)
				if body.ConsumerInputs["basicAuthUsername"] != in.BasicUsername || body.ConsumerInputs["basicAuthPassword"] != in.BasicPassword || body.ConsumerInputs["acceptUntrustedCerts"] != "" || body.PublisherInputs["projectId"] != projectID || body.PublisherInputs["repository"] != repoID || body.ResourceVersion != "1.0" {
					t.Error("hook body")
				}
				jsonReply(w, hookWireFixture(in))
			} else {
				jsonReply(w, map[string]any{"value": []hookSubscriptionWire{hookWireFixture(in)}})
			}
		default:
			t.Error("unexpected path")
			w.WriteHeader(404)
		}
	})
	pubs, e := c.HookPublishers(context.Background(), "token", "org")
	if e != nil {
		t.Fatal(e)
	}
	cons, e := c.HookConsumers(context.Background(), "token", "org")
	if e != nil {
		t.Fatal(e)
	}
	created, e := c.CreateHook(context.Background(), "token", "org", in, HookMetadata{Publishers: pubs, Consumers: cons})
	if e != nil || created.ID != accountID {
		t.Fatal(created, e)
	}
	list, e := c.HookSubscriptions(context.Background(), "token", "org")
	if e != nil || len(list) != 1 || list[0].CallbackURL != in.CallbackURL {
		t.Fatal(list, e)
	}
	raw, _ := json.Marshal(list)
	inputJSON, _ := json.Marshal(in)
	text := fmt.Sprintf("%+v %#v %s %s", in, in, raw, inputJSON)
	if strings.Contains(text, in.BasicUsername) || strings.Contains(text, in.BasicPassword) {
		t.Fatal("secret leaked through DTO")
	}
	if posted.Load() != 1 {
		t.Fatal("unexpected mutation")
	}
}
func TestHookRejectsUnverifiedVersionFieldsConsumerAndScope(t *testing.T) {
	for _, mode := range []string{"version", "missing_password", "nonconfidential_password", "missing_project", "missing_repo", "unknown_event", "wrong_consumer", "override", "required_extra", "http_callback", "query_callback", "repo_board", "invalid_uuid"} {
		t.Run(mode, func(t *testing.T) {
			metadata := metadataFixture()
			in := hookInputFixture()
			switch mode {
			case "version":
				in.ResourceVersion = "99"
			case "missing_password":
				metadata.Consumers[0].Actions[0].Inputs = metadata.Consumers[0].Actions[0].Inputs[:2]
			case "nonconfidential_password":
				metadata.Consumers[0].Actions[0].Inputs[2].Confidential = false
			case "missing_project":
				metadata.Publishers[0].Inputs = nil
			case "missing_repo":
				metadata.Publishers[0].Events[0].Inputs = nil
			case "unknown_event":
				in.EventType = "arbitrary.event"
			case "wrong_consumer":
				metadata.Consumers[0].ID = "other"
			case "override":
				metadata.Consumers[0].Actions[0].AllowResourceVersionOverride = false
			case "required_extra":
				metadata.Publishers[0].Inputs = append(metadata.Publishers[0].Inputs, HookInputDescriptor{ID: "newFilter", Validation: HookInputValidation{Required: true}})
			case "http_callback":
				in.CallbackURL = "http://openfox.example/hooks"
			case "query_callback":
				in.CallbackURL = "https://openfox.example/hooks?secret=x"
			case "repo_board":
				in.EventType = "workitem.updated"
			case "invalid_uuid":
				in.ProjectID = "../other"
			}
			var calls atomic.Int32
			c := fixture(t, func(w http.ResponseWriter, r *http.Request) { calls.Add(1); w.WriteHeader(500) })
			_, e := c.CreateHook(context.Background(), "token", "org", in, metadata)
			errorCode(t, e, "invalid_request", false)
			if calls.Load() != 0 {
				t.Fatal("invalid hook made external write")
			}
		})
	}
	in := hookInputFixture()
	in.RepositoryID = ""
	in.EventType = "workitem.updated"
	if e := ValidateHookCreate(in, metadataFixture()); e != nil {
		t.Fatal("valid Boards hook rejected", e)
	}
}
func TestHookUnknownWriteAndUnboundedListNeverMeansNotFound(t *testing.T) {
	for _, mode := range []string{"malformed", "wrong_scope", "server", "oversized"} {
		t.Run(mode, func(t *testing.T) {
			c := fixture(t, func(w http.ResponseWriter, r *http.Request) {
				switch mode {
				case "malformed":
					fmt.Fprint(w, "{")
				case "wrong_scope":
					v := hookWireFixture(hookInputFixture())
					v.PublisherInputs["repository"] = subjectID
					jsonReply(w, v)
				case "server":
					w.WriteHeader(500)
					fmt.Fprint(w, "password must not leak")
				case "oversized":
					fmt.Fprint(w, strings.Repeat("x", MaxResponseBytes+1))
				}
			})
			_, e := c.CreateHook(context.Background(), "token", "org", hookInputFixture(), metadataFixture())
			code := "invalid_response"
			if mode == "server" {
				code = "upstream_error"
			}
			if mode == "oversized" {
				code = "response_limit_or_read"
			}
			errorCode(t, e, code, true)
		})
	}
	for _, mode := range []string{"cursor", "over", "missing", "duplicate", "count_mismatch"} {
		t.Run(mode, func(t *testing.T) {
			c := fixture(t, func(w http.ResponseWriter, r *http.Request) {
				list := []hookSubscriptionWire{}
				switch mode {
				case "count_mismatch":
					jsonReply(w, map[string]any{"count": 1, "value": list})
					return
				case "cursor":
					w.Header().Set("x-ms-continuationtoken", "more")
				case "over":
					list = make([]hookSubscriptionWire, MaxItems+1)
				case "missing":
					jsonReply(w, map[string]any{})
					return
				case "duplicate":
					list = []hookSubscriptionWire{hookWireFixture(hookInputFixture()), hookWireFixture(hookInputFixture())}
				}
				jsonReply(w, map[string]any{"value": list})
			})
			list, e := c.HookSubscriptions(context.Background(), "token", "org")
			if e == nil || list != nil {
				t.Fatal("partial list treated as absent")
			}
		})
	}
}
func TestDeleteHookOnlyDocumentedSuccessAndPreservesUnknown(t *testing.T) {
	for _, status := range []int{200, 204, 202, 404, 500} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			var observed int
			c := fixture(t, func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "DELETE" || r.URL.Path != "/org/_apis/hooks/subscriptions/"+accountID {
					t.Error("delete contract")
				}
				w.WriteHeader(status)
			})
			c.Observe = func(meta ResponseMeta) { observed = meta.HTTPStatus }
			e := c.DeleteHook(context.Background(), "token", "org", accountID)
			if status == 200 || status == 204 {
				if e != nil {
					t.Fatal(e)
				}
			} else if status == 202 {
				errorCode(t, e, "invalid_response", true)
			} else if status == 404 {
				errorCode(t, e, "not_found", false)
			} else {
				errorCode(t, e, "upstream_error", true)
			}
			if observed != status {
				t.Fatal("lost rate observation")
			}
		})
	}
}
