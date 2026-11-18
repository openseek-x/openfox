package secrets

import (
	"bytes"
	"errors"
	"testing"
)

func TestOAuthEnvelopeLimitDoesNotChangeOrdinarySecrets(t *testing.T) {
	b, e := New(make([]byte, KeySize))
	if e != nil {
		t.Fatal(e)
	}
	p := bytes.Repeat([]byte("t"), MaxOAuthPlaintext)
	s, e := b.SealOAuth(p, "org:grant")
	if e != nil {
		t.Fatal(e)
	}
	got, e := b.Open(s, "org:grant")
	if e != nil || !bytes.Equal(got, p) {
		t.Fatal(e)
	}
	if _, e = b.Open(s, "other:grant"); !errors.Is(e, ErrDecrypt) {
		t.Fatal(e)
	}
	if _, e = b.SealOAuth(append(p, 'x'), "a"); !errors.Is(e, ErrTooLarge) {
		t.Fatal(e)
	}
	if _, e = b.Seal(p, "a"); !errors.Is(e, ErrTooLarge) {
		t.Fatal(e)
	}
	s.Ciphertext[0] ^= 1
	if _, e = b.Open(s, "org:grant"); !errors.Is(e, ErrDecrypt) {
		t.Fatal(e)
	}
	var missing *Box
	if _, e = missing.SealOAuth(nil, "a"); !errors.Is(e, ErrUnavailable) {
		t.Fatal(e)
	}
}
