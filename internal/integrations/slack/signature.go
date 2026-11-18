package slack

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"strings"
	"time"
)

// VerifySignature verifies the exact raw bytes before any JSON/form decoding.
// Receipt persistence/deduplication and app/team routing remain the caller's job.
func VerifySignature(signingSecret, timestampHeader, signature string, raw []byte, now time.Time) error {
	if !secret(signingSecret) || len(raw) > MaxEventBytes || len(timestampHeader) < 1 || len(timestampHeader) > 12 || len(signature) != 67 || !strings.HasPrefix(signature, "v0=") {
		return ErrSignature
	}
	seconds, e := strconv.ParseInt(timestampHeader, 10, 64)
	if e != nil || strconv.FormatInt(seconds, 10) != timestampHeader {
		return ErrSignature
	}
	delta := now.Unix() - seconds
	if delta < -300 || delta > 300 {
		return ErrSignature
	}
	supplied, e := hex.DecodeString(signature[3:])
	if e != nil {
		return ErrSignature
	}
	mac := hmac.New(sha256.New, []byte(signingSecret))
	mac.Write([]byte("v0:" + timestampHeader + ":"))
	mac.Write(raw)
	if !hmac.Equal(mac.Sum(nil), supplied) {
		return ErrSignature
	}
	return nil
}
