package stage3

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"openfox/internal/tenancy"
)

func TestTaskInvitationAssignees(t *testing.T) {
	env := newS3Env(t)
	owner, admin, editor := env.owner(), env.admin(), env.member()
	space := env.space.ID
	env.addSpaceMember(space, s3AdminID, "MEMBER")
	// Seed only the invitation. Exercise task operations through the authenticated API.
	invite := func(org, creator, email, role string, spaceIDs []string) string {
		id := s3NewID(t)
		env.exec(`INSERT INTO openfox.invitations(organization_id,id,created_by,normalized_email,role,token_hash,space_ids)
			VALUES($1,$2,$3,$4,$5,decode(md5($2::uuid::text)||md5($2::uuid::text),'hex'),coalesce($6::uuid[],'{}'))`, org, id, creator, email, role, spaceIDs)
		return id
	}
	outsiderEmail := s3Email(s3OutsiderID)
	i := invite(env.org, s3OwnerID, outsiderEmail, "GUEST", []string{space})
	body := func(id string) map[string]string { return map[string]string{"kind": "INVITATION", "id": id} }
	type detail struct {
		ID        string
		Version   int
		Assignees []struct{ Kind, ID, Name string }
	}
	path := spacePath(space) + "/tasks"
	var task detail
	owner.must(201, "POST", path, map[string]any{"title": "邀请验收", "assignees": []any{body(i)}}, &task)
	item := path + "/" + task.ID
	assertVisible := func(client *s3Client, visible bool, want int) detail {
		t.Helper()
		var res detail
		client.must(200, "GET", item, nil, &res)
		found := false
		for _, ref := range res.Assignees {
			if ref.Kind == "INVITATION" {
				found = ref.ID == i && ref.Name == outsiderEmail
				if !found {
					t.Fatalf("bad invitation = %+v", ref)
				}
			}
		}
		if found != visible || len(res.Assignees) != want {
			t.Fatalf("expected visible %v, total %d: %+v", visible, want, res)
		}
		return res
	}
	assertVisible(admin, true, 1)
	assertVisible(editor, false, 0)
	if resp := env.outsider().do("GET", item, nil); resp.Status != 404 {
		t.Fatalf("outside invitee gained access: %d", resp.Status)
	}
	searchPath := "search?types=task&offset=0&assignee=invitation:"
	var search searchResultView
	admin.must(200, "GET", searchPath+i, nil, &search)
	if *search.Groups[0].Total != 1 || !search.has("task", task.ID) {
		t.Fatalf("admin filter = %+v", search)
	}
	for _, id := range []string{i, s3NewID(t)} {
		for _, query := range []string{searchPath + id, path + "?assignee_not=invitation:" + id} {
			editor.expectError(403, "ACTION_FORBIDDEN", "GET", query, nil)
		}
		editor.expectError(403, "ACTION_FORBIDDEN", "PATCH", item, map[string]any{"version": task.Version, "assignees": []any{body(id)}})
		editor.expectError(403, "ACTION_FORBIDDEN", "POST", path, map[string]any{"title": "禁止", "assignees": []any{body(id)}})
	}
	editor.must(200, "PATCH", item, map[string]any{"version": task.Version, "assignees": []any{map[string]string{"kind": "HUMAN", "id": s3MemberID}}}, nil)
	assertVisible(editor, false, 1)
	current := assertVisible(admin, true, 2)
	// Private names / IDs must also be absent in task activity and saved shared filters.
	for _, url := range []string{item + "/activities", path + "?assignee=user:" + s3MemberID} {
		resp := editor.must(200, "GET", url, nil, nil)
		if strings.Contains(string(resp.Body), i) || strings.Contains(string(resp.Body), outsiderEmail) {
			t.Fatalf("private invitation leaked on %s", url)
		}
	}
	owner.expectError(400, "INVALID_VIEW_FILTERS", "POST", spacePath(space)+"/views", map[string]any{"name": "bad", "visibility": "space", "filters": map[string]any{"assignee": "invitation:" + i}})
	// The physical cap still reserves one slot, even for someone who cannot see it.
	var full []map[string]string
	for n := 0; n < 10; n++ {
		full = append(full, map[string]string{"kind": "AGENT", "id": env.insertAgent(space)})
	}
	editor.expectError(400, "TOO_MANY_ASSIGNEES", "PATCH", item, map[string]any{"version": current.Version, "assignees": full})
	assertVisible(owner, true, 2)
	// The same reservation counts for old write paths that insert normalized rows
	// directly. A multirow insert/ON CONFLICT cannot bypass the combined cap.
	editor.must(200, "PATCH", item, map[string]any{"version": current.Version, "assignees": full[:9]}, nil)
	current = assertVisible(owner, true, 10)
	selection := tenancy.OrganizationSelection{OrganizationID: env.org, ActorID: s3MemberID, AuthorizationVersion: 1}
	err := env.database.RunForOrganization(env.ctx, env.org, tenancy.SourceUserRequest, tenancy.OrganizationSelectorFunc(func(context.Context, string) (tenancy.OrganizationSelection, error) { return selection, nil }), func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO openfox.task_assignees(organization_id,space_id,task_id,kind,user_id,created_by)
			VALUES($1,$2,$3,'HUMAN',$4,$4) ON CONFLICT DO NOTHING`, env.org, space, task.ID, s3MemberID)
		return err
	})
	var dbErr *pgconn.PgError
	if !errors.As(err, &dbErr) || dbErr.ConstraintName != "task_assignees_reserved_capacity" {
		t.Fatalf("direct write bypassed reservation: %v", err)
	}
	// Lock order is inverse during acceptance. A concurrent explicit edit returns
	// version conflict and can retry instead of blocking while holding the task.
	lockTx, err := env.superuser.Begin(env.ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer lockTx.Rollback(context.Background())
	if _, err = lockTx.Exec(env.ctx, `SELECT 1 FROM openfox.invitations WHERE id=$1 FOR UPDATE`, i); err != nil {
		t.Fatal(err)
	}
	admin.expectError(409, "VERSION_CONFLICT", "PATCH", item, map[string]any{"version": current.Version, "assignees": []any{}})
	if err = lockTx.Rollback(env.ctx); err != nil {
		t.Fatal(err)
	}
	admin.must(200, "PATCH", item, map[string]any{"version": current.Version, "assignees": []any{}}, nil)
	assertVisible(owner, false, 0)
	// Invalid, cross-tenant, expired, and ineligible-space additions all give the same domain error.
	unscoped := invite(env.org, s3OwnerID, "no-space@example.test", "GUEST", nil)
	foreign := invite(env.otherOrg, s3OutsiderID, "foreign-space@example.test", "MEMBER", []string{env.otherSpace.ID})
	expired := invite(env.org, s3OwnerID, "expired-space@example.test", "GUEST", []string{space})
	env.exec(`UPDATE openfox.invitations SET issued_at=now()-interval '3 day',expires_at=now()-interval '1 day' WHERE id=$1`, expired)
	for _, id := range []string{unscoped, foreign, expired, s3NewID(t)} {
		owner.expectError(400, "ASSIGNEE_NOT_ALLOWED", "POST", path, map[string]any{"title": "拒绝", "assignees": []any{body(id)}})
	}
	s := env.createSpace("shared")
	target := spacePath(s.ID) + "/tasks"
	shared := invite(env.org, s3OwnerID, "shared-target@example.test", "MEMBER", nil)
	var expiring detail
	owner.must(201, "POST", target, map[string]any{"title": "共享空间允许成员", "assignees": []any{body(shared)}}, &expiring)
	// Wall-clock expiry precedes lazy list cleanup. Search and detail already
	// exclude this reservation while its stored status is still PENDING.
	env.exec(`UPDATE openfox.invitations SET issued_at=now()-interval '3 day', expires_at=now()-interval '1 day' WHERE id=$1`, shared)
	owner.must(200, "GET", target+"/"+expiring.ID, nil, &expiring)
	if len(expiring.Assignees) != 0 {
		t.Fatalf("expired reservation still visible: %+v", expiring.Assignees)
	}
	if r := env.search(owner, "", "types=task", "offset=0", "assignee=invitation:"+shared); *r.Groups[0].Total != 0 {
		t.Fatalf("expired reservation matched: %+v", r)
	}

	owner.expectError(400, "ASSIGNEE_NOT_ALLOWED", "POST", target, map[string]any{"title": "访客未获准", "assignees": []any{body(unscoped)}})
}
