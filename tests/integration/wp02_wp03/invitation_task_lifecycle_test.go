package wp02_wp03

import (
	"context"
	"net/http"
	"testing"
)

const assignmentSpace = "00000000-0000-0000-0000-0000000000aa"

// Only the existing per-test PostgreSQL clone is seeded here. All invitation transitions
// use the real HTTP handlers, tenant roles and in-memory test mailer.
type assignmentLifecycle struct {
	t               *testing.T
	ctx             context.Context
	h               *postgresHarness
	owner, target   *http.Client
	inviteID, token string
}

func newAssignmentLifecycle(t *testing.T, inviteToSpace bool) *assignmentLifecycle {
	t.Helper()
	ctx, fixture := newHarness(t)
	h, ok := fixture.(*postgresHarness)
	if !ok {
		t.Skip("requires the isolated PostgreSQL clone")
	}
	owner, err := h.Client(ctx, userBearerAuth(Owner))
	if err != nil {
		t.Fatal(err)
	}
	target, err := h.Client(ctx, userBearerAuth(InviteTarget))
	if err != nil {
		t.Fatal(err)
	}
	member := &assignmentLifecycle{t: t, ctx: ctx, h: h, owner: owner, target: target}
	body := map[string]any{"email": InviteTarget.Email, "role": "MEMBER"}
	if inviteToSpace {
		body["space_ids"] = []string{assignmentSpace}
	}
	created := requestJSON(t, owner, http.MethodPost, member.orgInvites(), body, nil)
	member.inviteID = requireID(t, assertStatus(t, created, http.StatusCreated), "id")
	member.token, err = h.InvitationToken(ctx, member.inviteID)
	if err != nil {
		t.Fatal(err)
	}
	return member
}
func (f *assignmentLifecycle) orgInvites() string {
	return route(f.h, "/organizations/"+OrganizationA+"/invites")
}
func (f *assignmentLifecycle) accept() *http.Response {
	return requestJSON(f.t, f.target, http.MethodPost, route(f.h, "/invites/"+f.inviteID+"/accept"), map[string]string{"token": f.token}, nil)
}
func (f *assignmentLifecycle) revoke() *http.Response {
	return requestJSON(f.t, f.owner, http.MethodPost, f.orgInvites()+"/"+f.inviteID+"/revoke", nil, nil)
}
func (f *assignmentLifecycle) exec(sql string, args ...any) {
	f.t.Helper()
	if _, err := f.h.seed.Exec(f.ctx, sql, args...); err != nil {
		f.t.Fatal(err)
	}
}
func (f *assignmentLifecycle) reserve(number int, legacyUser string) string {
	f.t.Helper()
	var task string
	err := f.h.seed.QueryRow(f.ctx, `INSERT INTO openfox.tasks(organization_id,space_id,title,type_id,status_id,sequence,assignee_id)
		SELECT w.organization_id,w.space_id,'Invitation lifecycle',w.task_type_id,w.status_id,$3,NULLIF($4,'')::uuid
		FROM openfox.workflow_statuses w WHERE w.organization_id=$1 AND w.space_id=$2 AND w.is_default
		ORDER BY w.task_type_id LIMIT 1 RETURNING id::text`, OrganizationA, assignmentSpace, number, legacyUser).Scan(&task)
	if err != nil {
		f.t.Fatal(err)
	}
	f.exec(`INSERT INTO openfox.task_invitation_assignees(organization_id,task_id,invitation_id,created_by) VALUES($1,$2,$3,$4)`, OrganizationA, task, f.inviteID, UserOwner)
	return task
}
func (f *assignmentLifecycle) want(sql, want string, args ...any) {
	f.t.Helper()
	var got string
	if err := f.h.seed.QueryRow(f.ctx, sql, args...).Scan(&got); err != nil {
		f.t.Fatal(err)
	}
	if got != want {
		f.t.Fatalf("got %q want %q for %s", got, want, sql)
	}
}
func (f *assignmentLifecycle) checkTerminal(status, reservations string) {
	f.t.Helper()
	f.want(`SELECT status FROM openfox.invitations WHERE id=$1`, status, f.inviteID)
	f.want(`SELECT count(*)::text FROM openfox.task_invitation_assignees WHERE invitation_id=$1`, reservations, f.inviteID)
}

func TestTaskInvitationAcceptanceProjectsMemberAndPreservesLegacyAssignees(t *testing.T) {
	f := newAssignmentLifecycle(t, true)
	// A revoked member can rejoin. Legacy tasks may still project them as their assignee.
	f.exec(`INSERT INTO openfox.organization_members(organization_id,user_id,role,status) VALUES ($1,$2,'MEMBER','REVOKED')`, OrganizationA, InviteTargetUser)
	empty := f.reserve(9100, "")
	legacy := f.reserve(9101, UserMember)
	duplicate := f.reserve(9102, InviteTargetUser)
	f.exec(`INSERT INTO openfox.task_assignees(organization_id,space_id,task_id,kind,user_id,created_by)
		VALUES ($1,$2,$3,'HUMAN',$4,$5)`, OrganizationA, assignmentSpace, duplicate, InviteTargetUser, UserOwner)
	assertStatus(t, f.accept(), http.StatusOK)
	assertError(t, f.accept(), http.StatusConflict, "INVITATION_CONFLICT")
	f.checkTerminal("ACCEPTED", "0")
	f.want(`SELECT count(*)::text FROM openfox.space_members WHERE organization_id=$1 AND space_id=$2 AND user_id=$3 AND status='ACTIVE'`, "1", OrganizationA, assignmentSpace, InviteTargetUser)
	f.want(`SELECT assignee_id::text||':'||version::text||':'||status FROM openfox.tasks WHERE id=$1`, InviteTargetUser+":2:TODO", empty)
	f.want(`SELECT assignee_id::text FROM openfox.tasks WHERE id=$1`, UserMember, legacy)
	f.want(`SELECT count(*)::text FROM openfox.task_assignees WHERE task_id=$1 AND kind='HUMAN'`, "2", legacy)
	f.want(`SELECT count(*)::text FROM openfox.task_assignees WHERE task_id=$1 AND user_id=$2`, "1", duplicate, InviteTargetUser)
	f.want(`SELECT count(*)::text FROM openfox.task_activities WHERE task_id=ANY($1::uuid[]) AND kind='task.assignee_added' AND safe_diff=jsonb_build_object('kind','HUMAN','id',$2::uuid)`, "2", []string{empty, legacy, duplicate}, InviteTargetUser)
	f.want(`SELECT count(*)::text FROM openfox.task_followers WHERE task_id=ANY($1::uuid[]) AND user_id=$2`, "2", []string{empty, legacy, duplicate}, InviteTargetUser)
}
func TestTaskInvitationLosingAccessNeverGrantsSpace(t *testing.T) {
	f := newAssignmentLifecycle(t, false)
	f.exec(`UPDATE openfox.spaces SET visibility='SHARED' WHERE id=$1`, assignmentSpace)
	task := f.reserve(9101, "")
	f.exec(`UPDATE openfox.spaces SET visibility='PRIVATE' WHERE id=$1`, assignmentSpace)
	assertStatus(t, f.accept(), http.StatusOK)
	f.checkTerminal("ACCEPTED", "0")
	f.want(`SELECT count(*)::text FROM openfox.space_members WHERE organization_id=$1 AND space_id=$2 AND user_id=$3`, "0", OrganizationA, assignmentSpace, InviteTargetUser)
	f.want(`SELECT count(*)::text FROM openfox.task_assignees WHERE task_id=$1`, "0", task)
	f.want(`SELECT count(*)::text FROM openfox.task_followers WHERE task_id=$1 AND user_id=$2`, "0", task, InviteTargetUser)
}
func TestTaskInvitationActiveMemberConflictPreservesReservation(t *testing.T) {
	f := newAssignmentLifecycle(t, true)
	f.reserve(9100, "")
	// They joined by another route after the email invitation was issued.
	f.exec(`INSERT INTO openfox.organization_members(organization_id,user_id,role,status) VALUES($1,$2,'GUEST','ACTIVE')`, OrganizationA, InviteTargetUser)
	assertError(t, f.accept(), http.StatusConflict, "INVITATION_CONFLICT")
	f.checkTerminal("PENDING", "1")
	f.want(`SELECT role FROM openfox.organization_members WHERE organization_id=$1 AND user_id=$2`, "GUEST", OrganizationA, InviteTargetUser)
	f.want(`SELECT count(*)::text FROM openfox.space_members WHERE space_id=$1 AND user_id=$2`, "0", assignmentSpace, InviteTargetUser)
}
func TestTaskInvitationRevocationAndLazyExpiry(t *testing.T) {
	for _, action := range []string{"revoke", "list-expired", "revoke-expired", "resend-expired", "replace-expired"} {
		t.Run(action, func(t *testing.T) {
			f := newAssignmentLifecycle(t, true)
			task := f.reserve(9100, UserOwner)
			status := "REVOKED"
			if action != "revoke" {
				if err := f.h.ExpireInvitation(f.ctx, f.inviteID); err != nil {
					t.Fatal(err)
				}
				assertError(t, f.accept(), http.StatusConflict, "INVITATION_CONFLICT")
				status = "EXPIRED"
			}
			switch action {
			case "revoke":
				assertStatus(t, f.revoke(), http.StatusOK)
				assertError(t, f.revoke(), http.StatusConflict, "INVITATION_CONFLICT")
			case "revoke-expired":
				assertError(t, f.revoke(), http.StatusConflict, "INVITATION_CONFLICT")
			case "resend-expired":
				assertError(t, requestJSON(t, f.owner, http.MethodPost, f.orgInvites()+"/"+f.inviteID+"/resend", nil, nil), http.StatusConflict, "INVITATION_CONFLICT")
			case "list-expired":
				assertStatus(t, requestJSON(t, f.owner, http.MethodGet, f.orgInvites(), nil, nil), http.StatusOK)
			case "replace-expired":
				assertStatus(t, requestJSON(t, f.owner, http.MethodPost, f.orgInvites(), map[string]string{"email": InviteTarget.Email, "role": "GUEST"}, nil), http.StatusCreated)
			}
			f.checkTerminal(status, "0")
			f.want(`SELECT assignee_id::text||':'||version::text FROM openfox.tasks WHERE id=$1`, UserOwner+":2", task)
			f.want(`SELECT count(*)::text FROM openfox.task_followers WHERE task_id=$1 AND user_id=$2`, "0", task, InviteTargetUser)
		})
	}
}

func TestTaskInvitationBatchExpiryUpdatesTaskOnce(t *testing.T) {
	f := newAssignmentLifecycle(t, true)
	task := f.reserve(9100, "")
	created := requestJSON(t, f.owner, http.MethodPost, f.orgInvites(), map[string]string{
		"email": ConcurrentInviteTarget.Email, "role": "MEMBER",
	}, nil)
	other := requireID(t, assertStatus(t, created, http.StatusCreated), "id")
	f.exec(`INSERT INTO openfox.task_invitation_assignees(organization_id,task_id,invitation_id,created_by)
		VALUES($1,$2,$3,$4)`, OrganizationA, task, other, UserOwner)
	for _, id := range []string{f.inviteID, other} {
		if err := f.h.ExpireInvitation(f.ctx, id); err != nil {
			t.Fatal(err)
		}
	}
	assertStatus(t, requestJSON(t, f.owner, http.MethodGet, f.orgInvites(), nil, nil), http.StatusOK)
	f.want(`SELECT count(*)::text FROM openfox.invitations WHERE id=ANY($1::uuid[]) AND status='EXPIRED'`, "2", []string{f.inviteID, other})
	f.want(`SELECT count(*)::text FROM openfox.task_invitation_assignees WHERE task_id=$1`, "0", task)
	f.want(`SELECT version::text FROM openfox.tasks WHERE id=$1`, "2", task)
}

func TestTaskInvitationConcurrentAcceptAndRevoke(t *testing.T) {
	f := newAssignmentLifecycle(t, true)
	task := f.reserve(9100, "")
	results := concurrentResponses(t, f.accept, f.revoke)
	if countStatuses(results, http.StatusOK) != 1 || countStatuses(results, http.StatusConflict) != 1 {
		t.Fatalf("accept/revoke responses: %d %d", results[0].StatusCode, results[1].StatusCode)
	}
	for _, response := range results {
		responseBody(t, response)
	}
	f.want(`SELECT count(*)::text FROM openfox.task_invitation_assignees WHERE invitation_id=$1`, "0", f.inviteID)
	f.want(`SELECT (CASE WHEN i.status='ACCEPTED' THEN t.assignee_id=$3::uuid ELSE i.status='REVOKED' AND t.assignee_id IS NULL END)::text FROM openfox.invitations i,openfox.tasks t WHERE i.id=$1 AND t.id=$2`, "true", f.inviteID, task, InviteTargetUser)
	f.want(`SELECT version::text FROM openfox.tasks WHERE id=$1`, "2", task)
}
func TestTaskInvitationFailureRollsBackAcceptance(t *testing.T) {
	f := newAssignmentLifecycle(t, true)
	task := f.reserve(9100, "")
	// Fail exactly the insert introduced by acceptance, inside this disposable database.
	f.exec(`ALTER TABLE openfox.task_assignees ADD CONSTRAINT test_reject_assignment CHECK (kind <> 'HUMAN') NOT VALID`)
	assertStatus(t, f.accept(), http.StatusInternalServerError)
	f.checkTerminal("PENDING", "1")
	f.want(`SELECT count(*)::text FROM openfox.organization_members WHERE organization_id=$1 AND user_id=$2`, "0", OrganizationA, InviteTargetUser)
	f.want(`SELECT count(*)::text FROM openfox.space_members WHERE space_id=$1 AND user_id=$2`, "0", assignmentSpace, InviteTargetUser)
	f.want(`SELECT version::text FROM openfox.tasks WHERE id=$1`, "1", task)
	f.exec(`ALTER TABLE openfox.task_assignees DROP CONSTRAINT test_reject_assignment`)
	assertStatus(t, f.accept(), http.StatusOK)
	f.checkTerminal("ACCEPTED", "0")
}
