package stage2c

// GET /devices/center 返回的当前中心地址必须与安装令牌里解出来的 CenterURL 完全一致；
// 组织成员可读，非成员 404，未登录 401。

import (
	"net/http"
	"testing"

	"openfox/internal/devices"
)

func TestDeviceCenterURLMatchesInstallToken(t *testing.T) {
	e := newEnv(t)
	var center struct {
		CenterURL string `json:"center_url"`
	}
	e.call(http.StatusOK, http.MethodGet, "devices/center", nil, &center)
	if center.CenterURL != e.center {
		t.Fatalf("center_url = %q, want %q", center.CenterURL, e.center)
	}

	var issued struct {
		InstallToken string `json:"install_token"`
	}
	e.call(http.StatusCreated, http.MethodPost, "devices/registration-tokens", map[string]any{"name": "Center URL", "scope": "PERSONAL"}, &issued)
	decoded, err := devices.DecodeInstallToken(issued.InstallToken)
	if err != nil {
		t.Fatal(err)
	}
	if decoded.CenterURL != center.CenterURL {
		t.Fatalf("install token center = %q, center endpoint = %q", decoded.CenterURL, center.CenterURL)
	}

	// 非成员：用一个没有成员关系的组织，404（与其他电脑接口一致）。
	other := newID(t)
	e.exec(`INSERT INTO openfox.organizations (id, slug, name) VALUES ($1::uuid, $2, 'Stage 8a other')`, other, "s8a-"+other[:8])
	response, err := e.http.Get(e.center + "/api/v1/organizations/" + other + "/devices/center")
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusNotFound {
		t.Fatalf("non-member status = %d, want 404", response.StatusCode)
	}

	// 未登录：401。
	anonymous, err := http.Get(e.center + "/api/v1/organizations/" + e.org + "/devices/center")
	if err != nil {
		t.Fatal(err)
	}
	anonymous.Body.Close()
	if anonymous.StatusCode != http.StatusUnauthorized {
		t.Fatalf("anonymous status = %d, want 401", anonymous.StatusCode)
	}
}
