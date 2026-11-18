package stage8d

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"openfox/internal/conversations"
	"openfox/internal/daemon/supervisor"
	"openfox/internal/devices"
	"openfox/internal/tenancy"
	"openfox/internal/workdirpath"
)

func (h *harness) testWorkDirectoryPath(t *testing.T) {
	h.setAgentConfig("codex", "gpt-5.1", "FOLLOW_RUNTIME")
	h.setCapabilities(strings.Replace(fullCapabilities, "{", `{"workspace_path":true,`, 1))
	device2 := "00000000-0000-0000-0000-000000000022"
	h.exec(`UPDATE openfox.physical_devices SET owner_user_id=$1::uuid WHERE id IN ($2::uuid,$3::uuid)`, alice, device1, device2)
	user := tenancy.OrganizationSelection{OrganizationID: orgA, ActorID: alice, AuthorizationVersion: h.authVersion(orgA, alice)}
	device := tenancy.OrganizationSelection{OrganizationID: orgA, ActorID: device1, AuthorizationVersion: 1}
	otherDevice := tenancy.OrganizationSelection{OrganizationID: orgA, ActorID: device2, AuthorizationVersion: 1}
	store := devices.PostgresStore{Pool: h.pool}
	conversationID := h.newConversation(alice)
	getTarget := func(actor, conversation string) (result conversations.WorkDirectoryTarget, err error) {
		selection := tenancy.OrganizationSelection{OrganizationID: orgA, ActorID: actor, AuthorizationVersion: h.authVersion(orgA, actor)}
		err = h.pool.RunForOrganization(h.ctx, orgA, tenancy.SourceUserRequest, selector(selection), func(ctx context.Context, tx pgx.Tx) error {
			result, err = (conversations.Service{}).WorkDirectory(ctx, tx, orgA, conversation, h.actor(orgA, actor))
			return err
		})
		return
	}
	if result, err := getTarget(alice, conversationID); err != nil || result.Target != nil {
		t.Fatalf("unrun conversation: %+v %v", result, err)
	}
	runID := h.sendAndDispatch(alice, conversationID, "isolated workspace fixture; no runtime execution")
	lease := h.claim(runID)
	target, err := getTarget(alice, conversationID)
	if err != nil || target.Target == nil || !target.Supported || !target.Online || target.Target.DeviceID != device1 || target.Target.RunID != runID {
		t.Fatalf("target %+v %v", target, err)
	}
	private, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	der, _ := x509.MarshalPKIXPublicKey(&private.PublicKey)
	request := workdirpath.Request{Scope: *target.Target, RequestID: newID(t), PublicKey: base64.StdEncoding.EncodeToString(der), ExpiresAt: time.Now().Add(30 * time.Second).UnixMilli()}
	encode := func(request workdirpath.Request) json.RawMessage { raw, _ := json.Marshal(request); return raw }
	for _, change := range []func(*workdirpath.Request){
		func(r *workdirpath.Request) { r.OrganizationID = orgB },
		func(r *workdirpath.Request) { r.DeviceID = device2 },
		func(r *workdirpath.Request) { r.ConversationID = newID(t) },
		func(r *workdirpath.Request) { r.UserID = carol },
		func(r *workdirpath.Request) { r.Generation++ },
		func(r *workdirpath.Request) { r.RunID = newID(t) },
		func(r *workdirpath.Request) { r.ExpiresAt = time.Now().Add(-time.Second).UnixMilli() },
	} {
		bad := request
		change(&bad)
		if _, _, err := store.CreateCommand(h.ctx, user, device1, devices.CommandWorkspacePath, encode(bad)); err == nil {
			t.Fatal("invalid scope accepted")
		}
	}
	if _, err := getTarget(carol, conversationID); err == nil {
		t.Fatal("private conversation visible to another member")
	}
	privateConversation := h.newConversation(carol)
	h.sendAndMaterialize(carol, privateConversation, "private scope fixture")
	if _, err := getTarget(alice, privateConversation); err == nil {
		t.Fatal("device owner bypassed private conversation")
	}
	var workers sync.WaitGroup
	commands := make(chan devices.Command, 6)
	created := make(chan bool, 6)
	commandErrors := make(chan error, 6)
	for range 6 {
		workers.Add(1)
		go func() {
			defer workers.Done()
			command, fresh, err := store.CreateCommand(h.ctx, user, device1, devices.CommandWorkspacePath, encode(request))
			commands <- command
			created <- fresh
			commandErrors <- err
		}()
	}
	workers.Wait()
	close(commands)
	close(created)
	close(commandErrors)
	for err := range commandErrors {
		if err != nil {
			t.Fatal(err)
		}
	}
	var command devices.Command
	for result := range commands {
		if command.ID != "" && command.ID != result.ID {
			t.Fatal("duplicate command")
		}
		command = result
	}
	count := 0
	for fresh := range created {
		if fresh {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("created %d", count)
	}
	conflicting := request
	conflicting.RequestID = newID(t)
	if _, _, err := store.CreateCommand(h.ctx, user, device1, devices.CommandWorkspacePath, encode(conflicting)); !errors.Is(err, devices.ErrCommandConflict) {
		t.Fatalf("different request reused: %v", err)
	}
	if _, err := store.ClaimCommand(h.ctx, otherDevice, command.ID); err == nil {
		t.Fatal("other device claimed request")
	}
	claimed, err := store.ClaimCommand(h.ctx, device, command.ID)
	if err != nil || !claimed.ExpiresAt.Equal(command.ExpiresAt) {
		t.Fatalf("claim extended expiry: %v", err)
	}
	if _, err := store.CompleteCommand(h.ctx, device, command.ID, devices.CommandResultInput{Status: devices.CommandSucceeded, Result: json.RawMessage(`{"path":"/private/secret"}`)}); err == nil {
		t.Fatal("plaintext accepted by center")
	}
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "执行电脑", "实际目录")
	if err := os.MkdirAll(path, 0700); err != nil {
		t.Fatal(err)
	}
	database := filepath.Join(root, "remote-device", "runs.db")
	journal, err := supervisor.OpenJournal(h.ctx, database)
	if err != nil {
		t.Fatal(err)
	}
	journal.Close()
	db, err := sql.Open("sqlite", database)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`INSERT INTO conversation_directory_facts(device_id,organization_id,space_id,conversation_id,agent_id,run_id,generation,path) VALUES(?,?,?,?,?,?,?,?)`, device1, orgA, spaceA, conversationID, agentA, runID, request.Generation, path)
	db.Close()
	if err != nil {
		t.Fatal(err)
	}
	actual, err := supervisor.ReadWorkDirectoryPath(h.ctx, database, request.Scope)
	if err != nil || actual != path {
		t.Fatal("actual remote fact unavailable", err)
	}
	encrypted, err := workdirpath.Seal(request, actual)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(encrypted)
	if _, err := store.CompleteCommand(h.ctx, device, command.ID, devices.CommandResultInput{Status: devices.CommandSucceeded, Result: raw}); err != nil {
		t.Fatal(err)
	}
	result, err := store.GetCommand(h.ctx, user, device1, command.ID)
	if err != nil {
		t.Fatal(err)
	}
	otherOrganization := tenancy.OrganizationSelection{OrganizationID: orgB, ActorID: alice, AuthorizationVersion: h.authVersion(orgB, alice)}
	if _, err := store.GetCommand(h.ctx, otherOrganization, device1, command.ID); err == nil {
		t.Fatal("cross-organization result visible")
	}
	expired := request
	expired.ExpiresAt = time.Now().Add(-time.Second).UnixMilli()
	h.exec(`UPDATE openfox.device_commands SET params=$1::jsonb WHERE id=$2::uuid`, []byte(encode(expired)), command.ID)
	if _, err := store.GetCommand(h.ctx, user, device1, command.ID); !errors.Is(err, devices.ErrCommandExpired) {
		t.Fatal("expired result still available", err)
	}
	h.exec(`UPDATE openfox.device_commands SET params=$1::jsonb WHERE id=$2::uuid`, []byte(encode(request)), command.ID)
	var stored string
	h.queryRow(`SELECT result::text FROM openfox.device_commands WHERE id=$1`, []any{command.ID}, &stored)
	if strings.Contains(stored, path) || strings.Contains(stored, "实际目录") || workdirpath.ValidateEnvelope([]byte(stored)) != nil {
		t.Fatal("path stored as plaintext")
	}
	var envelope workdirpath.Envelope
	json.Unmarshal(result.Result, &envelope)
	wrapped, _ := base64.StdEncoding.DecodeString(envelope.WrappedKey)
	secret, err := rsa.DecryptOAEP(sha256.New(), rand.Reader, private, wrapped, request.Binding())
	if err != nil {
		t.Fatal(err)
	}
	block, _ := aes.NewCipher(secret)
	aead, _ := cipher.NewGCM(block)
	nonce, _ := base64.StdEncoding.DecodeString(envelope.Nonce)
	ciphertext, _ := base64.StdEncoding.DecodeString(envelope.Ciphertext)
	plain, err := aead.Open(nil, nonce, ciphertext, request.Binding())
	if err != nil || string(plain) != path {
		t.Fatal("incorrect decrypted path", err)
	}
	h.exec(`UPDATE openfox.device_commands SET requested_by_user_id=$1::uuid WHERE id=$2::uuid`, carol, command.ID)
	if _, err := store.GetCommand(h.ctx, user, device1, command.ID); err == nil {
		t.Fatal("wrong requesting user accepted")
	}
	h.exec(`UPDATE openfox.device_commands SET requested_by_user_id=$1::uuid WHERE id=$2::uuid`, alice, command.ID)
	h.exec(`UPDATE openfox.conversations SET owner_user_id=$1::uuid,visibility='PRIVATE' WHERE id=$2::uuid`, carol, conversationID)
	if _, err := store.GetCommand(h.ctx, user, device1, command.ID); err == nil {
		t.Fatal("revoked conversation access still retrieves result")
	}
	h.exec(`UPDATE openfox.conversations SET owner_user_id=$1::uuid WHERE id=$2::uuid`, alice, conversationID)
	h.exec(`UPDATE openfox.physical_devices SET owner_user_id=$1::uuid WHERE id=$2::uuid`, carol, device1)
	if _, err := store.GetCommand(h.ctx, user, device1, command.ID); err == nil {
		t.Fatal("revoked device owner still retrieves result")
	}
	h.exec(`UPDATE openfox.physical_devices SET owner_user_id=$1::uuid WHERE id=$2::uuid`, alice, device1)
	h.exec(`UPDATE openfox.physical_devices SET last_seen_at=clock_timestamp()-interval '5 minutes' WHERE id=$1::uuid`, device1)
	if value, err := getTarget(alice, conversationID); err != nil || value.Online {
		t.Fatal("offline state incorrect", err)
	}
	h.setCapabilities(fullCapabilities)
	if value, err := getTarget(alice, conversationID); err != nil || value.Supported {
		t.Fatal("old daemon state incorrect", err)
	}
	if _, _, err := store.CreateCommand(h.ctx, user, device1, devices.CommandWorkspacePath, encode(conflicting)); err == nil {
		t.Fatal("unsupported daemon received request")
	}
	h.finish(runID, lease)
}
