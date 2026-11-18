package stage2

import (
	"net/http"
	"testing"
)

func TestDeviceTimezoneHeartbeatAndLegacy(t *testing.T) {
	e := newAgEnv(t)
	owner := e.client(agOwnerID)
	first := e.registerDevice(e.issueToken(owner, "ORGANIZATION").InstallToken)
	second := e.registerDevice(e.issueToken(owner, "ORGANIZATION").InstallToken)
	for i, d := range []devDevice{first, second} {
		zone := []string{"Asia/Shanghai", "America/New_York"}[i]
		r := e.deviceCall(d, http.MethodPost, "/v1/device/heartbeat", map[string]any{
			"center_epoch": d.CenterEpoch, "protocol_major": 1, "protocol_minor": 0, "daemon_version": "r12-test",
			"operating_system": "darwin", "architecture": "arm64", "capabilities": map[string]any{"timezone": zone},
		})
		if r.Status != http.StatusOK {
			t.Fatalf("heartbeat=%d %s", r.Status, r.Body)
		}
		var persisted string
		if err := e.superuser.QueryRow(e.ctx, `SELECT capabilities->>'timezone' FROM openfox.physical_devices WHERE id=$1::uuid`, d.ID).Scan(&persisted); err != nil || persisted != zone {
			t.Fatalf("persisted=%q err=%v", persisted, err)
		}
		var detail struct {
			Timezone *string `json:"timezone"`
		}
		owner.must(http.StatusOK, http.MethodGet, "devices/"+d.ID, nil, &detail)
		if detail.Timezone == nil || *detail.Timezone != zone {
			t.Fatalf("timezone=%v want=%s", detail.Timezone, zone)
		}
	}
	legacy := e.deviceCall(first, http.MethodPost, "/v1/device/heartbeat", map[string]any{
		"center_epoch": first.CenterEpoch, "protocol_major": 1, "protocol_minor": 0, "daemon_version": "r12-legacy",
		"operating_system": "darwin", "architecture": "arm64", "capabilities": map[string]any{},
	})
	if legacy.Status != http.StatusOK {
		t.Fatalf("legacy heartbeat=%d %s", legacy.Status, legacy.Body)
	}
	var detail struct {
		Timezone *string `json:"timezone"`
	}
	owner.must(http.StatusOK, http.MethodGet, "devices/"+first.ID, nil, &detail)
	if detail.Timezone != nil {
		t.Fatalf("stale timezone=%s", *detail.Timezone)
	}
}
