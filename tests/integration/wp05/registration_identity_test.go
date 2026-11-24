package wp05

import (
	"crypto/ed25519"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"openfox/internal/api"
	"openfox/internal/daemon"
	"openfox/internal/deviceauth"
	"openfox/internal/devices"
	"openfox/internal/identity"
	"openfox/internal/tenancy"
)

func TestRegistrationSurvivesLostResponseAndRestart(t *testing.T) {
	ctx, pool := wp05Database(t)
	store := devices.PostgresStore{Pool: pool}
	auth := deviceauth.Service{Store: deviceauth.PostgresStore{Pool: pool}, Now: time.Now}
	mux := http.NewServeMux()

	api.RegisterDeviceManagementRoutes(mux, identity.Service{}, auth, store)
	handler := http.StripPrefix("/api", mux)
	var dropped atomic.Bool
	center := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/device/register" && dropped.CompareAndSwap(false, true) {
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, r)
			if response.Code != http.StatusCreated {
				t.Errorf("first exchange returned %d", response.Code)
			}
			http.Error(w, "response lost after commit", http.StatusBadGateway)
			return
		}
		handler.ServeHTTP(w, r)
	}))
	defer center.Close()
	auth.CenterAPIURL = daemon.APIBaseURL(center.URL)
	api.RegisterDeviceRoutes(mux, identity.Service{}, auth, pool)
	dir := t.TempDir()
	binary := filepath.Join(dir, "openfox")
	if output, err := exec.CommandContext(ctx, "go", "build", "-o", binary, "openfox/cmd/openfox").CombinedOutput(); err != nil {
		t.Fatalf("build CLI: %s: %v", output, err)
	}
	selection := tenancy.OrganizationSelection{OrganizationID: wp05OrgA, ActorID: wp05OwnerA, AuthorizationVersion: 1}
	name := t.Name()
	register := func(configDir string, force bool) error {
		t.Helper()
		issued, err := store.IssueRegistrationToken(ctx, selection, devices.RegistrationTokenInput{Name: name, Scope: devices.ScopePersonal}, time.Time{})
		if err != nil {
			t.Fatal(err)
		}
		token, err := devices.EncodeInstallToken(center.URL, issued.Token)
		if err != nil {
			t.Fatal(err)
		}
		args := []string{"computer", "register", "--no-service", "--config-dir", configDir, "--install-token", "-"}
		if force {
			args = append(args, "--force")
		}
		cmd := exec.CommandContext(ctx, binary, args...)
		cmd.Stdin = strings.NewReader(token + "\n")
		return cmd.Run()
	}
	configDir := filepath.Join(dir, "first")
	if err := register(configDir, false); err == nil {
		t.Fatal("expected lost response")
	}
	devicesNamed := func() []devices.Device {
		t.Helper()
		page, err := store.ListVisible(ctx, selection, devices.Viewer{Role: "OWNER"}, devices.ListOptions{})
		if err != nil {
			t.Fatal(err)
		}
		var found []devices.Device
		for _, d := range page.Devices {
			if d.Name == name {
				found = append(found, d)
			}
		}
		return found
	}
	first := devicesNamed()
	if len(first) != 1 {
		t.Fatalf("after lost response: %d records", len(first))
	}
	configFile := filepath.Join(configDir, daemon.DeviceConfigFileName)
	if _, err := os.Stat(configFile); !os.IsNotExist(err) {
		t.Fatalf("partial config: %v", err)
	}
	for _, force := range []bool{false, true} {
		if err := register(configDir, force); err != nil {
			t.Fatal(err)
		}
		cfg, err := daemon.LoadDeviceConfig(configFile)
		if err != nil || cfg.DeviceID != first[0].ID {
			t.Fatalf("retry changed identity: %v", err)
		}
		found := devicesNamed()
		if len(found) != 1 || found[0].GrantID != first[0].GrantID {
			t.Fatal("retry created a device or grant")
		}
	}
	if err := register(filepath.Join(dir, "second"), false); err != nil {
		t.Fatal(err)
	}
	if len(devicesNamed()) != 2 {
		t.Fatal("different installations with the same name were merged")
	}
}

func TestConcurrentRegistrationKeepsIdentityAndGrant(t *testing.T) {
	ctx, pool := wp05Database(t)
	store := devices.PostgresStore{Pool: pool}
	owner := tenancy.OrganizationSelection{OrganizationID: wp05OrgA, ActorID: wp05OwnerA, AuthorizationVersion: 1}
	public, _, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	issue := func(actor string) devices.RegistrationExchange {
		t.Helper()
		selection := owner
		selection.ActorID = actor
		issued, err := store.IssueRegistrationToken(ctx, selection, devices.RegistrationTokenInput{Name: t.Name(), Scope: devices.ScopePersonal}, time.Time{})
		if err != nil {
			t.Fatal(err)
		}
		return devices.RegistrationExchange{Token: issued.Token, KeyID: "primary", PublicKey: public}
	}
	type result struct {
		devices.RegistrationResult
		err error
	}
	start := make(chan struct{})
	results := make(chan result, 4)
	for range cap(results) {
		req := issue(owner.ActorID)
		go func() {
			<-start
			registration, err := store.RegisterWithToken(ctx, req)
			results <- result{registration, err}
		}()
	}
	close(start)
	var id string
	for range cap(results) {
		r := <-results
		if r.err != nil || (id != "" && id != r.DeviceID) {
			t.Fatalf("concurrent registration: %+v", r)
		}
		id = r.DeviceID
	}
	d, err := store.Get(ctx, owner, devices.Viewer{Role: "OWNER"}, id)
	if err != nil {
		t.Fatal(err)
	}
	updated, err := store.UpdateGrantScope(ctx, owner, id, devices.GrantScopeInput{Scope: devices.ScopeSpace, SpaceID: wp05SpaceA, ExpectedVersion: d.PermissionVersion})
	if err != nil {
		t.Fatal(err)
	}
	req := issue(owner.ActorID)
	r, err := store.RegisterWithToken(ctx, req)
	if err != nil || r.DeviceID != id {
		t.Fatalf("reconnect: %+v %v", r, err)
	}
	after, err := store.Get(ctx, owner, devices.Viewer{Role: "OWNER"}, id)
	if err != nil || after.GrantID != updated.GrantID || after.GrantScope != devices.ScopeSpace || after.PermissionVersion != updated.PermissionVersion {
		t.Fatal("reconnect changed existing grant")
	}
	if _, err = store.RegisterWithToken(ctx, req); err != devices.ErrRegistrationConflict {
		t.Fatalf("consumed token: %v", err)
	}
	if _, err = store.RegisterWithToken(ctx, issue("00000000-0000-0000-0000-000000000004")); err != devices.ErrRegistrationConflict {
		t.Fatalf("another owner: %v", err)
	}
	auth := deviceauth.PostgresStore{Pool: pool}
	if err := auth.RevokeGrant(ctx, owner, id, time.Now()); err != nil {
		t.Fatal(err)
	}
	if r, err := store.RegisterWithToken(ctx, issue(owner.ActorID)); err != nil || r.DeviceID != id {
		t.Fatalf("self-removed grant: %+v %v", r, err)
	}
	member := "00000000-0000-0000-0000-000000000004"
	memberKey, _, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	memberRequest := issue(member)
	memberRequest.PublicKey = memberKey
	memberDevice, err := store.RegisterWithToken(ctx, memberRequest)
	if err != nil {
		t.Fatal(err)
	}
	if err := auth.RevokeGrant(ctx, owner, memberDevice.DeviceID, time.Now()); err != nil {
		t.Fatal(err)
	}
	memberRequest = issue(member)
	memberRequest.PublicKey = memberKey
	if _, err := store.RegisterWithToken(ctx, memberRequest); err != devices.ErrRegistrationConflict {
		t.Fatalf("administrator removal: %v", err)
	}
	if err := auth.RevokeDevice(ctx, id, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := store.RegisterWithToken(ctx, issue(owner.ActorID)); err != devices.ErrRegistrationConflict {
		t.Fatalf("revoked device: %v", err)
	}
}
