package wp06

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"openfox/internal/agents"
	"openfox/internal/devices"
	"openfox/internal/platform/postgres"
	"openfox/internal/policy"
	"openfox/internal/tenancy"
)

const (
	wp06OrgA    = "00000000-0000-0000-0000-0000000000a1"
	wp06OrgB    = "00000000-0000-0000-0000-0000000000b1"
	wp06OwnerA  = "00000000-0000-0000-0000-000000000001"
	wp06MemberA = "00000000-0000-0000-0000-000000000004"
	wp06OwnerB  = "00000000-0000-0000-0000-000000000002"
	wp06SpaceA  = "00000000-0000-0000-0000-0000000000aa"
	wp06SpaceB  = "00000000-0000-0000-0000-0000000000bb"
	wp06DeviceA = "00000000-0000-0000-0000-000000000011"
)

func TestPostgresStoreAgentVersionsAuthorizationAndArchive(t *testing.T) {
	url := os.Getenv("OPENFOX_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("OPENFOX_TEST_DATABASE_URL is not configured")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	p, err := postgres.New(ctx, postgres.Config{
		URL: url, MaxConns: 4, ContextRole: "openfox_context", AppRole: "openfox_app",
		StatementTimeout: 3 * time.Second, LockTimeout: time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()

	ownerSelection := tenancy.OrganizationSelection{OrganizationID: wp06OrgA, ActorID: wp06OwnerA, AuthorizationVersion: 1}
	owner := agents.Actor{UserID: wp06OwnerA, Role: policy.OrganizationRoleOwner, AuthorizationVersion: 1, RequestID: "wp06-agent-create"}
	devicesList, err := (devices.PostgresStore{Pool: p}).List(ctx, ownerSelection)
	if err != nil {
		t.Fatal(err)
	}
	var device devices.Device
	for _, candidate := range devicesList {
		if candidate.ID == wp06DeviceA {
			device = candidate
			break
		}
	}
	if device.ID == "" || device.GrantID == "" {
		t.Fatalf("fixture device grant = %+v", device)
	}

	store := agents.PostgresStore{Pool: p}
	created, err := store.Create(ctx, ownerSelection, owner, wp06SpaceA, agents.CreateInput{
		Name: "WP06 Build Agent", Kind: agents.KindCode, Visibility: agents.VisibilityPrivate,
		Config: agents.ConfigInput{
			Instructions: "Run the bounded build workflow.", ComputerID: device.ID, GrantID: device.GrantID,
			Runtime: "codex", Model: "gpt-5", DirectoryMode: agents.DirectoryWorkspace,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if created.Config == nil || created.CurrentConfigVersion != 1 || created.Config.Version != 1 {
		t.Fatalf("created Agent = %+v", created)
	}

	listed, err := store.List(ctx, ownerSelection, owner, wp06SpaceA, agents.ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !containsAgent(listed, created.ID) {
		t.Fatalf("created Agent missing from list: %+v", listed)
	}

	updated, err := store.Update(ctx, ownerSelection, owner, wp06SpaceA, created.ID, agents.UpdateInput{
		Description: stringPtr("Updated description"), Version: created.Version,
	})
	if err != nil {
		t.Fatal(err)
	}
	if updated.Description != "Updated description" || updated.Version != created.Version+1 {
		t.Fatalf("updated Agent = %+v", updated)
	}
	if _, err := store.Update(ctx, ownerSelection, owner, wp06SpaceA, created.ID, agents.UpdateInput{
		Name: stringPtr("stale"), Version: created.Version,
	}); !errors.Is(err, agents.ErrVersionConflict) {
		t.Fatalf("stale update error = %v, want version conflict", err)
	}

	versioned, err := store.CreateConfig(ctx, ownerSelection, owner, wp06SpaceA, created.ID, updated.Version, agents.ConfigInput{
		Instructions: "Second immutable version.", ComputerID: device.ID, GrantID: device.GrantID,
		Runtime: "codex", Model: "gpt-5", MaxConcurrency: 2,
		QueueTimeoutSeconds: 7200, RunTimeoutSeconds: 1800, DirectoryMode: agents.DirectoryExplicit,
		DirectoryRoots: []string{"src", "tests"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if versioned.CurrentConfigVersion != 2 || versioned.Config == nil || versioned.Config.MaxConcurrency != 2 {
		t.Fatalf("versioned Agent = %+v", versioned)
	}
	configs, err := store.ListConfigs(ctx, ownerSelection, owner, wp06SpaceA, created.ID)
	if err != nil || len(configs) != 2 {
		t.Fatalf("config history = %#v, %v", configs, err)
	}

	archived, err := store.Update(ctx, ownerSelection, owner, wp06SpaceA, created.ID, agents.UpdateInput{
		Archived: boolPtr(true), Version: versioned.Version,
	})
	if err != nil || archived.ArchivedAt == nil {
		t.Fatalf("archive = %+v, %v", archived, err)
	}
	if _, err := store.CreateConfig(ctx, ownerSelection, owner, wp06SpaceA, created.ID, archived.Version, agents.ConfigInput{
		ComputerID: device.ID, GrantID: device.GrantID, Runtime: "codex", Model: "gpt-5",
	}); !errors.Is(err, agents.ErrArchived) {
		t.Fatalf("archived config error = %v, want archived", err)
	}

	memberSelection := tenancy.OrganizationSelection{OrganizationID: wp06OrgA, ActorID: wp06MemberA, AuthorizationVersion: 1}
	member := agents.Actor{UserID: wp06MemberA, Role: policy.OrganizationRoleMember, AuthorizationVersion: 1}
	if _, err := store.List(ctx, memberSelection, member, wp06SpaceA, agents.ListOptions{}); !errors.Is(err, agents.ErrNotFound) {
		t.Fatalf("private Agent visibility error = %v, want not found", err)
	}

	otherSelection := tenancy.OrganizationSelection{OrganizationID: wp06OrgB, ActorID: wp06OwnerB, AuthorizationVersion: 1}
	other := agents.Actor{UserID: wp06OwnerB, Role: policy.OrganizationRoleOwner, AuthorizationVersion: 1}
	if _, err := store.Get(ctx, otherSelection, other, wp06SpaceB, created.ID); !errors.Is(err, agents.ErrNotFound) {
		t.Fatalf("cross-tenant Agent read error = %v, want not found", err)
	}
	otherDevices, err := (devices.PostgresStore{Pool: p}).List(ctx, otherSelection)
	if err != nil || len(otherDevices) == 0 {
		t.Fatalf("other organization devices = %+v, %v", otherDevices, err)
	}
	if _, err := store.Create(ctx, ownerSelection, owner, wp06SpaceA, agents.CreateInput{
		Name: "Cross Tenant Device Agent", Kind: agents.KindCode, Visibility: agents.VisibilityShared,
		Config: agents.ConfigInput{ComputerID: otherDevices[0].ID, GrantID: otherDevices[0].GrantID, Runtime: "codex", Model: "gpt-5"},
	}); !errors.Is(err, agents.ErrInvalidConfig) {
		t.Fatalf("cross-tenant device error = %v, want invalid config", err)
	}
}

func containsAgent(items []agents.Agent, id string) bool {
	for _, item := range items {
		if item.ID == id {
			return true
		}
	}
	return false
}

func stringPtr(value string) *string { return &value }
func boolPtr(value bool) *bool       { return &value }
