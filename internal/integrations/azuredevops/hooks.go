package azuredevops

import (
	"context"
	"encoding/json"
	"net/url"
	"slices"
	"strings"
)

type HookInputValidation struct {
	DataType  string `json:"dataType"`
	Required  bool   `json:"isRequired"`
	MinLength int    `json:"minLength"`
	MaxLength int    `json:"maxLength"`
}
type HookInputDescriptor struct {
	ID           string              `json:"id"`
	Confidential bool                `json:"isConfidential"`
	Validation   HookInputValidation `json:"validation"`
}
type HookEvent struct {
	ID               string                `json:"id"`
	PublisherID      string                `json:"publisherId"`
	ResourceVersions []string              `json:"supportedResourceVersions"`
	Inputs           []HookInputDescriptor `json:"inputDescriptors"`
}
type HookPublisher struct {
	ID     string                `json:"id"`
	Inputs []HookInputDescriptor `json:"inputDescriptors"`
	Events []HookEvent           `json:"supportedEvents"`
}
type HookAction struct {
	ID                           string                     `json:"id"`
	ConsumerID                   string                     `json:"consumerId"`
	EventTypes                   []string                   `json:"supportedEventTypes"`
	ResourceVersions             map[string]json.RawMessage `json:"supportedResourceVersions"`
	AllowResourceVersionOverride bool                       `json:"allowResourceVersionOverride"`
	Inputs                       []HookInputDescriptor      `json:"inputDescriptors"`
}
type HookConsumer struct {
	ID      string                `json:"id"`
	Inputs  []HookInputDescriptor `json:"inputDescriptors"`
	Actions []HookAction          `json:"actions"`
}
type HookMetadata struct {
	Publishers []HookPublisher
	Consumers  []HookConsumer
}

// Basic credentials are deliberately excluded from JSON and String formatting.
// They are added only to the private request body, and are never returned.
type HookCreateInput struct {
	ProjectID       string
	RepositoryID    string
	EventType       string
	ResourceVersion string
	CallbackURL     string
	BasicUsername   string `json:"-"`
	BasicPassword   string `json:"-"`
}

func (HookCreateInput) String() string   { return "azure_devops.HookCreateInput(redacted)" }
func (HookCreateInput) GoString() string { return "azure_devops.HookCreateInput(redacted)" }

type HookSubscription struct {
	ID               string `json:"id"`
	PublisherID      string `json:"publisherId"`
	EventType        string `json:"eventType"`
	ResourceVersion  string `json:"resourceVersion"`
	ConsumerID       string `json:"consumerId"`
	ConsumerActionID string `json:"consumerActionId"`
	Status           string `json:"status"`
	ProjectID        string `json:"projectId"`
	RepositoryID     string `json:"repositoryId,omitempty"`
	CallbackURL      string `json:"callbackUrl"`
}
type hookSubscriptionWire struct {
	ID               string            `json:"id"`
	PublisherID      string            `json:"publisherId"`
	EventType        string            `json:"eventType"`
	ResourceVersion  string            `json:"resourceVersion"`
	ConsumerID       string            `json:"consumerId"`
	ConsumerActionID string            `json:"consumerActionId"`
	Status           string            `json:"status"`
	PublisherInputs  map[string]string `json:"publisherInputs"`
	ConsumerInputs   map[string]string `json:"consumerInputs"`
}

func validHookInputs(inputs []HookInputDescriptor) bool {
	if len(inputs) > 200 {
		return false
	}
	seen := map[string]bool{}
	for _, v := range inputs {
		if !ValidText(v.ID, 256) || seen[v.ID] || len(v.Validation.DataType) > 100 || v.Validation.MinLength < 0 || v.Validation.MaxLength < 0 {
			return false
		}
		seen[v.ID] = true
	}
	return true
}
func (c *Client) HookPublishers(ctx context.Context, token, org string) ([]HookPublisher, error) {
	if !ValidOrganization(org) {
		return nil, invalid()
	}
	var out struct {
		Value []HookPublisher `json:"value"`
	}
	h, err := c.request(ctx, "GET", apiURL("dev.azure.com", orgPath(org)+"/_apis/hooks/publishers", nil), token, nil, nil, false, &out)
	if err != nil {
		return nil, err
	}
	if out.Value == nil || len(out.Value) > 200 || h.Get("x-ms-continuationtoken") != "" {
		return nil, protocol()
	}
	seen := map[string]bool{}
	for _, p := range out.Value {
		if !ValidText(p.ID, 256) || seen[p.ID] || !validHookInputs(p.Inputs) || len(p.Events) > MaxItems {
			return nil, protocol()
		}
		seen[p.ID] = true
		events := map[string]bool{}
		for _, e := range p.Events {
			if !ValidText(e.ID, 256) || e.PublisherID != p.ID || events[e.ID] || !validHookInputs(e.Inputs) || len(e.ResourceVersions) > 100 {
				return nil, protocol()
			}
			events[e.ID] = true
			versions := map[string]bool{}
			for _, v := range e.ResourceVersions {
				if !ValidText(v, 100) || versions[v] {
					return nil, protocol()
				}
				versions[v] = true
			}
		}
	}
	return out.Value, nil
}
func (c *Client) HookConsumers(ctx context.Context, token, org string) ([]HookConsumer, error) {
	if !ValidOrganization(org) {
		return nil, invalid()
	}
	var out struct {
		Value []HookConsumer `json:"value"`
	}
	h, err := c.request(ctx, "GET", apiURL("dev.azure.com", orgPath(org)+"/_apis/hooks/consumers", nil), token, nil, nil, false, &out)
	if err != nil {
		return nil, err
	}
	if out.Value == nil || len(out.Value) > 200 || h.Get("x-ms-continuationtoken") != "" {
		return nil, protocol()
	}
	seen := map[string]bool{}
	for _, p := range out.Value {
		if !ValidText(p.ID, 256) || seen[p.ID] || !validHookInputs(p.Inputs) || len(p.Actions) > 200 {
			return nil, protocol()
		}
		seen[p.ID] = true
		actions := map[string]bool{}
		for _, a := range p.Actions {
			if !ValidText(a.ID, 256) || a.ConsumerID != p.ID || actions[a.ID] || !validHookInputs(a.Inputs) || len(a.EventTypes) > MaxItems || len(a.ResourceVersions) > MaxItems {
				return nil, protocol()
			}
			actions[a.ID] = true
			for _, e := range a.EventTypes {
				if !ValidText(e, 256) {
					return nil, protocol()
				}
			}
			for _, v := range a.ResourceVersions {
				if !checkFieldValue(v) {
					return nil, protocol()
				}
			}
		}
	}
	return out.Value, nil
}
func safeCallback(raw string) bool {
	if !ValidText(raw, 4096) {
		return false
	}
	u, e := url.Parse(raw)
	return e == nil && u.Scheme == "https" && u.Hostname() != "" && u.User == nil && u.RawQuery == "" && u.Fragment == ""
}
func hookInputSet(inputs []HookInputDescriptor) (map[string]HookInputDescriptor, error) {
	if !validHookInputs(inputs) {
		return nil, invalid()
	}
	out := map[string]HookInputDescriptor{}
	for _, v := range inputs {
		if _, ok := out[v.ID]; ok {
			return nil, invalid()
		}
		out[v.ID] = v
	}
	return out, nil
}

// ValidateHookCreate only permits this application's narrow webhook contract.
// The caller supplies metadata freshly read from this same organization.
func ValidateHookCreate(in HookCreateInput, metadata HookMetadata) error {
	if !ValidUUID(in.ProjectID) || !ValidText(in.ResourceVersion, 100) || !safeCallback(in.CallbackURL) || !ValidText(in.BasicUsername, 256) || !ValidText(in.BasicPassword, 256) || strings.ContainsAny(in.BasicUsername, " :\t\r\n") || strings.ContainsAny(in.BasicPassword, " \t\r\n") {
		return invalid()
	}
	repoEvent := false
	switch in.EventType {
	case "git.push", "git.pullrequest.created", "git.pullrequest.updated":
		repoEvent = true
	case "workitem.created", "workitem.updated", "workitem.deleted", "workitem.restored":
	default:
		return invalid()
	}
	if repoEvent && !ValidUUID(in.RepositoryID) || !repoEvent && in.RepositoryID != "" {
		return invalid()
	}
	var p *HookPublisher
	var event *HookEvent
	var consumer *HookConsumer
	var action *HookAction
	for i := range metadata.Publishers {
		v := &metadata.Publishers[i]
		if v.ID == "tfs" {
			if p != nil {
				return invalid()
			}
			p = v
		}
	}
	if p == nil {
		return invalid()
	}
	for i := range p.Events {
		v := &p.Events[i]
		if v.ID == in.EventType {
			if event != nil {
				return invalid()
			}
			event = v
		}
	}
	if event == nil || event.PublisherID != "tfs" || !slices.Contains(event.ResourceVersions, in.ResourceVersion) {
		return invalid()
	}
	publishers, err := hookInputSet(append(append([]HookInputDescriptor{}, p.Inputs...), event.Inputs...))
	if err != nil {
		return err
	}
	if _, ok := publishers["projectId"]; !ok {
		return invalid()
	}
	if repoEvent {
		if _, ok := publishers["repository"]; !ok {
			return invalid()
		}
	}
	for _, v := range publishers {
		if v.Validation.Required && v.ID != "projectId" && (!repoEvent || v.ID != "repository") {
			return invalid()
		}
	}
	for i := range metadata.Consumers {
		v := &metadata.Consumers[i]
		if v.ID == "webHooks" {
			if consumer != nil {
				return invalid()
			}
			consumer = v
		}
	}
	if consumer == nil {
		return invalid()
	}
	for i := range consumer.Actions {
		v := &consumer.Actions[i]
		if v.ID == "httpRequest" {
			if action != nil {
				return invalid()
			}
			action = v
		}
	}
	if action == nil || action.ConsumerID != "webHooks" || !action.AllowResourceVersionOverride || !slices.Contains(action.EventTypes, "*") && !slices.Contains(action.EventTypes, in.EventType) {
		return invalid()
	}
	consumers, err := hookInputSet(append(append([]HookInputDescriptor{}, consumer.Inputs...), action.Inputs...))
	if err != nil {
		return err
	}
	for _, name := range []string{"url", "basicAuthUsername", "basicAuthPassword"} {
		if _, ok := consumers[name]; !ok {
			return invalid()
		}
	}
	if !consumers["basicAuthPassword"].Confidential {
		return invalid()
	}
	values := map[string]string{"url": in.CallbackURL, "basicAuthUsername": in.BasicUsername, "basicAuthPassword": in.BasicPassword}
	for _, d := range consumers {
		v, ok := values[d.ID]
		if !ok && d.Validation.Required {
			return invalid()
		}
		if ok && (len(v) < d.Validation.MinLength || d.Validation.MaxLength > 0 && len(v) > d.Validation.MaxLength) {
			return invalid()
		}
	}
	return nil
}
func subscriptionView(w hookSubscriptionWire) (HookSubscription, error) {
	if !ValidUUID(w.ID) || !ValidText(w.PublisherID, 256) || !ValidText(w.EventType, 256) || !ValidText(w.ResourceVersion, 100) || !ValidText(w.ConsumerID, 256) || !ValidText(w.ConsumerActionID, 256) || !ValidText(w.Status, 100) || len(w.PublisherInputs) > 200 || len(w.ConsumerInputs) > 200 {
		return HookSubscription{}, protocol()
	}
	for k, v := range w.PublisherInputs {
		if !ValidText(k, 256) || !boundedText(v) {
			return HookSubscription{}, protocol()
		}
	}
	for k, v := range w.ConsumerInputs {
		if !ValidText(k, 256) || !boundedText(v) {
			return HookSubscription{}, protocol()
		}
	}
	// List may include unrelated subscriptions. Do not interpret or require their
	// consumer-specific inputs; retain only nonsecret matching fields.
	return HookSubscription{ID: uuid(w.ID), PublisherID: w.PublisherID, EventType: w.EventType, ResourceVersion: w.ResourceVersion, ConsumerID: w.ConsumerID, ConsumerActionID: w.ConsumerActionID, Status: w.Status, ProjectID: uuid(w.PublisherInputs["projectId"]), RepositoryID: uuid(w.PublisherInputs["repository"]), CallbackURL: w.ConsumerInputs["url"]}, nil
}
func (c *Client) HookSubscriptions(ctx context.Context, token, org string) ([]HookSubscription, error) {
	if !ValidOrganization(org) {
		return nil, invalid()
	}
	var out struct {
		Count *int                   `json:"count"`
		Value []hookSubscriptionWire `json:"value"`
	}
	h, err := c.request(ctx, "GET", apiURL("dev.azure.com", orgPath(org)+"/_apis/hooks/subscriptions", nil), token, nil, nil, false, &out)
	if err != nil {
		return nil, err
	}
	if out.Value == nil || len(out.Value) > MaxItems || out.Count != nil && *out.Count != len(out.Value) || h.Get("x-ms-continuationtoken") != "" {
		return nil, protocol()
	}
	result := []HookSubscription{}
	seen := map[string]bool{}
	for _, w := range out.Value {
		v, e := subscriptionView(w)
		if e != nil {
			return nil, e
		}
		if seen[v.ID] {
			return nil, protocol()
		}
		seen[v.ID] = true
		result = append(result, v)
	}
	return result, nil
}
func (c *Client) CreateHook(ctx context.Context, token, org string, in HookCreateInput, metadata HookMetadata) (HookSubscription, error) {
	if !ValidOrganization(org) {
		return HookSubscription{}, invalid()
	}
	if err := ValidateHookCreate(in, metadata); err != nil {
		return HookSubscription{}, err
	}
	pub := map[string]string{"projectId": uuid(in.ProjectID)}
	if in.RepositoryID != "" {
		pub["repository"] = uuid(in.RepositoryID)
	}
	body := map[string]any{"publisherId": "tfs", "eventType": in.EventType, "resourceVersion": in.ResourceVersion, "consumerId": "webHooks", "consumerActionId": "httpRequest", "publisherInputs": pub, "consumerInputs": map[string]string{"url": in.CallbackURL, "basicAuthUsername": in.BasicUsername, "basicAuthPassword": in.BasicPassword}}
	var out hookSubscriptionWire
	_, err := c.request(ctx, "POST", apiURL("dev.azure.com", orgPath(org)+"/_apis/hooks/subscriptions", nil), token, body, nil, true, &out)
	if err != nil {
		return HookSubscription{}, err
	}
	v, e := subscriptionView(out)
	if e != nil || v.PublisherID != "tfs" || v.ConsumerID != "webHooks" || v.ConsumerActionID != "httpRequest" || v.ProjectID != uuid(in.ProjectID) || v.RepositoryID != uuid(in.RepositoryID) || v.CallbackURL != in.CallbackURL || v.EventType != in.EventType || v.ResourceVersion != in.ResourceVersion {
		return HookSubscription{}, &Error{Code: "invalid_response", Unknown: true}
	}
	return v, nil
}
func (c *Client) DeleteHook(ctx context.Context, token, org, id string) error {
	if c == nil || !ValidOrganization(org) || !ValidUUID(id) {
		return invalid()
	}
	// The documented success statuses are 200 and 204. An unexpected 202 cannot
	// be called completed deletion; leave the durable caller to reconcile it.
	client := *c
	status := 0
	client.Observe = func(meta ResponseMeta) {
		status = meta.HTTPStatus
		if c.Observe != nil {
			c.Observe(meta)
		}
	}
	_, err := client.request(ctx, "DELETE", apiURL("dev.azure.com", orgPath(org)+"/_apis/hooks/subscriptions/"+uuid(id), nil), token, nil, nil, true, nil)
	if err == nil && status != 200 && status != 204 {
		return &Error{Code: "invalid_response", HTTPStatus: status, Unknown: true}
	}
	return err
}
