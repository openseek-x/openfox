package qq

import (
	"bytes"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
)

func signed(t *testing.T, body []byte) http.Header {
	t.Helper()
	key, e := privateKey("synthetic-secret")
	if e != nil {
		t.Fatal(e)
	}
	h := http.Header{}
	h.Set("X-Bot-Appid", "app")
	h.Set("X-Signature-Timestamp", "1725442341")
	h.Set("X-Signature-Ed25519", hex.EncodeToString(ed25519.Sign(key, append([]byte("1725442341"), body...))))
	return h
}
func TestOfficialSignatureVector(t *testing.T) {
	// QQ's published seed/public key agree. Its displayed signature does not
	// match the displayed timestamp/body (confirmed independently with Node's
	// built-in Ed25519). Keep that discrepancy visible, never accept that value.
	body := []byte(`{ "op": 0,"d": {}, "t": "GATEWAY_EVENT_NAME"}`)
	key, e := privateKey("naOC0ocQE3shWLAfffVLB1rhYPG7")
	if e != nil || !bytes.Equal(key.Public().(ed25519.PublicKey), []byte{215, 195, 98, 254, 120, 174, 248, 31, 242, 50, 135, 180, 147, 98, 139, 93, 176, 42, 60, 79, 227, 11, 33, 94, 77, 25, 96, 155, 93, 118, 103, 58}) {
		t.Fatal("official public key differs", e)
	}
	h := http.Header{}
	h.Set("X-Bot-Appid", "app")
	h.Set("X-Signature-Timestamp", "1725442341")
	h.Set("X-Signature-Ed25519", "865ad13a61752ca65e26bde6676459cd36cf1be609375b37bd62af366e1dc25a8dc789ba7f14e017ada3d554c671a911bfdf075ba54835b23391d509579ed002")
	if e := VerifySignature("app", "naOC0ocQE3shWLAfffVLB1rhYPG7", h, body); !errors.Is(e, ErrSignature) {
		t.Fatal("inconsistent example was accepted", e)
	}
	h.Set("X-Signature-Ed25519", "2eb9983ebb8bb209e78fd095942f58e442656656e7975d01e64f9023a84b7c964290fdd40e5500c33867ccfe9563b7e0b6bac0e1d42c13e787b304fd51f71102")
	if e := VerifySignature("app", "naOC0ocQE3shWLAfffVLB1rhYPG7", h, body); e != nil {
		t.Fatal(e)
	}
}
func TestSignedChallengeAndRawBoundary(t *testing.T) {
	body := []byte(`{"op":13,"d":{"plain_token":"challenge","event_ts":"1725442341"}}`)
	h := signed(t, body)
	if e := VerifySignature("app", "synthetic-secret", h, body); e != nil {
		t.Fatal(e)
	}
	r, e := Challenge("synthetic-secret", body)
	if e != nil {
		t.Fatal(e)
	}
	sig, e := hex.DecodeString(r.Signature)
	if e != nil {
		t.Fatal(e)
	}
	key, _ := privateKey("synthetic-secret")
	if r.PlainToken != "challenge" || !ed25519.Verify(key.Public().(ed25519.PublicKey), []byte("1725442341challenge"), sig) {
		t.Fatal("challenge response")
	}
	for _, tc := range []struct {
		name   string
		mutate func(http.Header, []byte) (http.Header, []byte)
	}{
		{"raw-whitespace", func(h http.Header, b []byte) (http.Header, []byte) { return h, append(b, ' ') }},
		{"wrong-app", func(h http.Header, b []byte) (http.Header, []byte) { h.Set("X-Bot-Appid", "other"); return h, b }},
		{"missing-app", func(h http.Header, b []byte) (http.Header, []byte) { h.Del("X-Bot-Appid"); return h, b }},
		{"multiple-signatures", func(h http.Header, b []byte) (http.Header, []byte) {
			h.Add("X-Signature-Ed25519", h.Get("X-Signature-Ed25519"))
			return h, b
		}},
		{"bad-hex", func(h http.Header, b []byte) (http.Header, []byte) { h.Set("X-Signature-Ed25519", "zz"); return h, b }},
		{"bad-timestamp", func(h http.Header, b []byte) (http.Header, []byte) { h.Set("X-Signature-Timestamp", "-1"); return h, b }},
		{"oversize", func(h http.Header, b []byte) (http.Header, []byte) { return h, make([]byte, MaxBodyBytes+1) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			headers, raw := tc.mutate(h.Clone(), append([]byte(nil), body...))
			if e := VerifySignature("app", "synthetic-secret", headers, raw); !errors.Is(e, ErrSignature) {
				t.Fatal(e)
			}
		})
	}
	for _, raw := range []string{`{"op":13,"op":13,"d":{"plain_token":"a","event_ts":"1"}}`, `{"op":13,"d":{"plain_token":"a","event_ts":""}}`, `{"op":0,"d":{}}`} {
		if _, e := Challenge("synthetic-secret", []byte(raw)); e == nil {
			t.Fatal("invalid challenge accepted")
		}
	}
}
func eventFixture(scope Scope) map[string]any {
	role := "member"
	kind := "C2C_MESSAGE_CREATE"
	author := map[string]any{"user_openid": "same-external-id", "member_openid": "wrong-scope", "bot": false, "union_openid": "do-not-merge"}
	if scope == Group {
		kind = "GROUP_AT_MESSAGE_CREATE"
		author = map[string]any{"member_openid": "same-external-id", "user_openid": "wrong-scope", "member_role": role, "bot": false}
	}
	return map[string]any{"op": 0, "id": "event-one", "t": kind, "d": map[string]any{"id": "original-message", "author": author, "content": " /task input ", "group_openid": "group-id", "message_type": 0, "timestamp": "2026-11-15T08:00:00+08:00", "message_scene": map[string]any{"source": "default", "ext": []string{"msg_idx=index==", "auth_token=secret-that-must-disappear"}}}}
}
func encoded(v any) []byte { b, _ := json.Marshal(v); return b }
func TestMessageScopedParsing(t *testing.T) {
	for _, scope := range []Scope{C2C, Group} {
		v, e := ParseMessage(encoded(eventFixture(scope)))
		if e != nil || v.Scope != scope || v.SenderID != "same-external-id" || v.MessageIndex != "index==" || v.Text != " /task input " || !v.SentAt.Equal(now) {
			t.Fatal(v, e)
		}
		if strings.Contains(string(encoded(v)), "secret-that") {
			t.Fatal("auth token retained")
		}
		if scope == Group && v.ChatID != "group-id" || scope == C2C && v.ChatID != v.SenderID {
			t.Fatal("typed scope lost")
		}
	}
	for _, tc := range []struct {
		name string
		edit func(map[string]any)
		kind error
	}{
		{"all-group", func(v map[string]any) { v["t"] = "GROUP_MESSAGE_CREATE" }, ErrUnsupported},
		{"history", func(v map[string]any) { v["d"].(map[string]any)["message_type"] = 102 }, ErrUnsupported},
		{"missing-kind", func(v map[string]any) { delete(v["d"].(map[string]any), "message_type") }, ErrUnsupported},
		{"bot", func(v map[string]any) { v["d"].(map[string]any)["author"].(map[string]any)["bot"] = true }, ErrUnsupported},
		{"unknown-scene", func(v map[string]any) {
			v["d"].(map[string]any)["message_scene"].(map[string]any)["source"] = "history"
		}, ErrUnsupported},
		{"missing-sender", func(v map[string]any) { delete(v["d"].(map[string]any)["author"].(map[string]any), "user_openid") }, ErrInvalid},
		{"empty", func(v map[string]any) { v["d"].(map[string]any)["content"] = " " }, ErrInvalid},
		{"oversize-text", func(v map[string]any) { v["d"].(map[string]any)["content"] = strings.Repeat("字", MaxTextRunes+1) }, ErrInvalid},
		{"invalid-time", func(v map[string]any) { v["d"].(map[string]any)["timestamp"] = "yesterday" }, ErrInvalid},
		{"duplicate-index", func(v map[string]any) {
			v["d"].(map[string]any)["message_scene"].(map[string]any)["ext"] = []string{"msg_idx=a", "msg_idx=b"}
		}, ErrInvalid},
	} {
		t.Run(tc.name, func(t *testing.T) {
			v := eventFixture(C2C)
			tc.edit(v)
			if _, e := ParseMessage(encoded(v)); !errors.Is(e, tc.kind) {
				t.Fatal(e)
			}
		})
	}
	if _, e := ParseMessage([]byte(`{"op":0,"op":13,"id":"event","t":"C2C_MESSAGE_CREATE","d":{}}`)); !errors.Is(e, ErrInvalid) {
		t.Fatal("duplicate envelope accepted", e)
	}
}
