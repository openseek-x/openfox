package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"openfox/internal/deviceauth"
	"openfox/internal/devices"
	"openfox/internal/identity"
	"openfox/internal/tenancy"
)

type managementStore struct {
	issued     devices.RegistrationTokenResult
	registered devices.RegistrationResult
	heartbeat  devices.HeartbeatResult
}

func (s *managementStore) IssueRegistrationToken(context.Context, tenancy.OrganizationSelection, devices.RegistrationTokenInput, time.Time) (devices.RegistrationTokenResult, error) {
	return s.issued, nil
}

func (s *managementStore) RegisterWithToken(context.Context, devices.RegistrationExchange) (devices.RegistrationResult, error) {
	return s.registered, nil
}

func (s *managementStore) List(context.Context, tenancy.OrganizationSelection) ([]devices.Device, error) {
	return []devices.Device{}, nil
}

func (s *managementStore) UpdateGrantScope(context.Context, tenancy.OrganizationSelection, string, devices.GrantScopeInput) (devices.Device, error) {
	return devices.Device{}, nil
}

func (s *managementStore) RotateKey(context.Context, tenancy.OrganizationSelection, string, devices.KeyRotationInput, time.Time) (devices.KeyRotationResult, error) {
	return devices.KeyRotationResult{}, nil
}

func (s *managementStore) RecordHeartbeat(context.Context, deviceauth.Principal, devices.Heartbeat, time.Time) (devices.HeartbeatResult, error) {
	return s.heartbeat, nil
}

func (s *managementStore) CenterEpoch(context.Context) (int64, error) { return 1, nil }

func (s *managementStore) ListVisible(context.Context, tenancy.OrganizationSelection, devices.Viewer, devices.ListOptions) (devices.Page, error) {
	return devices.Page{Devices: []devices.Device{}}, nil
}
func (s *managementStore) Get(context.Context, tenancy.OrganizationSelection, devices.Viewer, string) (devices.DeviceDetail, error) {
	return devices.DeviceDetail{}, nil
}
func (s *managementStore) UpdateSettings(context.Context, tenancy.OrganizationSelection, devices.Viewer, string, devices.SettingsInput) (devices.Device, error) {
	return devices.Device{}, nil
}
func (s *managementStore) RemovalBlockers(context.Context, tenancy.OrganizationSelection, devices.Viewer, string) (devices.RemovalBlockers, error) {
	return devices.RemovalBlockers{}, nil
}
func (s *managementStore) Remove(context.Context, tenancy.OrganizationSelection, devices.Viewer, string, time.Time) error {
	return nil
}
func (s *managementStore) RemoveFromOrganization(context.Context, tenancy.OrganizationSelection, devices.Viewer, string) (devices.RemovalResult, error) {
	return devices.RemovalResult{}, nil
}
func (s *managementStore) UnbindAgents(context.Context, tenancy.OrganizationSelection, devices.Viewer, string, []string) (devices.RemovalResult, error) {
	return devices.RemovalResult{}, nil
}
func (s *managementStore) ReplaceDevice(context.Context, tenancy.OrganizationSelection, devices.Viewer, string, string) (devices.ReplacementResult, error) {
	return devices.ReplacementResult{}, nil
}

type managementDeviceAuthStore struct{}

func (managementDeviceAuthStore) CreateChallenge(context.Context, deviceauth.Challenge) error {
	return nil
}

func (managementDeviceAuthStore) ConsumeChallenge(context.Context, deviceauth.VerifyRequest, deviceauth.PendingSession, time.Time, deviceauth.SignatureVerifier) (deviceauth.Session, error) {
	return deviceauth.Session{}, nil
}

func (managementDeviceAuthStore) AuthenticateSession(context.Context, []byte, time.Time) (deviceauth.Session, error) {
	return deviceauth.Session{
		ID: "00000000-0000-0000-0000-000000000001", DeviceID: testDeviceID,
		KeyID: "primary", DeviceGeneration: 1, KeyGeneration: 1,
		ExpiresAt: time.Now().Add(time.Hour),
	}, nil
}

func (managementDeviceAuthStore) SelectOrganization(context.Context, string, string) (tenancy.OrganizationSelection, error) {
	return tenancy.OrganizationSelection{}, nil
}

func (managementDeviceAuthStore) RegisterDevice(context.Context, deviceauth.Registration) (string, error) {
	return testDeviceID, nil
}

func (managementDeviceAuthStore) RevokeGrant(context.Context, tenancy.OrganizationSelection, string, time.Time) error {
	return nil
}

func (managementDeviceAuthStore) RevokeDevice(context.Context, string, time.Time) error { return nil }

func (managementDeviceAuthStore) RevokeKey(context.Context, string, string, time.Time) error {
	return nil
}

func newManagementHandler(store devices.Store) *DeviceManagementHandler {
	selection := tenancy.OrganizationSelection{OrganizationID: testOrgID, ActorID: testUserID, AuthorizationVersion: 1}
	userAuth := identity.Service{
		Store:           &apiIdentityStore{selection: selection, role: "OWNER"},
		SessionIdle:     time.Hour,
		SessionAbsolute: time.Hour,
		Now:             time.Now,
	}
	return &DeviceManagementHandler{
		userAuth:   userAuth,
		deviceAuth: deviceauth.Service{Store: managementDeviceAuthStore{}, Now: time.Now},
		store:      store,
	}
}

func TestRegistrationTokenRouteReturnsOneTimeTokenAtIssuanceBoundary(t *testing.T) {
	store := &managementStore{issued: devices.RegistrationTokenResult{
		ID: "00000000-0000-0000-0000-000000000099", Token: "one-time-token",
		ExpiresAt: time.Now().Add(10 * time.Minute),
	}}
	handler := newManagementHandler(store)
	mux := http.NewServeMux()
	RegisterDeviceManagementRoutes(mux, handler.userAuth, handler.deviceAuth, handler.store)
	request := httptest.NewRequest(http.MethodPost, "/v1/organizations/"+testOrgID+"/devices/registration-tokens", strings.NewReader(`{"name":"Mac"}`))
	request.Header.Set("Authorization", "Bearer user-session")
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusCreated {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	var response registrationTokenResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.Token != "one-time-token" {
		t.Fatalf("token = %q", response.Token)
	}
}

func TestDeviceHeartbeatRequiresDeviceBearerAndReturnsReceiptFacts(t *testing.T) {
	store := &managementStore{heartbeat: devices.HeartbeatResult{
		DeviceID: testDeviceID, CenterEpoch: 1, Version: 2,
		ReceivedAt: time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC),
	}}
	handler := newManagementHandler(store)
	mux := http.NewServeMux()
	RegisterDeviceManagementRoutes(mux, handler.userAuth, handler.deviceAuth, handler.store)
	request := httptest.NewRequest(http.MethodPost, "/v1/device/heartbeat", strings.NewReader(`{"center_epoch":1,"protocol_major":1,"protocol_minor":0,"capabilities":{"runtime":"unknown"}}`))
	request.Header.Set("Authorization", "Bearer device-session")
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	var response deviceHeartbeatResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.DeviceID != testDeviceID || response.Version != 2 {
		t.Fatalf("heartbeat response = %+v", response)
	}
}
