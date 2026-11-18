package automations

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"openfox/internal/conversations"
)

func TestNextRunAfter(t *testing.T) {
	ny, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatal(err)
	}
	minute := func(v int) *int { return &v }
	tests := []struct {
		name     string
		schedule Schedule
		after    time.Time
		want     time.Time
	}{
		{
			name:     "每小时在第 15 分",
			schedule: Schedule{Frequency: "hourly", Minute: minute(15), Timezone: "UTC"},
			after:    time.Date(2026, 10, 30, 9, 20, 0, 0, time.UTC),
			want:     time.Date(2026, 10, 30, 10, 15, 0, 0, time.UTC),
		},
		{
			name:     "每天固定时间，恰好等于 after 时取下一天",
			schedule: Schedule{Frequency: "daily", Time: "09:00", Timezone: "Asia/Shanghai"},
			after:    time.Date(2026, 10, 30, 1, 0, 0, 0, time.UTC),
			want:     time.Date(2026, 10, 31, 1, 0, 0, 0, time.UTC),
		},
		{
			name:     "工作日跳过周末（2026-10-31 是周六）",
			schedule: Schedule{Frequency: "weekdays", Time: "09:00", Timezone: "UTC"},
			after:    time.Date(2026, 10, 30, 10, 0, 0, 0, time.UTC),
			want:     time.Date(2026, 11, 2, 9, 0, 0, 0, time.UTC),
		},
		{
			name:     "每周指定星期（周三 = 3）",
			schedule: Schedule{Frequency: "weekly", Time: "08:30", Weekdays: []int{3}, Timezone: "UTC"},
			after:    time.Date(2026, 10, 30, 0, 0, 0, 0, time.UTC),
			want:     time.Date(2026, 11, 4, 8, 30, 0, 0, time.UTC),
		},
		{
			name:     "cron 带步长：每 2 小时整点",
			schedule: Schedule{Frequency: "custom", Cron: "0 */2 * * *", Timezone: "UTC"},
			after:    time.Date(2026, 10, 30, 9, 0, 0, 0, time.UTC),
			want:     time.Date(2026, 10, 30, 10, 0, 0, 0, time.UTC),
		},
		{
			name:     "纽约夏令时结束之后：本地 09:00 是 EST（UTC-5）",
			schedule: Schedule{Frequency: "daily", Time: "09:00", Timezone: "America/New_York"},
			after:    time.Date(2026, 11, 1, 15, 0, 0, 0, time.UTC),
			want:     time.Date(2026, 11, 2, 9, 0, 0, 0, ny),
		},
		{
			name:     "纽约夏令时结束之前：本地 09:00 是 EDT（UTC-4）",
			schedule: Schedule{Frequency: "daily", Time: "09:00", Timezone: "America/New_York"},
			after:    time.Date(2026, 10, 30, 14, 0, 0, 0, time.UTC),
			want:     time.Date(2026, 10, 31, 9, 0, 0, 0, ny),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := NextRunAfter(tt.schedule, tt.after)
			if err != nil {
				t.Fatalf("NextRunAfter() error = %v", err)
			}
			if !got.Equal(tt.want) {
				t.Fatalf("NextRunAfter() = %s, want %s", got, tt.want)
			}
		})
	}
}

func TestValidateScheduleRejects(t *testing.T) {
	tests := []struct {
		name     string
		schedule Schedule
	}{
		{"cron 分钟是范围", Schedule{Frequency: "custom", Cron: "0-30 * * * *", Timezone: "UTC"}},
		{"cron 分钟是步长", Schedule{Frequency: "custom", Cron: "*/15 * * * *", Timezone: "UTC"}},
		{"cron 不是 5 段", Schedule{Frequency: "custom", Cron: "0 9 * *", Timezone: "UTC"}},
		{"cron 取值越界", Schedule{Frequency: "custom", Cron: "0 25 * * *", Timezone: "UTC"}},
		{"非法时区", Schedule{Frequency: "daily", Time: "09:00", Timezone: "Mars/Olympus"}},
		{"Local 时区", Schedule{Frequency: "daily", Time: "09:00", Timezone: "Local"}},
		{"时间格式错误", Schedule{Frequency: "daily", Time: "9:00", Timezone: "UTC"}},
		{"每周没有星期", Schedule{Frequency: "weekly", Time: "09:00", Timezone: "UTC"}},
		{"未知频率", Schedule{Frequency: "monthly", Timezone: "UTC"}},
		{"2 月 30 日永远不会到", Schedule{Frequency: "custom", Cron: "0 9 30 2 *", Timezone: "UTC"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := NextRunAfter(tt.schedule, time.Date(2026, 10, 30, 0, 0, 0, 0, time.UTC)); err == nil {
				t.Fatal("NextRunAfter() error = nil, want invalid")
			}
		})
	}
}

func TestTaskFilters(t *testing.T) {
	task := TaskSnapshot{ProjectID: "p1", TypeID: "bug", AssigneeID: "u1", ExecutorAgentID: "a1", Priority: "HIGH", LabelIDs: []string{"l1", "l2"}}
	tests := []struct {
		name    string
		filters []TaskFilter
		match   bool
	}{
		{"空条件全部命中", nil, true},
		{"项目相等", []TaskFilter{{Field: "project", Op: "is", Value: "p1"}}, true},
		{"项目不等", []TaskFilter{{Field: "project", Op: "is_not", Value: "p1"}}, false},
		{"标签包含", []TaskFilter{{Field: "label", Op: "includes", Value: "l2"}}, true},
		{"标签排除", []TaskFilter{{Field: "label", Op: "excludes", Value: "l2"}}, false},
		{"执行 Agent 相等", []TaskFilter{{Field: "agent", Op: "is", Value: "a1"}}, true},
		{"多条件全部满足", []TaskFilter{{Field: "priority", Op: "is", Value: "HIGH"}, {Field: "task_type", Op: "is", Value: "feature"}}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := MatchTaskFilters(tt.filters, task); got != tt.match {
				t.Fatalf("MatchTaskFilters() = %v, want %v", got, tt.match)
			}
		})
	}
}

func TestValidateTaskFiltersRejects(t *testing.T) {
	tests := []struct {
		name   string
		filter TaskFilter
	}{
		{"未知字段", TaskFilter{Field: "owner", Op: "is", Value: "x"}},
		{"label 用 is", TaskFilter{Field: "label", Op: "is", Value: "x"}},
		{"priority 用 includes", TaskFilter{Field: "priority", Op: "includes", Value: "HIGH"}},
		{"空值", TaskFilter{Field: "priority", Op: "is", Value: ""}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := ValidateTaskFilters([]TaskFilter{tt.filter}); err == nil {
				t.Fatal("ValidateTaskFilters() error = nil, want invalid")
			}
		})
	}
}

func TestVerifySignature(t *testing.T) {
	secret := []byte("whsec_test")
	body := []byte(`{"ok":true}`)
	now := time.Unix(1_800_000_000, 0)
	openFox := func(ts int64) http.Header {
		h := http.Header{}
		stamp := strconv.FormatInt(ts, 10)
		h.Set("X-OpenFox-Timestamp", stamp)
		h.Set("X-OpenFox-Signature", "v1="+hmacHex(secret, stamp+"."+string(body)))
		return h
	}
	github := http.Header{}
	github.Set("X-Hub-Signature-256", "sha256="+hmacHex(secret, string(body)))
	badGitHub := http.Header{}
	badGitHub.Set("X-Hub-Signature-256", "sha256=00")

	tests := []struct {
		name     string
		required bool
		secret   []byte
		header   http.Header
		want     string
	}{
		{"OpenFox 签名有效", true, secret, openFox(now.Unix()), SignatureValid},
		{"OpenFox 签名时间戳过期", true, secret, openFox(now.Unix() - 301), SignatureExpired},
		{"OpenFox 签名时间戳超前", true, secret, openFox(now.Unix() + 301), SignatureExpired},
		{"GitHub 签名有效", true, secret, github, SignatureValid},
		{"GitHub 签名错误", true, secret, badGitHub, SignatureInvalid},
		{"缺少签名头", true, secret, http.Header{}, SignatureMissing},
		{"要求签名但没有密钥", true, nil, http.Header{}, SignatureMissing},
		{"未要求且没有签名", false, secret, http.Header{}, SignatureNotRequired},
		{"未要求但带了有效签名", false, secret, github, SignatureValid},
		{"未要求但带了错误签名", false, secret, badGitHub, SignatureInvalid},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := VerifySignature(tt.required, tt.secret, tt.header, body, now); got != tt.want {
				t.Fatalf("VerifySignature() = %s, want %s", got, tt.want)
			}
		})
	}
}

func TestIPAllowed(t *testing.T) {
	tests := []struct {
		name      string
		allowlist []string
		source    string
		want      bool
	}{
		{"空白名单不限制", nil, "203.0.113.9", true},
		{"单个 IPv4 命中", []string{"203.0.113.9"}, "203.0.113.9", true},
		{"CIDR 命中", []string{"10.0.0.0/8"}, "10.20.30.40", true},
		{"CIDR 不命中", []string{"10.0.0.0/8"}, "11.0.0.1", false},
		{"IPv6 CIDR 命中", []string{"2001:db8::/32"}, "2001:db8::1", true},
		{"IPv4 映射地址按 IPv4 匹配", []string{"203.0.113.9"}, "::ffff:203.0.113.9", true},
		{"来源无法解析", []string{"10.0.0.0/8"}, "not-an-ip", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IPAllowed(tt.allowlist, tt.source); got != tt.want {
				t.Fatalf("IPAllowed() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestValidateIPAllowlistRejects(t *testing.T) {
	tooMany := make([]string, maxIPAllowlist+1)
	for i := range tooMany {
		tooMany[i] = "10.0.0.1"
	}
	for _, entries := range [][]string{{"not-an-ip"}, {"10.0.0.0/33"}, tooMany} {
		if err := ValidateIPAllowlist(entries); err == nil {
			t.Fatalf("ValidateIPAllowlist(%v) error = nil, want invalid", entries)
		}
	}
}

func TestTokenHashIsStable(t *testing.T) {
	token, hash, err := newWebhookToken()
	if err != nil {
		t.Fatal(err)
	}
	if len(hash) != sha256.Size || string(hash) != string(TokenHash(token)) {
		t.Fatal("token hash does not match TokenHash()")
	}
}

func hmacHex(key []byte, message string) string {
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(message))
	return hex.EncodeToString(mac.Sum(nil))
}

func TestComposeMessageStaysWithinLimit(t *testing.T) {
	limit := conversations.MaxMessageLength
	tests := []struct {
		name    string
		prompt  string
		context string
		want    string
	}{
		{"正常拼接", "检查构建", "- 触发原因：定时", "检查构建\n\n---\n触发上下文\n- 触发原因：定时"},
		{"上下文超限时只截断上下文", strings.Repeat("好", 20000), strings.Repeat("好", 20000), ""},
		{"提示词单独超限时截断提示词", strings.Repeat("好", 26667), "上下文", strings.Repeat("好", 21845)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := composeMessage(tt.prompt, tt.context)
			if len(got) > limit || !utf8.ValidString(got) {
				t.Fatalf("composeMessage() length = %d, valid UTF-8 = %v, want <= %d", len(got), utf8.ValidString(got), limit)
			}
			if tt.want != "" && got != tt.want {
				t.Fatalf("composeMessage() = %q, want %q", got, tt.want)
			}
		})
	}
}
