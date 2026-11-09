package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"openfox/internal/daemon"
	"openfox/internal/daemon/commands"
	"openfox/internal/daemon/supervisor"
	"openfox/internal/workdirpath"
)

func workDirectoryScope() workdirpath.Scope {
	identifier := "11111111-1111-4111-8111-111111111111"
	return workdirpath.Scope{OrganizationID: identifier, DeviceID: identifier, SpaceID: identifier,
		ConversationID: identifier, AgentID: identifier, RunID: identifier, Generation: 1, UserID: identifier}
}

func TestWorkDirectoryPathUsesSelectedProfile(test *testing.T) {
	root, err := filepath.EvalSymlinks(test.TempDir())
	if err != nil {
		test.Fatal(err)
	}
	configDir := filepath.Join(root, "isolated-profile")
	actualPath := filepath.Join(root, "实际工作目录")
	if err := os.Mkdir(actualPath, 0700); err != nil {
		test.Fatal(err)
	}
	scope := workDirectoryScope()
	config := daemon.DeviceConfig{Version: 1, CenterURL: "https://test.invalid", WSSURL: "wss://test.invalid/api/v1/device/connect",
		DeviceID: scope.DeviceID, OrganizationID: scope.OrganizationID, KeyID: "test-key", CenterEpoch: 1,
		PrivateKeyFile: filepath.Join(configDir, "key"), TokenFile: filepath.Join(configDir, "token"), WorkspaceRoot: root}
	if err := daemon.WriteDeviceConfig(filepath.Join(configDir, daemon.DeviceConfigFileName), config); err != nil {
		test.Fatal(err)
	}
	database := filepath.Join(configDir, "runs.db")
	journal, err := supervisor.OpenJournal(test.Context(), database)
	if err != nil {
		test.Fatal(err)
	}
	journal.Close()
	connection, err := sql.Open("sqlite", database)
	if err != nil {
		test.Fatal(err)
	}
	defer connection.Close()
	_, err = connection.Exec(`INSERT INTO conversation_directory_facts
		(device_id,organization_id,space_id,conversation_id,agent_id,run_id,generation,path) VALUES(?,?,?,?,?,?,?,?)`,
		scope.DeviceID, scope.OrganizationID, scope.SpaceID, scope.ConversationID, scope.AgentID, scope.RunID, scope.Generation, actualPath)
	if err != nil {
		test.Fatal(err)
	}
	query := func(request workdirpath.Scope) struct {
		OK     bool   `json:"ok"`
		Path   string `json:"path"`
		Reason string `json:"reason"`
	} {
		test.Helper()
		var output bytes.Buffer
		args := []string{"--config-dir", configDir,
			"--organization-id", request.OrganizationID, "--device-id", request.DeviceID,
			"--space-id", request.SpaceID, "--conversation-id", request.ConversationID,
			"--agent-id", request.AgentID, "--run-id", request.RunID,
			"--generation", strconv.FormatInt(request.Generation, 10), "--user-id", request.UserID}
		if err := runWorkDirectoryPath(args, computerDeps{stdout: &output}); err != nil {
			test.Fatal(err)
		}
		var result struct {
			OK     bool   `json:"ok"`
			Path   string `json:"path"`
			Reason string `json:"reason"`
		}
		if err := json.Unmarshal(output.Bytes(), &result); err != nil {
			test.Fatal(err)
		}
		return result
	}
	if result := query(scope); !result.OK || result.Path != actualPath {
		test.Fatalf("selected profile result: %+v", result)
	}
	other := scope
	other.DeviceID = "22222222-2222-4222-8222-222222222222"
	if result := query(other); result.OK || result.Path != "" {
		test.Fatal("other device accepted")
	}
	if err := os.Remove(actualPath); err != nil {
		test.Fatal(err)
	}
	if result := query(scope); result.OK || result.Path != "" || result.Reason != "workspace_unavailable" {
		test.Fatal("removed path accepted")
	}
}

func TestWorkspacePathCommandNeverReturnsPlaintext(test *testing.T) {
	private, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		test.Fatal(err)
	}
	public, err := x509.MarshalPKIXPublicKey(&private.PublicKey)
	if err != nil {
		test.Fatal(err)
	}
	scope := workDirectoryScope()
	request := workdirpath.Request{Scope: scope, RequestID: scope.RunID,
		PublicKey: base64.StdEncoding.EncodeToString(public), ExpiresAt: time.Now().Add(30 * time.Second).UnixMilli()}
	raw, _ := json.Marshal(request)
	ctx := commands.WithIdentityForTest(test.Context(), scope.OrganizationID, request.RequestID)
	handlers := &commandHandlers{workDirectory: func(_ context.Context, actual workdirpath.Scope) (string, error) {
		if actual != scope {
			test.Fatal("scope changed")
		}
		return "/isolated/private-workspace", nil
	}}
	result, err := handlers.workspacePath(ctx, raw)
	encoded, _ := json.Marshal(result)
	if err != nil || workdirpath.ValidateEnvelope(encoded) != nil || bytes.Contains(encoded, []byte("private-workspace")) {
		test.Fatal("unencrypted result", err)
	}
	wrongContext := commands.WithIdentityForTest(test.Context(), "22222222-2222-4222-8222-222222222222", request.RequestID)
	for _, invalid := range []context.Context{test.Context(), wrongContext} {
		if _, err := handlers.workspacePath(invalid, raw); !errors.Is(err, commands.ErrInvalidParams) {
			test.Fatal("untrusted command context accepted", err)
		}
	}
	handlers.workDirectory = func(context.Context, workdirpath.Scope) (string, error) {
		return "", errors.New("private path must not be exposed")
	}
	var coded *commands.CodedError
	if _, err := handlers.workspacePath(ctx, raw); !errors.As(err, &coded) || coded.Code != "WORKSPACE_UNAVAILABLE" || coded.Result != nil {
		test.Fatal("unsafe error response", err)
	}
}
