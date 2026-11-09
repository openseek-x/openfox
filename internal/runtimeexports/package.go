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
	"errors"
	"strings"
	"time"

	"openfox/internal/artifacts"
	"openfox/internal/runtime"
)

var ErrInvalidPublicKey = errors.New("runtimeexports: invalid browser public key")

const encryptionOverhead = 12 + 16 // AES-GCM nonce and authentication tag.

type Manifest struct {
	Format      string    `json:"format"`
	Runtime     string    `json:"runtime"`
	SessionID   string    `json:"session_id"`
	CreatedAt   time.Time `json:"created_at"`
	NativeFile  string    `json:"native_file"`
	RestoreHint string    `json:"restore_hint"`
}

// Package exports a provider's one native session file and recovery hints.
func Package(ctx context.Context, provider, sessionID string, exporter runtime.NativeSessionExporter) ([]byte, error) {
	if exporter == nil || !knownProvider(provider) || !validSessionID(sessionID) {
		return nil, runtime.ErrInvalidRequest
	}
	file, err := exporter.ExportSession(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	name := file.Name
	if !validArchivePath(name) || len(file.Data) == 0 {
		return nil, runtime.ErrInvalidRequest
	}
	if int64(len(file.Data)) > artifacts.MaxArtifactBytes-int64(encryptionOverhead) {
		return nil, artifacts.ErrTooLarge
	}
	manifest := Manifest{
		Format: "openfox.runtime-session/v1", Runtime: provider, SessionID: sessionID,
		CreatedAt: time.Now().UTC(), NativeFile: name, RestoreHint: restoreHint(provider, sessionID, name),
	}
	manifestData, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return nil, err
	}
	var buffer bytes.Buffer
	archive := zip.NewWriter(&buffer)
	entries := []struct {
		name string
		data []byte
	}{
		{name: "manifest.json", data: manifestData},
		{name: "README.txt", data: []byte(readmeText(provider, sessionID, name))},
		{name: "native/" + name, data: file.Data},
	}
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			_ = archive.Close()
			return nil, err
		}
		writer, err := archive.Create(entry.name)
		if err != nil {
			_ = archive.Close()
			return nil, err
		}
		if _, err := writer.Write(entry.data); err != nil {
			_ = archive.Close()
			return nil, err
		}
		if int64(buffer.Len()) > artifacts.MaxArtifactBytes-int64(encryptionOverhead) {
			_ = archive.Close()
			return nil, artifacts.ErrTooLarge
		}
	}
	if err := archive.Close(); err != nil {
		return nil, err
	}
	if int64(buffer.Len()) > artifacts.MaxArtifactBytes-int64(encryptionOverhead) {
		return nil, artifacts.ErrTooLarge
	}
	return buffer.Bytes(), nil
}

// Encrypt encrypts the archive for the browser's ephemeral RSA-OAEP key.
// The returned ciphertext begins with a 12-byte GCM nonce.
func Encrypt(plaintext []byte, publicKey string) (ciphertext, wrappedKey []byte, err error) {
	key, err := ParsePublicKey(publicKey)
	if err != nil {
		return nil, nil, ErrInvalidPublicKey
	}
	contentKey := make([]byte, 32)
	if _, err := rand.Read(contentKey); err != nil {
		return nil, nil, err
	}
	block, err := aes.NewCipher(contentKey)
	if err != nil {
		return nil, nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, nil, err
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, nil, err
	}
	sealed := aead.Seal(nil, nonce, plaintext, nil)
	ciphertext = append(nonce, sealed...)
	wrappedKey, err = rsa.EncryptOAEP(sha256.New(), rand.Reader, key, contentKey, []byte("openfox-runtime-session-v1"))
	for index := range contentKey {
		contentKey[index] = 0
	}
	if err != nil {
		return nil, nil, err
	}
	return ciphertext, wrappedKey, nil
}

// ParsePublicKey validates a base64url-encoded RSA SubjectPublicKeyInfo.
func ParsePublicKey(publicKey string) (*rsa.PublicKey, error) {
	der, err := base64.RawURLEncoding.DecodeString(strings.TrimSpace(publicKey))
	if err != nil || len(der) > 2048 {
		return nil, ErrInvalidPublicKey
	}
	parsed, err := x509.ParsePKIXPublicKey(der)
	if err != nil {
		return nil, ErrInvalidPublicKey
	}
	key, ok := parsed.(*rsa.PublicKey)
	if !ok || key.N.BitLen() < 2048 || key.N.BitLen() > 4096 || key.E != 65537 {
		return nil, ErrInvalidPublicKey
	}
	return key, nil
}

func knownProvider(provider string) bool {
	switch provider {
	case "codex", "claude-code", "opencode", "pi":
		return true
	default:
		return false
	}
}

func validArchivePath(name string) bool {
	if name == "" || len(name) > 512 || strings.HasPrefix(name, "/") || strings.ContainsAny(name, `\\:`) || strings.IndexFunc(name, func(r rune) bool { return r < 0x20 || r == 0x7f }) >= 0 {
		return false
	}
	for _, part := range strings.Split(name, "/") {
		if part == "" || part == "." || part == ".." {
			return false
		}
	}
	return true
}

func validSessionID(id string) bool {
	if id == "" || len(id) > 256 || strings.TrimSpace(id) != id || strings.Contains(id, "..") || strings.ContainsAny(id, `/\\`) || id[0] == '-' {
		return false
	}
	for _, char := range id {
		if !(char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z' || char >= '0' && char <= '9' || char == '.' || char == '_' || char == '-') {
			return false
		}
	}
	return id[len(id)-1] != '.' && id[len(id)-1] != '_' && id[len(id)-1] != '-'
}

func restoreHint(provider, sessionID, name string) string {
	switch provider {
	case "opencode":
		return "Run `opencode session import native/" + name + " --directory <target-project>` to import the session, then open it with OpenCode."
	case "pi":
		return "Copy `native/sessions/` into PI_CODING_AGENT_DIR/sessions/ and open `native/" + name + "` with `pi --session <path>`; Pi uses the working directory recorded in the session."
	case "claude-code":
		return "Copy the `native/projects/` tree into CLAUDE_CONFIG_DIR/projects/, then run `claude --resume " + sessionID + "` from that project."
	default:
		return "Copy the `native/sessions/` directory into the target CODEX_HOME while preserving its subdirectories, then run `codex resume " + sessionID + "`."
	}
}

func readmeText(provider, sessionID, name string) string {
	return "OpenFox Runtime session package\n\n" +
		"Runtime: " + provider + "\nSession ID: " + sessionID + "\n\n" +
		"This archive contains one provider-native session file. It does not contain login credentials or Runtime settings. " +
		"Session transcripts and tool output may include sensitive text or local paths.\n\n" +
		restoreHint(provider, sessionID, name) + "\n"
}
