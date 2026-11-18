package automations

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"strconv"
	"time"
)

// 签名状态与 webhook_deliveries.signature_status 的取值一致。
const (
	SignatureValid       = "valid"
	SignatureInvalid     = "invalid"
	SignatureMissing     = "missing"
	SignatureNotRequired = "not_required"
	SignatureExpired     = "expired"
	SignatureIPBlocked   = "ip_blocked"
)

const (
	headerOpenFoxSignature = "X-OpenFox-Signature"
	headerOpenFoxTimestamp = "X-OpenFox-Timestamp"
	headerGitHubSignature  = "X-Hub-Signature-256"
	// signatureSkew 是 X-OpenFox-Timestamp 允许的最大偏差。
	signatureSkew = 300 * time.Second
)

// VerifySignature 校验 webhook 签名并返回签名状态。
// 调用方只有在返回 SignatureValid 或 SignatureNotRequired 时接受请求。
// 签名要求开启时，缺少签名头记 missing；签名要求关闭时，带了签名头且有密钥才校验，不通过记 invalid。
func VerifySignature(required bool, secret []byte, header http.Header, body []byte, now time.Time) string {
	if len(secret) == 0 {
		if required {
			return SignatureMissing
		}
		return SignatureNotRequired
	}
	if sig := header.Get(headerOpenFoxSignature); sig != "" {
		return verifyOpenFox(secret, sig, header.Get(headerOpenFoxTimestamp), body, now)
	}
	if sig := header.Get(headerGitHubSignature); sig != "" {
		want := "sha256=" + hexHMAC(secret, body)
		return compareSignature(want, sig)
	}
	if required {
		return SignatureMissing
	}
	return SignatureNotRequired
}

// verifyOpenFox 校验 v1=hex(HMAC-SHA256(secret, timestamp + "." + body))，时间戳偏差超过 300 秒记 expired。
func verifyOpenFox(secret []byte, sig, timestamp string, body []byte, now time.Time) string {
	seconds, err := strconv.ParseInt(timestamp, 10, 64)
	if err != nil {
		return SignatureInvalid
	}
	skew := now.Sub(time.Unix(seconds, 0))
	if skew > signatureSkew || skew < -signatureSkew {
		return SignatureExpired
	}
	signed := make([]byte, 0, len(timestamp)+1+len(body))
	signed = append(signed, timestamp...)
	signed = append(signed, '.')
	signed = append(signed, body...)
	want := "v1=" + hexHMAC(secret, signed)
	return compareSignature(want, sig)
}

func hexHMAC(secret, message []byte) string {
	mac := hmac.New(sha256.New, secret)
	mac.Write(message)
	return hex.EncodeToString(mac.Sum(nil))
}

// compareSignature 使用常量时间比较，避免通过响应时间推测签名。
func compareSignature(want, got string) string {
	if hmac.Equal([]byte(want), []byte(got)) {
		return SignatureValid
	}
	return SignatureInvalid
}
