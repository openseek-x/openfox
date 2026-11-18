package home

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"openfox/internal/agents"
	"openfox/internal/devices"
	"openfox/internal/platform/postgres"
	"openfox/internal/policy"
	"openfox/internal/tenancy"
)

// 「全部 Agent 和小队」：独立的成员与 SHARED 空间，避免影响其他首页测试。
const (
	adMember      = "00000000-0000-0000-0000-000000000f01"
	adGuest       = "00000000-0000-0000-0000-000000000f03"
	adSharedSpace = "00000000-0000-0000-0000-000000000f10"
	adOrgB        = "00000000-0000-0000-0000-0000000000b1"
	adOwnerB      = "00000000-0000-0000-0000-000000000002"
	adDevice      = "00000000-0000-0000-0000-000000000011"
)

func TestPostgresHomeAgentDirectory(t *testing.T) {
	url := os.Getenv("OPENFOX_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("OPENFOX_TEST_DATABASE_URL is not configured")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	seed, err := pgx.Connect(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	for _, statement := range []struct {
		query string
		args  []any
	}{
		{`INSERT INTO openfox.users (id, email) VALUES ($1::uuid, 'mia@example.test'), ($2::uuid, 'gil@example.test')`, []any{adMember, adGuest}},
		{`INSERT INTO openfox.organization_members (organization_id, user_id, role) VALUES ($1::uuid, $2::uuid, 'MEMBER'), ($1::uuid, $3::uuid, 'GUEST')`, []any{homeOrgA, adMember, adGuest}},
		{`INSERT INTO openfox.spaces (organization_id, id, name, visibility, prefix) VALUES ($1::uuid, $2::uuid, 'Directory shared', 'SHARED', 'dirs')`, []any{homeOrgA, adSharedSpace}},
		{`INSERT INTO openfox.space_members (organization_id, space_id, user_id, role, status) VALUES ($1::uuid, $2::uuid, $3::uuid, 'OWNER', 'ACTIVE')`, []any{homeOrgA, adSharedSpace, adMember}},
	} {
		if _, err := seed.Exec(ctx, statement.query, statement.args...); err != nil {
			_ = seed.Close(ctx)
			t.Fatal(err)
		}
	}
	if err := seed.Close(ctx); err != nil {
		t.Fatal(err)
	}
	pool, err := postgres.New(ctx, postgres.Config{
		URL: url, MaxConns: 4, ContextRole: "openfox_context", AppRole: "openfox_app",
		StatementTimeout: 3 * time.Second, LockTimeout: time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	store := agents.PostgresStore{Pool: pool}
	type who struct {
		selection tenancy.OrganizationSelection
		actor     agents.Actor
	}
	as := func(org, user string, role policy.OrganizationRole) who {
		return who{
			selection: tenancy.OrganizationSelection{OrganizationID: org, ActorID: user, AuthorizationVersion: 1},
			actor:     agents.Actor{UserID: user, Role: role, AuthorizationVersion: 1, RequestID: "home-agent-directory"},
		}
	}
	alice := as(homeOrgA, homeOwnerA, policy.OrganizationRoleOwner)
	mia := as(homeOrgA, adMember, policy.OrganizationRoleMember)
	guest := as(homeOrgA, adGuest, policy.OrganizationRoleGuest)
	bob := as(adOrgB, adOwnerB, policy.OrganizationRoleOwner)

	deviceList, err := (devices.PostgresStore{Pool: pool}).List(ctx, alice.selection)
	if err != nil {
		t.Fatal(err)
	}
	grantID := ""
	for _, device := range deviceList {
		if device.ID == adDevice {
			grantID = device.GrantID
		}
	}
	if grantID == "" {
		t.Fatal("fixture device grant not found")
	}
	create := func(w who, space, name string, visibility agents.Visibility) agents.Agent {
		t.Helper()
		agent, err := store.Create(ctx, w.selection, w.actor, space, agents.CreateInput{
			Name: name, Kind: agents.KindCode, Visibility: visibility,
			Config: agents.ConfigInput{ComputerID: adDevice, GrantID: grantID, Runtime: "codex", Model: "gpt-5"},
		})
		if err != nil {
			t.Fatalf("create %s: %v", name, err)
		}
		return agent
	}
	aliceShared := create(alice, homeSpaceA, "Dir alice shared", agents.VisibilityShared)
	alicePrivate := create(alice, homeSpaceA, "Dir alice private", agents.VisibilityPrivate)
	miaShared := create(mia, adSharedSpace, "Dir mia shared", agents.VisibilityShared)
	miaPrivate := create(mia, adSharedSpace, "Dir mia private", agents.VisibilityPrivate)

	directory := func(w who, archived bool) agents.Directory {
		t.Helper()
		result, err := store.Directory(ctx, w.selection, w.actor, agents.DirectoryOptions{Archived: archived})
		if err != nil {
			t.Fatal(err)
		}
		return result
	}
	byID := func(result agents.Directory) map[string]agents.DirectoryEntry {
		out := map[string]agents.DirectoryEntry{}
		for _, entry := range result.Entries {
			out[entry.ID] = entry
		}
		return out
	}

	// 1. 可见性：自己的私有 Agent、可读空间里的共享 Agent；看不到他人的私有 Agent 与不可读的私有空间。
	aliceView := byID(directory(alice, false))
	for _, id := range []string{aliceShared.ID, alicePrivate.ID, miaShared.ID} {
		if _, ok := aliceView[id]; !ok {
			t.Fatalf("alice directory misses %s: %+v", id, aliceView)
		}
	}
	if _, ok := aliceView[miaPrivate.ID]; ok {
		t.Fatal("alice sees mia's private Agent")
	}
	if entry := aliceView[miaShared.ID]; entry.SpaceName != "Directory shared" || entry.Mine || entry.OwnerEmail != "mia@example.test" || entry.Model == nil || *entry.Model != "gpt-5" {
		t.Fatalf("mia shared entry = %+v", entry)
	}
	if !aliceView[alicePrivate.ID].Mine {
		t.Fatalf("alice private entry = %+v", aliceView[alicePrivate.ID])
	}
	miaView := byID(directory(mia, false))
	if _, ok := miaView[aliceShared.ID]; ok {
		t.Fatal("mia sees an Agent in alice's private space")
	}
	if _, ok := miaView[miaPrivate.ID]; !ok {
		t.Fatal("mia misses her own private Agent")
	}
	if entries := directory(guest, false).Entries; len(entries) != 0 {
		t.Fatalf("guest directory = %+v", entries)
	}
	for _, entry := range directory(bob, false).Entries {
		if _, leaked := aliceView[entry.ID]; leaked {
			t.Fatalf("organization B sees organization A entry %+v", entry)
		}
	}

	// 2. 小队：成员必须是同一空间、可见、未归档的 Agent；名称在空间内唯一。
	crew, err := store.CreateCrew(ctx, alice.selection, alice.actor, homeSpaceA, agents.CrewInput{
		Name: "Dir crew", Description: "build + review", AgentIDs: []string{aliceShared.ID, alicePrivate.ID},
	})
	if err != nil {
		t.Fatal(err)
	}
	if crew.Type != agents.DirectoryTypeCrew || crew.Visibility != agents.VisibilityShared || len(crew.Members) != 2 || crew.Members[0].ID != aliceShared.ID || !crew.Mine {
		t.Fatalf("crew = %+v", crew)
	}
	for name, input := range map[string]agents.CrewInput{
		"other space": {Name: "Bad crew", AgentIDs: []string{miaShared.ID}},
		"empty":       {Name: "Bad crew"},
		"duplicate":   {Name: "Bad crew", AgentIDs: []string{aliceShared.ID, aliceShared.ID}},
		"blank name":  {Name: "  ", AgentIDs: []string{aliceShared.ID}},
	} {
		if _, err := store.CreateCrew(ctx, alice.selection, alice.actor, homeSpaceA, input); !errors.Is(err, agents.ErrInvalidRequest) {
			t.Fatalf("%s crew error = %v", name, err)
		}
	}
	if _, err := store.CreateCrew(ctx, alice.selection, alice.actor, homeSpaceA, agents.CrewInput{Name: "dir CREW", AgentIDs: []string{aliceShared.ID}}); !errors.Is(err, agents.ErrNameConflict) {
		t.Fatalf("duplicate crew name error = %v", err)
	}
	// 不是空间管理员（普通成员、不在该私有空间）不能建小队。
	if _, err := store.CreateCrew(ctx, mia.selection, mia.actor, homeSpaceA, agents.CrewInput{Name: "Mia in private", AgentIDs: []string{aliceShared.ID}}); !errors.Is(err, agents.ErrForbidden) {
		t.Fatalf("non-manager crew error = %v", err)
	}
	miaCrew, err := store.CreateCrew(ctx, mia.selection, mia.actor, adSharedSpace, agents.CrewInput{
		Name: "Mia crew", AgentIDs: []string{miaShared.ID, miaPrivate.ID},
	})
	if err != nil {
		t.Fatal(err)
	}
	// alice 能看到 mia 的共享小队，但成员里只有她可见的 Agent。
	if entry := byID(directory(alice, false))[miaCrew.ID]; len(entry.Members) != 1 || entry.Members[0].ID != miaShared.ID {
		t.Fatalf("alice view of mia crew = %+v", entry)
	}

	// 3. 置顶：个人偏好、幂等，不可见目标按不存在处理。
	for _, target := range []agents.PinTarget{{AgentID: aliceShared.ID}, {AgentID: aliceShared.ID}, {CrewID: miaCrew.ID}} {
		if err := store.SetPin(ctx, alice.selection, alice.actor, target, true); err != nil {
			t.Fatalf("pin %+v: %v", target, err)
		}
	}
	if err := store.SetPin(ctx, mia.selection, mia.actor, agents.PinTarget{AgentID: miaShared.ID}, true); err != nil {
		t.Fatal(err)
	}
	aliceView = byID(directory(alice, false))
	if !aliceView[aliceShared.ID].Pinned || !aliceView[miaCrew.ID].Pinned || aliceView[miaShared.ID].Pinned {
		t.Fatalf("alice pins = %+v", aliceView)
	}
	if !byID(directory(mia, false))[miaShared.ID].Pinned {
		t.Fatal("mia pin missing")
	}
	if err := store.SetPin(ctx, alice.selection, alice.actor, agents.PinTarget{AgentID: miaPrivate.ID}, true); !errors.Is(err, agents.ErrNotFound) {
		t.Fatalf("pin invisible Agent error = %v", err)
	}
	for _, target := range []agents.PinTarget{{}, {AgentID: aliceShared.ID, CrewID: crew.ID}} {
		if err := store.SetPin(ctx, alice.selection, alice.actor, target, true); !errors.Is(err, agents.ErrInvalidRequest) {
			t.Fatalf("pin %+v error = %v", target, err)
		}
	}
	if err := store.SetPin(ctx, alice.selection, alice.actor, agents.PinTarget{AgentID: aliceShared.ID}, false); err != nil {
		t.Fatal(err)
	}
	if byID(directory(alice, false))[aliceShared.ID].Pinned {
		t.Fatal("unpin did not take effect")
	}

	// 4. 归档：活跃视图排除并计数，归档视图列出；归档目标只能取消置顶。
	archived := true
	if _, err := store.Update(ctx, alice.selection, alice.actor, homeSpaceA, alicePrivate.ID, agents.UpdateInput{Archived: &archived, Version: alicePrivate.Version}); err != nil {
		t.Fatal(err)
	}
	active := directory(alice, false)
	if _, ok := byID(active)[alicePrivate.ID]; ok || active.ArchivedCount != 1 {
		t.Fatalf("active after archive = %d entries, archived %d", len(active.Entries), active.ArchivedCount)
	}
	if entry := byID(active)[crew.ID]; len(entry.Members) != 1 {
		t.Fatalf("crew members after archive = %+v", entry.Members)
	}
	if list := directory(alice, true); len(list.Entries) != 1 || list.Entries[0].ID != alicePrivate.ID || list.Entries[0].ArchivedAt == nil {
		t.Fatalf("archived directory = %+v", list.Entries)
	}
	if err := store.SetPin(ctx, alice.selection, alice.actor, agents.PinTarget{AgentID: alicePrivate.ID}, true); !errors.Is(err, agents.ErrArchived) {
		t.Fatalf("pin archived error = %v", err)
	}
	if err := store.SetPin(ctx, alice.selection, alice.actor, agents.PinTarget{AgentID: alicePrivate.ID}, false); err != nil {
		t.Fatalf("unpin archived error = %v", err)
	}

	// 5. 小队归档 / 恢复：乐观锁版本。
	archivedCrew, err := store.UpdateCrew(ctx, alice.selection, alice.actor, homeSpaceA, crew.ID, agents.CrewPatch{Archived: &archived, Version: crew.Version})
	if err != nil || archivedCrew.ArchivedAt == nil || archivedCrew.Version != crew.Version+1 {
		t.Fatalf("archive crew = %+v, %v", archivedCrew, err)
	}
	if _, err := store.UpdateCrew(ctx, alice.selection, alice.actor, homeSpaceA, crew.ID, agents.CrewPatch{Archived: &archived, Version: crew.Version}); !errors.Is(err, agents.ErrVersionConflict) {
		t.Fatalf("stale crew update error = %v", err)
	}
	if directory(alice, false).ArchivedCount != 2 {
		t.Fatal("archived count should include the crew")
	}
	restore := false
	restored, err := store.UpdateCrew(ctx, alice.selection, alice.actor, homeSpaceA, crew.ID, agents.CrewPatch{Archived: &restore, Version: archivedCrew.Version})
	if err != nil || restored.ArchivedAt != nil {
		t.Fatalf("restore crew = %+v, %v", restored, err)
	}
	if _, err := store.UpdateCrew(ctx, bob.selection, bob.actor, homeSpaceA, crew.ID, agents.CrewPatch{Archived: &archived, Version: restored.Version}); !errors.Is(err, agents.ErrNotFound) {
		t.Fatalf("cross-tenant crew update error = %v", err)
	}
}
