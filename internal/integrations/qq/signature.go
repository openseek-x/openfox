package qq

import (
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strings"
)

func privateKey(value string) (ed25519.PrivateKey, error) {
	if !secret(value) {
		return nil, ErrInvalid
	}
	seed := value
	for len(seed) < ed25519.SeedSize {
		seed += seed
	}
	return ed25519.NewKeyFromSeed([]byte(seed[:ed25519.SeedSize])), nil
}
func header(h http.Header, key string) (string, error) {
	v := h.Values(key)
	if len(v) != 1 || v[0] == "" || strings.TrimSpace(v[0]) != v[0] {
		return "", ErrSignature
	}
	return v[0], nil
}

// The signed input is the ORIGINAL timestamp bytes followed by ORIGINAL body.
// There is no invented clock window: QQ has not specified its retry horizon.
func VerifySignature(appID, appSecret string, headers http.Header, body []byte) error {
	if !ID(appID) || len(body) == 0 || len(body) > MaxBodyBytes {
		return ErrSignature
	}
	remote, e := header(headers, "X-Bot-Appid")
	if e != nil || remote != appID {
		return ErrSignature
	}
	ts, e := header(headers, "X-Signature-Timestamp")
	if e != nil || len(ts) > 20 {
		return ErrSignature
	}
	for _, r := range ts {
		if r < '0' || r > '9' {
			return ErrSignature
		}
	}
	sig, e := header(headers, "X-Signature-Ed25519")
	if e != nil {
		return ErrSignature
	}
	decoded, e := hex.DecodeString(sig)
	if e != nil || len(decoded) != ed25519.SignatureSize {
		return ErrSignature
	}
	key, e := privateKey(appSecret)
	if e != nil {
		return ErrSignature
	}
	signed := make([]byte, 0, len(ts)+len(body))
	signed = append(signed, ts...)
	signed = append(signed, body...)
	if !ed25519.Verify(key.Public().(ed25519.PublicKey), signed, decoded) {
		return ErrSignature
	}
	return nil
}

type ChallengeResponse struct {
	PlainToken string `json:"plain_token"`
	Signature  string `json:"signature"`
}

// Challenge must be called only after VerifySignature, including for op 13.
func Challenge(appSecret string, body []byte) (ChallengeResponse, error) {
	var v struct {
		Op   *int `json:"op"`
		Data struct {
			Token     string `json:"plain_token"`
			Timestamp string `json:"event_ts"`
		} `json:"d"`
	}
	if !object(body) || json.Unmarshal(body, &v) != nil || v.Op == nil || *v.Op != 13 || !ID(v.Data.Token) || len(v.Data.Timestamp) > 20 || v.Data.Timestamp == "" {
		return ChallengeResponse{}, ErrInvalid
	}
	for _, r := range v.Data.Timestamp {
		if r < '0' || r > '9' {
			return ChallengeResponse{}, ErrInvalid
		}
	}
	key, e := privateKey(appSecret)
	if e != nil {
		return ChallengeResponse{}, e
	}
	return ChallengeResponse{PlainToken: v.Data.Token, Signature: hex.EncodeToString(ed25519.Sign(key, []byte(v.Data.Timestamp+v.Data.Token)))}, nil
}
