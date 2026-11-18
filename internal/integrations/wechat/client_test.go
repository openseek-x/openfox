package wechat

import "testing"

func TestNormalizeBase(t *testing.T) {
	prod := New("http://127.0.0.1:9000", "t", false)
	dev := New("http://127.0.0.1:9000", "t", true)
	allowed := map[string]string{
		"https://ilinkai.weixin.qq.com":       "https://ilinkai.weixin.qq.com",
		"ilinkai.weixin.qq.com":               "https://ilinkai.weixin.qq.com",
		"https://weixin.qq.com/some/path?x=1": "https://weixin.qq.com",
		"https://A.Weixin.QQ.com:443":         "https://a.weixin.qq.com",
	}
	for in, want := range allowed {
		if got, err := prod.NormalizeBase(in); err != nil || got != want {
			t.Errorf("NormalizeBase(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, in := range []string{"", "http://weixin.qq.com", "https://evil.com", "https://weixin.qq.com.evil.com", "https://evilweixin.qq.com",
		"https://u@weixin.qq.com", "https://weixin.qq.com:8443", "file:///etc/passwd", "http://127.0.0.1:9000", "https://169.254.169.254"} {
		if got, err := prod.NormalizeBase(in); err == nil {
			t.Errorf("production NormalizeBase(%q) = %q, want rejection", in, got)
		}
	}
	if _, err := dev.NormalizeBase("http://127.0.0.1:9000"); err != nil {
		t.Errorf("dev must allow the configured base: %v", err)
	}
	if _, err := dev.NormalizeBase("http://127.0.0.1:9001"); err == nil {
		t.Error("dev must not allow another port")
	}
}

func TestMessageText(t *testing.T) {
	var m Message
	if _, ok := m.Text(); ok {
		t.Fatal("empty message has no text")
	}
}
