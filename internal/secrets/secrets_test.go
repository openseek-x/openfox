package secrets

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func testBox(t *testing.T) *Box {
	t.Helper()
	box, err := New(bytes.Repeat([]byte{7}, KeySize))
	if err != nil {
		t.Fatal(err)
	}
	return box
}

func TestSealOpenRoundTripAndAAD(t *testing.T) {
	box := testBox(t)
	aad := EnvAAD("org", "agent", "ANTHROPIC_API_KEY")
	sealed, err := box.Seal([]byte("sk-secret"), aad)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(sealed.Ciphertext, []byte("sk-secret")) || len(sealed.Nonce) != NonceSize || sealed.KeyID != box.KeyID() {
		t.Fatalf("sealed = %+v", sealed)
	}
	plain, err := box.Open(sealed, aad)
	if err != nil || string(plain) != "sk-secret" {
		t.Fatalf("open = %q, %v", plain, err)
	}
	// 挪到别的 Agent / 名称：解密失败。
	if _, err := box.Open(sealed, EnvAAD("org", "other", "ANTHROPIC_API_KEY")); !errors.Is(err, ErrDecrypt) {
		t.Fatalf("cross-row open err = %v", err)
	}
	tampered := sealed
	tampered.Ciphertext = append([]byte{}, sealed.Ciphertext...)
	tampered.Ciphertext[0] ^= 1
	if _, err := box.Open(tampered, aad); !errors.Is(err, ErrDecrypt) {
		t.Fatalf("tampered err = %v", err)
	}
	// 每次加密 nonce 不同。
	again, _ := box.Seal([]byte("sk-secret"), aad)
	if bytes.Equal(again.Nonce, sealed.Nonce) {
		t.Fatal("nonce reused")
	}
}

func TestOtherKeyAndLimits(t *testing.T) {
	box := testBox(t)
	other, _ := New(bytes.Repeat([]byte{9}, KeySize))
	sealed, _ := box.Seal([]byte("v"), "a")
	if _, err := other.Open(sealed, "a"); !errors.Is(err, ErrDecrypt) {
		t.Fatalf("other key err = %v", err)
	}
	if _, err := box.Seal(make([]byte, MaxPlaintext+1), "a"); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("too large err = %v", err)
	}
	var none *Box
	if _, err := none.Seal([]byte("v"), "a"); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("nil box err = %v", err)
	}
	if _, err := New([]byte("short")); err == nil {
		t.Fatal("short key accepted")
	}
}

func TestLoadFile(t *testing.T) {
	dir := t.TempDir()
	if box, err := LoadFile(""); box != nil || err != nil {
		t.Fatalf("empty path = %v, %v", box, err)
	}
	hexKey := filepath.Join(dir, "hex.key")
	if err := os.WriteFile(hexKey, []byte(strings.Repeat("ab", KeySize)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if box, err := LoadFile(hexKey); err != nil || box == nil {
		t.Fatalf("hex key = %v, %v", box, err)
	}
	rawKey := filepath.Join(dir, "raw.key")
	if err := os.WriteFile(rawKey, bytes.Repeat([]byte{1}, KeySize), 0o600); err != nil {
		t.Fatal(err)
	}
	if box, err := LoadFile(rawKey); err != nil || box == nil {
		t.Fatalf("raw key = %v, %v", box, err)
	}
	open := filepath.Join(dir, "open.key")
	if err := os.WriteFile(open, bytes.Repeat([]byte{1}, KeySize), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadFile(open); err == nil {
		t.Fatal("world-readable key accepted")
	}
	bad := filepath.Join(dir, "bad.key")
	if err := os.WriteFile(bad, []byte("too short"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadFile(bad); err == nil {
		t.Fatal("bad key accepted")
	}
}
