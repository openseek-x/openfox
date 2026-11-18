package runtimeexports

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"io"
	"strings"
	"testing"

	"openfox/internal/runtime"
)

type testSessionExporter struct {
	file runtime.SessionFile
}

func (e testSessionExporter) ExportSession(context.Context, string) (runtime.SessionFile, error) {
	return e.file, nil
}

func TestPackagePreservesNativePathAndExcludesUnrelatedFiles(t *testing.T) {
	const sessionID = "thread-123"
	rollout := []byte(`{"type":"session_meta","id":"thread-123"}` + "\n")
	archive, err := Package(context.Background(), "codex", sessionID, testSessionExporter{file: runtime.SessionFile{
		Name: "sessions/2026/11/08/rollout-2026-11-08T12-00-00-thread-123.jsonl",
		Data: rollout,
	}})
	if err != nil {
		t.Fatal(err)
	}
	reader, err := zip.NewReader(bytes.NewReader(archive), int64(len(archive)))
	if err != nil {
		t.Fatal(err)
	}
	if len(reader.File) != 3 {
		t.Fatalf("archive entries = %d, want manifest, README, native session only", len(reader.File))
	}
	contents := map[string][]byte{}
	for _, file := range reader.File {
		body, err := file.Open()
		if err != nil {
			t.Fatal(err)
		}
		data, readErr := io.ReadAll(body)
		_ = body.Close()
		if readErr != nil {
			t.Fatal(readErr)
		}
		contents[file.Name] = data
	}
	if !bytes.Equal(contents["native/sessions/2026/11/08/rollout-2026-11-08T12-00-00-thread-123.jsonl"], rollout) {
		t.Fatal("native session file path or contents were not preserved")
	}
	var manifest Manifest
	if err := json.Unmarshal(contents["manifest.json"], &manifest); err != nil {
		t.Fatal(err)
	}
	if manifest.Runtime != "codex" || manifest.SessionID != sessionID || manifest.NativeFile != "sessions/2026/11/08/rollout-2026-11-08T12-00-00-thread-123.jsonl" {
		t.Fatalf("manifest = %+v", manifest)
	}
}

func TestPackageRejectsUnsafeNativePath(t *testing.T) {
	for _, name := range []string{"../outside.jsonl", `C:/outside.jsonl`, `sessions\\outside.jsonl`} {
		t.Run(name, func(t *testing.T) {
			_, err := Package(context.Background(), "pi", "session-1", testSessionExporter{file: runtime.SessionFile{
				Name: name, Data: []byte("{}"),
			}})
			if err != runtime.ErrInvalidRequest {
				t.Fatalf("Package error = %v, want invalid request", err)
			}
		})
	}
}

func TestPackageRejectsSessionIDThatCouldAlterRestoreInstructions(t *testing.T) {
	for _, sessionID := range []string{"../session", "-resume", "id`\nclaude --dangerous", "session/child"} {
		t.Run(sessionID, func(t *testing.T) {
			_, err := Package(context.Background(), "claude-code", sessionID, testSessionExporter{file: runtime.SessionFile{
				Name: "projects/project/session.jsonl", Data: []byte("{}"),
			}})
			if err != runtime.ErrInvalidRequest {
				t.Fatalf("Package error = %v, want invalid request", err)
			}
		})
	}
}

func TestOpenCodeRestoreHintUsesNativeImportCommand(t *testing.T) {
	hint := restoreHint("opencode", "ses_example", "opencode-session.json")
	if !strings.Contains(hint, "opencode session import native/opencode-session.json --directory") {
		t.Fatalf("restore hint = %q", hint)
	}
}

func TestEncryptRoundTrip(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	publicDER, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	publicKey := base64.RawURLEncoding.EncodeToString(publicDER)
	plaintext := []byte("native session archive")
	ciphertext, wrapped, err := Encrypt(plaintext, publicKey)
	if err != nil {
		t.Fatal(err)
	}
	contentKey, err := rsa.DecryptOAEP(sha256.New(), rand.Reader, key, wrapped, []byte("openfox-runtime-session-v1"))
	if err != nil {
		t.Fatal(err)
	}
	block, err := aes.NewCipher(contentKey)
	if err != nil {
		t.Fatal(err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		t.Fatal(err)
	}
	if got := len(ciphertext); got < aead.NonceSize()+aead.Overhead() {
		t.Fatalf("encrypted bytes = %d, too short", got)
	}
	decrypted, err := aead.Open(nil, ciphertext[:aead.NonceSize()], ciphertext[aead.NonceSize():], nil)
	if err != nil || !bytes.Equal(decrypted, plaintext) {
		t.Fatalf("decrypt error = %v, plaintext = %q", err, decrypted)
	}
}

func TestParsePublicKeyRejectsNonRSAAndShortKeys(t *testing.T) {
	if _, err := ParsePublicKey("invalid"); err == nil {
		t.Fatal("expected invalid public key")
	}
	key, err := rsa.GenerateKey(rand.Reader, 1024)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ParsePublicKey(base64.RawURLEncoding.EncodeToString(der)); err == nil {
		t.Fatal("expected rejection of an undersized RSA key")
	}
}
