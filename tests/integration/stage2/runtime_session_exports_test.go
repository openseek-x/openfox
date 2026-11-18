package stage2

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"testing"

	"openfox/internal/api"
	"openfox/internal/artifacts"
)

func TestRuntimeSessionExportStartAndStatusAreScopedToRequester(t *testing.T) {
	storage := artifacts.Local{Root: t.TempDir()}
	e := newAgEnvWithOptionsAndStorage(t, api.DispatchOptions{}, storage)
	f := newConvFixture(e)
	owner := e.client(agOwnerID)
	conversation := f.newConversation(owner)
	runID := f.insertRun(conversation.ID, "SUCCEEDED")
	const sessionID = "thread-session-export-123"
	e.exec(`UPDATE openfox.runs SET runtime = 'codex', model = 'gpt-5', approval_mode = 'FOLLOW_RUNTIME'
		WHERE organization_id = $1::uuid AND id = $2::uuid`, e.org, runID)
	e.exec(`INSERT INTO openfox.run_events (organization_id, run_id, generation, sequence, type, safe_payload, payload_hash)
		VALUES ($1::uuid, $2::uuid, 1, 1, 'started', jsonb_build_object('data', jsonb_build_object('thread_id', $3::text)), repeat('0', 64))`,
		e.org, runID, sessionID)
	e.exec(`UPDATE openfox.physical_devices SET capabilities = $2::jsonb WHERE id = $1::uuid`, e.deviceID,
		`{"runtime_session_export":true,"runtimes":{"codex":{"runnable":true}}}`)

	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	publicKey := base64.RawURLEncoding.EncodeToString(der)
	path := conversationPath(conversation.ID) + "/runtime-session-exports"
	var created struct {
		ID         string `json:"id"`
		Runtime    string `json:"runtime"`
		Status     string `json:"status"`
		WrappedKey string `json:"wrapped_key"`
	}
	owner.must(http.StatusAccepted, http.MethodPost, path, map[string]string{"public_key": publicKey}, &created)
	if created.ID == "" || created.Runtime != "codex" || created.Status != "PENDING" || created.WrappedKey != "" {
		t.Fatalf("created export = %+v", created)
	}

	var commandJSON []byte
	e.scalar(&commandJSON, `SELECT params FROM openfox.device_commands
		WHERE organization_id = $1::uuid AND kind = 'runtime_session_export' AND params->>'export_id' = $2`, e.org, created.ID)
	var commandParams struct {
		ExportID  string `json:"export_id"`
		RunID     string `json:"run_id"`
		Runtime   string `json:"runtime"`
		SessionID string `json:"session_id"`
		PublicKey string `json:"public_key"`
	}
	if err := json.Unmarshal(commandJSON, &commandParams); err != nil {
		t.Fatal(err)
	}
	if commandParams.ExportID != created.ID || commandParams.RunID != runID || commandParams.Runtime != "codex" ||
		commandParams.SessionID != sessionID || commandParams.PublicKey != publicKey {
		t.Fatalf("device command params = %+v", commandParams)
	}

	var status struct {
		ID         string `json:"id"`
		Status     string `json:"status"`
		WrappedKey string `json:"wrapped_key"`
	}
	owner.must(http.StatusOK, http.MethodGet, path+"/"+created.ID, nil, &status)
	if status.ID != created.ID || status.Status != "PENDING" || status.WrappedKey != "" {
		t.Fatalf("pending status = %+v", status)
	}

	owner.must(http.StatusNoContent, http.MethodPut, conversationPath(conversation.ID)+"/members/"+agMemberID,
		map[string]string{"access_level": "VIEWER"}, nil)
	e.client(agMemberID).expectError(http.StatusNotFound, "NOT_FOUND", http.MethodGet, path+"/"+created.ID, nil)
}
