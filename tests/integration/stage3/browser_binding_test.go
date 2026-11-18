package stage3

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"openfox/internal/browser"
	"openfox/internal/dispatch"
	"openfox/internal/tenancy"
)

func browserDeviceTx(e *s3Env, org, device string, fn func(context.Context, pgx.Tx) error) error {
	selection := tenancy.OrganizationSelection{OrganizationID: org, ActorID: device, AuthorizationVersion: 1}
	return e.database.RunForOrganization(e.ctx, org, tenancy.SourceDeviceRequest, tenancy.OrganizationSelectorFunc(func(context.Context, string) (tenancy.OrganizationSelection, error) { return selection, nil }), fn)
}

func TestBrowserBindingOwnershipClaimConcurrencyAndRevocation(t *testing.T) {
	e := newS3Env(t)
	agent := e.insertAgent(e.space.ID)
	e.configureAgent(agent)
	device := e.ensureDevice("ORGANIZATION", "")
	e.exec(`UPDATE openfox.physical_devices SET owner_user_id=$2::uuid WHERE id=$1::uuid`, device, s3OwnerID)
	var conversation struct {
		ID string `json:"id"`
	}
	e.owner().must(201, http.MethodPost, "conversations", map[string]any{"space_id": e.space.ID, "agent_id": agent}, &conversation)
	path := "conversations/" + conversation.ID + "/browser-bindings"
	input := browser.CreateInput{DeviceID: device, HostInstanceID: s3NewID(t), HostEpoch: 1}
	var created browser.CreateResult
	e.owner().must(201, http.MethodPost, path, input, &created)
	if len(created.ClaimSecret) != 64 || created.OwnerUserID != s3OwnerID || created.DeviceID != device {
		t.Fatal("binding scope or claim missing")
	}
	var hash string
	e.scalar(&hash, `SELECT claim_hash FROM openfox.browser_bindings WHERE id=$1::uuid`, created.ID)
	if hash == created.ClaimSecret {
		t.Fatal("claim stored in plaintext")
	}
	e.owner().expectError(409, "BROWSER_BINDING_CONFLICT", http.MethodPost, path, input)
	e.client(s3AdminID).expectError(404, "BROWSER_UNAVAILABLE", http.MethodPost, path, input)
	identity := browser.Identity{HostInstanceID: created.HostInstanceID, HostEpoch: created.HostEpoch, Revision: created.Revision}
	claim := func(org, dev, secret string) error {
		return browserDeviceTx(e, org, dev, func(ctx context.Context, tx pgx.Tx) error {
			_, err := browser.Claim(ctx, tx, org, dev, created.ID, secret, identity)
			return err
		})
	}
	if err := claim(e.org, device, strings.Repeat("0", 64)); !errors.Is(err, browser.ErrUnavailable) {
		t.Fatalf("wrong claim: %v", err)
	}
	if err := claim(e.otherOrg, device, created.ClaimSecret); !errors.Is(err, browser.ErrUnavailable) {
		t.Fatalf("cross tenant claim: %v", err)
	}
	if err := claim(e.org, s3NewID(t), created.ClaimSecret); !errors.Is(err, browser.ErrUnavailable) {
		t.Fatalf("other device: %v", err)
	}
	results := make(chan error, 2)
	for range 2 {
		go func() { results <- claim(e.org, device, created.ClaimSecret) }()
	}
	success := 0
	for range 2 {
		err := <-results
		if err == nil {
			success++
		} else if !errors.Is(err, browser.ErrUnavailable) {
			t.Fatalf("claim failed: %v", err)
		}
	}
	if success != 1 {
		t.Fatalf("claim consumed %d times", success)
	}
	heartbeat := func() error {
		return browserDeviceTx(e, e.org, device, func(ctx context.Context, tx pgx.Tx) error {
			_, err := browser.Heartbeat(ctx, tx, e.org, device, created.ID, identity)
			return err
		})
	}
	if err := heartbeat(); err != nil {
		t.Fatal(err)
	}
	identity.HostEpoch++
	if err := heartbeat(); !errors.Is(err, browser.ErrUnavailable) {
		t.Fatalf("old epoch accepted: %v", err)
	}
	identity.HostEpoch--
	input.Replace = true
	var replacement browser.CreateResult
	e.owner().must(201, http.MethodPost, path, input, &replacement)
	if replacement.ID == created.ID || replacement.Revision <= created.Revision {
		t.Fatal("replacement did not invalidate original binding")
	}
	if err := heartbeat(); !errors.Is(err, browser.ErrUnavailable) {
		t.Fatalf("replaced binding accepted: %v", err)
	}
	e.owner().must(204, http.MethodDelete, path+"/"+replacement.ID, nil, nil)
	identity = browser.Identity{HostInstanceID: replacement.HostInstanceID, HostEpoch: replacement.HostEpoch, Revision: replacement.Revision}
	created = replacement
	if err := claim(e.org, device, created.ClaimSecret); !errors.Is(err, browser.ErrUnavailable) {
		t.Fatalf("revoked claim accepted: %v", err)
	}
	e.owner().must(201, http.MethodPost, path, input, &created)
	identity = browser.Identity{HostInstanceID: created.HostInstanceID, HostEpoch: created.HostEpoch, Revision: created.Revision}
	e.exec(`UPDATE openfox.browser_bindings SET lease_expires_at=clock_timestamp()-interval '1 second' WHERE id=$1::uuid`, created.ID)
	if err := claim(e.org, device, created.ClaimSecret); !errors.Is(err, browser.ErrUnavailable) {
		t.Fatalf("expired claim accepted: %v", err)
	}
}

func TestBrowserAuthorizationEnforcesOwnerRunGenerationInputsAndReadOnly(t *testing.T) {
	e := newS3Env(t)
	agent := e.insertAgent(e.space.ID)
	e.configureAgent(agent)
	device := e.ensureDevice("ORGANIZATION", "")
	e.exec(`UPDATE openfox.physical_devices SET owner_user_id=$2::uuid WHERE id=$1::uuid`, device, s3OwnerID)
	run := e.insertRun(e.space.ID, agent, "", "RUNNING")
	e.exec(`UPDATE openfox.runs SET lease_expires_at=clock_timestamp()+interval '5 minutes' WHERE id=$1::uuid`, run)
	var conversation string
	e.scalar(&conversation, `SELECT conversation_id::text FROM openfox.runs WHERE id=$1::uuid`, run)
	var created browser.CreateResult
	e.owner().must(201, http.MethodPost, "conversations/"+conversation+"/browser-bindings", browser.CreateInput{DeviceID: device, HostInstanceID: s3NewID(t), HostEpoch: 1}, &created)
	identity := browser.Identity{HostInstanceID: created.HostInstanceID, HostEpoch: created.HostEpoch, Revision: created.Revision}
	err := browserDeviceTx(e, e.org, device, func(ctx context.Context, tx pgx.Tx) error {
		_, err := browser.Claim(ctx, tx, e.org, device, created.ID, created.ClaimSecret, identity)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	input := dispatch.BrowserAuthorizationInput{Identity: identity, BindingID: created.ID, Generation: 1, Operation: "observe"}
	authorize := func() error {
		return browserDeviceTx(e, e.org, device, func(ctx context.Context, tx pgx.Tx) error {
			_, err := (dispatch.Store{}).AuthorizeBrowser(ctx, tx, e.org, device, run, input)
			return err
		})
	}
	if err := authorize(); !errors.Is(err, dispatch.ErrAuthorizationRevoked) {
		t.Fatalf("run without user input accepted: %v", err)
	}
	clientID := s3NewID(t)
	var message struct {
		ID string `json:"id"`
	}
	e.owner().must(201, http.MethodPost, "conversations/"+conversation+"/messages", map[string]any{"body": "Inspect the browser", "client_message_id": clientID}, &message)
	e.exec(`INSERT INTO openfox.conversation_inputs(organization_id,conversation_id,message_id,body,source_event_id,run_id)
 VALUES($1::uuid,$2::uuid,$3::uuid,'Inspect the browser',$4,$5::uuid)`, e.org, conversation, message.ID, "browser:"+clientID, run)
	input.ClientMessageID = clientID
	if err := authorize(); err != nil {
		t.Fatal(err)
	}
	var runtimeContext dispatch.RunContext
	if err := browserDeviceTx(e, e.org, device, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		runtimeContext, err = (dispatch.Store{}).Context(ctx, tx, e.org, device, run, 1)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if len(runtimeContext.BrowserClientMessageIDs) != 1 || runtimeContext.BrowserClientMessageIDs[0] != clientID {
		t.Fatal("authenticated input identity missing from Run context")
	}
	input.Generation++
	if err := authorize(); !errors.Is(err, dispatch.ErrGenerationConflict) {
		t.Fatalf("wrong generation: %v", err)
	}
	input.Generation--
	input.ClientMessageID = s3NewID(t)
	if err := authorize(); !errors.Is(err, dispatch.ErrAuthorizationRevoked) {
		t.Fatalf("other message: %v", err)
	}
	input.ClientMessageID = clientID
	e.exec(`UPDATE openfox.runs SET runtime='codex',model='default',approval_mode='READ_ONLY' WHERE id=$1::uuid`, run)
	input.Operation = "act"
	if err := authorize(); !errors.Is(err, dispatch.ErrAuthorizationRevoked) {
		t.Fatalf("read-only mutation accepted: %v", err)
	}
	input.Operation = "observe"
	if err := authorize(); err != nil {
		t.Fatal(err)
	}
	e.exec(`UPDATE openfox.messages SET authenticated_user_input=false WHERE id=$1::uuid`, message.ID)
	if err := authorize(); !errors.Is(err, dispatch.ErrAuthorizationRevoked) {
		t.Fatalf("synthetic message accepted: %v", err)
	}
}
