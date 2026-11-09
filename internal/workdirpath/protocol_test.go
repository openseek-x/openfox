package workdirpath

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestWorkspacePathSealingAndBinding(t *testing.T) {
	private, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	der, _ := x509.MarshalPKIXPublicKey(&private.PublicKey)
	id := "11111111-1111-4111-8111-111111111111"
	request := Request{Scope: Scope{OrganizationID: id, DeviceID: id, SpaceID: id, ConversationID: id, AgentID: id, RunID: id, Generation: 1, UserID: id}, RequestID: id, PublicKey: base64.StdEncoding.EncodeToString(der), ExpiresAt: time.Now().Add(30 * time.Second).UnixMilli()}
	raw, _ := json.Marshal(request)
	parsed, err := Parse(raw)
	if err != nil || parsed != request {
		t.Fatalf("parse: %v", err)
	}
	path := "/private/tmp/工作目录/worktree"
	envelope, err := Seal(request, path)
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(envelope)
	if ValidateEnvelope(encoded) != nil || bytes.Contains(encoded, []byte(path)) {
		t.Fatal("invalid encrypted result")
	}
	wrapped, _ := base64.StdEncoding.DecodeString(envelope.WrappedKey)
	secret, err := rsa.DecryptOAEP(sha256.New(), rand.Reader, private, wrapped, request.Binding())
	if err != nil {
		t.Fatal(err)
	}
	block, _ := aes.NewCipher(secret)
	authenticated, _ := cipher.NewGCM(block)
	nonce, _ := base64.StdEncoding.DecodeString(envelope.Nonce)
	ciphertext, _ := base64.StdEncoding.DecodeString(envelope.Ciphertext)
	plain, err := authenticated.Open(nil, nonce, ciphertext, request.Binding())
	if err != nil || string(plain) != path {
		t.Fatal("decrypt failed", err)
	}
	request.Generation++
	if _, err := rsa.DecryptOAEP(sha256.New(), rand.Reader, private, wrapped, request.Binding()); err == nil {
		t.Fatal("cross-generation key accepted")
	}
	if _, err := authenticated.Open(nil, nonce, ciphertext, request.Binding()); err == nil {
		t.Fatal("cross-generation path accepted")
	}
	for _, bad := range []string{"", "\x00", strings.Repeat("a", 4097), "folder\nsecret"} {
		if _, err := Seal(request, bad); err == nil {
			t.Fatal("invalid path accepted")
		}
	}
	for _, invalid := range [][]byte{[]byte(`{}`), append(raw, []byte(`{}`)...), bytes.Replace(raw, []byte(`"generation":1`), []byte(`"generation":0`), 1), bytes.Replace(raw, []byte(`"public_key"`), []byte(`"path"`), 1)} {
		if _, err := Parse(invalid); err == nil {
			t.Fatal("invalid request accepted")
		}
	}
	for _, invalid := range []string{`{"path":"/secret"}`, strings.Replace(string(encoded), Algorithm, "other", 1), strings.Replace(string(encoded), `"nonce":`, `"path":`, 1)} {
		if ValidateEnvelope([]byte(invalid)) == nil {
			t.Fatal("invalid envelope accepted")
		}
	}
	request.ExpiresAt = time.Now().Add(-time.Second).UnixMilli()
	if _, err := Seal(request, path); err == nil {
		t.Fatal("expired request accepted")
	}
}
