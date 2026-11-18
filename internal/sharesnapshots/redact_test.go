package sharesnapshots

import (
	"strings"
	"testing"
)

func rulesOf(findings []Finding) []string {
	names := make([]string, 0, len(findings))
	for _, f := range findings {
		names = append(names, f.Rule)
	}
	return names
}

func TestScanDetectsKnownCredentialFormats(t *testing.T) {
	cases := map[string]string{
		"private_key_block":     "key:\n-----BEGIN OPENSSH PRIVATE KEY-----\nabc",
		"aws_access_key_id":     "id AKIAIOSFODNN7EXAMPLE here",
		"github_token":          "token ghp_" + strings.Repeat("a1", 20),
		"gitlab_token":          "glpat-abcdefghij1234567890",
		"anthropic_api_key":     "sk-ant-api03-abcdefghijklmnopqrst",
		"openai_api_key":        "OPENAI sk-proj-" + strings.Repeat("aB3", 14),
		"slack_token":           "xoxb-1234567890-abcdefghij",
		"google_api_key":        "AIza" + strings.Repeat("a1B", 11) + "xy",
		"stripe_live_key":       "sk_live_abcdefghij1234567890",
		"jwt":                   "eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0.abcdefghijk1234",
		"authorization_header":  "Authorization: Bearer abcdef1234567890xyz",
		"bearer_token":          "use Bearer a1b2c3d4e5f6g7h8i9j0k1 now",
		"url_with_credentials":  "postgres://admin:s3cretPass@db.internal:5432/app",
		"credential_assignment": `config: {"api_key": "Zx81-abcdQ9"}`,
	}
	for rule, text := range cases {
		got := rulesOf(Scan(1, text))
		found := false
		for _, name := range got {
			found = found || name == rule
		}
		if !found {
			t.Errorf("规则 %s 未命中，文本 %q，实际 %v", rule, text, got)
		}
	}
}

func TestScanAvoidsFalsePositivesOnOrdinaryText(t *testing.T) {
	samples := []string{
		"请重置你的 password，然后再登录。",
		"The token is required for bearer authentication in this API.",
		"password: ********",
		"password = None",
		"api_key = os.environ[\"API_KEY\"]",
		"api_key: ${API_KEY}",
		"secret=$SECRET_VALUE_FROM_ENV",
		"export API_KEY=<your-key-here>",
		"password: your_password_here",
		"visit https://example.com:8080/path and http://localhost:3000",
		"git clone https://github.com/org/repo.git",
		"mail me at someone@example.com",
		"sk-learn is a python package; sk-ant is a prefix",
		"commit 0123456789abcdef0123456789abcdef01234567",
		"uuid 3f2a9b1c-4d5e-6f70-8a9b-0c1d2e3f4a5b",
		"AKIA only prefix and ASIA too",
		"eyJ is the base64 of {\"",
		"Authorization header carries credentials",
		"token: abc",
		"## 密钥管理\n密钥应存放在密钥管理服务里，不要写入代码。",
		"let secret = compute_secret(input)",
		"private_key: path/to/key.pem",
	}
	for _, text := range samples {
		if got := Scan(1, text); len(got) != 0 {
			t.Errorf("误报：%q -> %v", text, got)
		}
	}
}

func TestScanReportsOneBasedPositionWithoutEcho(t *testing.T) {
	text := "第一行\n中文前缀 AKIAIOSFODNN7EXAMPLE"
	got := Scan(3, text)
	if len(got) != 1 {
		t.Fatalf("got %v", got)
	}
	f := got[0]
	if f.Item != 3 || f.Line != 2 || f.Column != 6 {
		t.Fatalf("位置 = %+v，期望 item 3 行 2 列 6", f)
	}
}

func TestScanCapsFindings(t *testing.T) {
	text := strings.Repeat("AKIAIOSFODNN7EXAMPLE\n", 100)
	if got := Scan(1, text); len(got) != maxFindings {
		t.Fatalf("命中数 = %d，期望上限 %d", len(got), maxFindings)
	}
}
