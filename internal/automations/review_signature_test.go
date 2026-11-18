package automations

import (
	"net/http"
	"reflect"
	"strconv"
	"testing"
	"time"
)

func TestReviewSignedDedupeIgnoresUnsignedHeaders(t *testing.T) {
	now := time.Now()
	body := []byte(`{"event":"changed"}`)
	secret := []byte("回归测试密钥")
	for _, kind := range []string{"github", "openfox"} {
		t.Run(kind, func(t *testing.T) {
			header := make(http.Header)
			if kind == "github" {
				header.Set(headerGitHubSignature, "sha256="+hexHMAC(secret, body))
			} else {
				stamp := strconv.FormatInt(now.Unix(), 10)
				header.Set(headerOpenFoxTimestamp, stamp)
				header.Set(headerOpenFoxSignature, "v1="+hexHMAC(secret, append([]byte(stamp+"."), body...)))
			}
			if VerifySignature(true, secret, header, body, now) != SignatureValid {
				t.Fatal("签名测试数据无效")
			}
			first, _ := dedupeKeyFor(header, body)
			for _, name := range []string{"X-OpenFox-Delivery", "X-GitHub-Delivery", "X-Request-Id"} {
				header.Set(name, "改变未签名请求头")
				if key, _ := dedupeKeyFor(header, body); key != first {
					t.Errorf("%s 绕过签名去重", name)
				}
				header.Del(name)
			}
			if kind == "github" {
				header.Set(headerOpenFoxTimestamp, "未签名时间戳")
				if key, _ := dedupeKeyFor(header, body); key != first {
					t.Error("未签名时间戳绕过去重")
				}
			}
		})
	}
}

func TestDeliveryHeadersKeepOnlyDiagnosticFields(t *testing.T) {
	in := http.Header{}
	in.Set("Authorization", "synthetic")
	in.Set("Cookie", "synthetic")
	in.Set("X-Custom-Token", "synthetic")
	in.Set("X-Hub-Signature-256", "synthetic")
	in.Set("X-GitHub-Event", "push")
	want := map[string]string{"X-Github-Event": "push", "X-Hub-Signature-256": maskedHeaderValue}
	if got := storedHeaders(in); !reflect.DeepEqual(got, want) {
		t.Fatal("unexpected stored diagnostic fields")
	}
	got, err := decodeDeliveryHeaders(`{"Authorization":"synthetic","X-Custom-Token":"synthetic","X-Hub-Signature-256":"synthetic","X-Github-Event":"push"}`)
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatal("unexpected fields in delivery detail or replay")
	}
}
