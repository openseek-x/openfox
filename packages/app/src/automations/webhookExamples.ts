// Webhook 签名示例代码。签名规则见契约 receiveWebhook：
// X-OpenFox-Signature = "v1=" + hex(HMAC-SHA256(secret, timestamp + "." + body))，时间戳为 Unix 秒。
// 代码本身不翻译；示例使用环境变量 WEBHOOK_URL 与 WEBHOOK_SECRET，不包含真实密钥。

export interface WebhookExample {
  id: "curl" | "python" | "node" | "go" | "java";
  label: string;
  code: string;
}

export const WEBHOOK_EXAMPLES: WebhookExample[] = [
  {
    id: "curl",
    label: "curl",
    code: `TS=$(date +%s)
BODY='{"event":"ping"}'
SIG=$(printf '%s.%s' "$TS" "$BODY" | openssl dgst -sha256 -hmac "$WEBHOOK_SECRET" | sed 's/^.* //')
curl -X POST "$WEBHOOK_URL" \\
  -H "Content-Type: application/json" \\
  -H "X-OpenFox-Timestamp: $TS" \\
  -H "X-OpenFox-Signature: v1=$SIG" \\
  -d "$BODY"`,
  },
  {
    id: "python",
    label: "Python",
    code: `import hashlib
import hmac
import json
import os
import time
import urllib.request

url = os.environ["WEBHOOK_URL"]
secret = os.environ["WEBHOOK_SECRET"].encode()
body = json.dumps({"event": "ping"}).encode()
ts = str(int(time.time()))
sig = hmac.new(secret, ts.encode() + b"." + body, hashlib.sha256).hexdigest()
req = urllib.request.Request(url, data=body, method="POST", headers={
    "Content-Type": "application/json",
    "X-OpenFox-Timestamp": ts,
    "X-OpenFox-Signature": "v1=" + sig,
})
urllib.request.urlopen(req)`,
  },
  {
    id: "node",
    label: "Node.js",
    code: `import { createHmac } from "node:crypto";

const url = process.env.WEBHOOK_URL;
const secret = process.env.WEBHOOK_SECRET;
const body = JSON.stringify({ event: "ping" });
const ts = Math.floor(Date.now() / 1000).toString();
const sig = createHmac("sha256", secret).update(\`\${ts}.\${body}\`).digest("hex");
await fetch(url, {
  method: "POST",
  headers: {
    "Content-Type": "application/json",
    "X-OpenFox-Timestamp": ts,
    "X-OpenFox-Signature": \`v1=\${sig}\`,
  },
  body,
});`,
  },
  {
    id: "go",
    label: "Go",
    code: `package main

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"os"
	"strconv"
	"time"
)

func main() {
	url := os.Getenv("WEBHOOK_URL")
	secret := []byte(os.Getenv("WEBHOOK_SECRET"))
	body := []byte(\`{"event":"ping"}\`)
	ts := strconv.FormatInt(time.Now().Unix(), 10)
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(ts + "."))
	mac.Write(body)
	sig := hex.EncodeToString(mac.Sum(nil))
	req, _ := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-OpenFox-Timestamp", ts)
	req.Header.Set("X-OpenFox-Signature", "v1="+sig)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		panic(err)
	}
	defer resp.Body.Close()
}`,
  },
  {
    id: "java",
    label: "Java",
    code: `import java.net.URI;
import java.net.http.HttpClient;
import java.net.http.HttpRequest;
import java.net.http.HttpResponse;
import java.nio.charset.StandardCharsets;
import java.util.HexFormat;
import javax.crypto.Mac;
import javax.crypto.spec.SecretKeySpec;

public class Ping {
    public static void main(String[] args) throws Exception {
        String url = System.getenv("WEBHOOK_URL");
        String secret = System.getenv("WEBHOOK_SECRET");
        String body = "{\\"event\\":\\"ping\\"}";
        String ts = String.valueOf(System.currentTimeMillis() / 1000);
        Mac mac = Mac.getInstance("HmacSHA256");
        mac.init(new SecretKeySpec(secret.getBytes(StandardCharsets.UTF_8), "HmacSHA256"));
        String sig = HexFormat.of().formatHex(mac.doFinal((ts + "." + body).getBytes(StandardCharsets.UTF_8)));
        HttpRequest request = HttpRequest.newBuilder(URI.create(url))
            .header("Content-Type", "application/json")
            .header("X-OpenFox-Timestamp", ts)
            .header("X-OpenFox-Signature", "v1=" + sig)
            .POST(HttpRequest.BodyPublishers.ofString(body))
            .build();
        HttpClient.newHttpClient().send(request, HttpResponse.BodyHandlers.ofString());
    }
}`,
  },
];
