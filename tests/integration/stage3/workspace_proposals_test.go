package stage3

import (
	"context"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"openfox/internal/artifactreview"
	"openfox/internal/artifacts"
	"openfox/internal/conversations"
	"openfox/internal/docs"
	"openfox/internal/membership"
	"openfox/internal/tenancy"
)

type proposalEnv struct {
	e                 *s3Env
	storage           artifacts.Local
	run, conversation string
}

func newProposalEnv(t *testing.T) *proposalEnv {
	e := newS3Env(t)
	root := t.TempDir()
	withAvatarStorage(e, root)
	agent := e.insertAgent(e.space.ID)
	e.configureAgent(agent)
	run := e.insertRun(e.space.ID, agent, "", "RUNNING")
	e.exec(`UPDATE openfox.runs SET state='SUCCEEDED',finished_at=clock_timestamp() WHERE id=$1`, run)
	var conv string
	e.scalar(&conv, `SELECT conversation_id::text FROM openfox.runs WHERE id=$1`, run)
	return &proposalEnv{e: e, storage: artifacts.Local{Root: root}, run: run, conversation: conv}
}
func (p *proposalEnv) tx(fn func(context.Context, pgx.Tx) error) error {
	return p.e.database.RunForOrganization(p.e.ctx, p.e.org, tenancy.SourceUserRequest, tenancy.OrganizationSelectorFunc(func(context.Context, string) (tenancy.OrganizationSelection, error) {
		return tenancy.OrganizationSelection{OrganizationID: p.e.org, ActorID: s3OwnerID, AuthorizationVersion: 1}, nil
	}), fn)
}
func (p *proposalEnv) record(source artifactreview.Input, body string) artifactreview.Proposal {
	e := p.e
	id := s3NewID(e.t)
	key := e.org + "/" + p.run + "/" + id
	stored, err := p.storage.Put(e.ctx, key, strings.NewReader(body), 10<<20)
	if err != nil {
		e.t.Fatal(err)
	}
	var out artifactreview.Proposal
	err = p.tx(func(ctx context.Context, tx pgx.Tx) error {
		svc := artifactreview.Service{}
		member, err := (membership.Service{}).LoadActiveActor(ctx, tx, e.org, s3OwnerID)
		if err != nil {
			return err
		}
		f, err := svc.CommentFile(ctx, tx, e.org, p.conversation, conversations.Actor{UserID: s3OwnerID, Role: member.Role, AuthorizationVersion: member.AuthorizationVersion}, source)
		if err != nil {
			return err
		}
		source.ConversationID = p.conversation
		out, err = svc.RecordProposal(ctx, tx, artifactreview.Proposal{ID: id, RunID: p.run, ConversationID: p.conversation, Source: source, Base: f, StorageKey: key, SizeBytes: stored.Size, SHA256: stored.SHA256, RequestHash: artifactreview.Digest(id), CreatedBy: s3OwnerID, Summary: "review candidate"}, 50<<20)
		return err
	})
	if err != nil {
		e.t.Fatal(err)
	}
	return out
}
func (p *proposalEnv) path(id string) string {
	return "conversations/" + p.conversation + "/proposals/" + id
}
func TestWorkspaceProposalNativeAcceptanceAndConflict(t *testing.T) {
	h := newProposalEnv(t)
	e := h.e
	owner := e.owner()
	node := e.docNodeIn(owner, e.space.ID, docPersonal(), "", docs.KindDoc, "候选文档")
	e.exec(`UPDATE openfox.doc_nodes SET body='original',version=version+1 WHERE id=$1`, node.ID)
	var version int64
	e.scalar(&version, `SELECT version FROM openfox.doc_nodes WHERE id=$1`, node.ID)
	source := artifactreview.Input{ID: s3NewID(t), SpaceID: e.space.ID, NodeID: node.ID, NodeVersion: version, SHA256: artifactreview.Digest("original")}
	a, b := h.record(source, "candidate A"), h.record(source, "candidate B")
	var body string
	e.scalar(&body, `SELECT body FROM openfox.doc_nodes WHERE id=$1`, node.ID)
	if body != "original" {
		t.Fatal("candidate changed original")
	}
	if r := owner.do("GET", h.path(a.ID)+"/content?side=base", nil); r.Status != 200 || string(r.Body) != "original" {
		t.Fatalf("immutable base: %+v", r)
	}
	// Current permission is required even though creation was authorized earlier.
	e.admin().must(404, "GET", h.path(a.ID), nil, nil)
	if r := owner.doIn(e.otherOrg, "GET", h.path(a.ID), nil); r.Status < 400 {
		t.Fatal("tenant leak")
	}
	owner.must(404, "GET", "conversations/"+s3NewID(t)+"/proposals/"+a.ID, nil, nil)
	decisions := []artifactreview.ProposalDecision{{OperationID: s3NewID(t), Version: 1, Action: "accept"}, {OperationID: s3NewID(t), Version: 1, Action: "accept"}}
	ps := []artifactreview.Proposal{a, b}
	results := parallel(2, func(i int) int { return owner.do("POST", h.path(ps[i].ID)+"/decision", decisions[i]).Status })
	if countStatus(results, 200) != 1 || countStatus(results, 409) != 1 {
		t.Fatal("concurrent native acceptance", results)
	}
	winner, loser := 0, 1
	if results[1] == 200 {
		winner, loser = 1, 0
	}
	var accepted artifactreview.Proposal
	owner.must(200, "POST", h.path(ps[winner].ID)+"/decision", decisions[winner], &accepted)
	if accepted.Status != "ACCEPTED" || accepted.ResultNodeVersion != version+1 {
		t.Fatalf("bad receipt %+v", accepted)
	}
	owner.must(409, "POST", h.path(ps[winner].ID)+"/decision", artifactreview.ProposalDecision{OperationID: decisions[winner].OperationID, Version: 1, Action: "reject"}, nil)
	owner.must(200, "POST", h.path(ps[loser].ID)+"/decision", artifactreview.ProposalDecision{OperationID: s3NewID(t), Version: 1, Action: "reject"}, nil)
	if e.count(`SELECT count(*) FROM openfox.audit_events WHERE organization_id=$1 AND action='workspace.proposal.accept'`, e.org) != 1 {
		t.Fatal("duplicate audit")
	}
	if r := owner.do("GET", h.path(a.ID)+"/content?side=base", nil); r.Status != 200 || string(r.Body) != "original" {
		t.Fatal("baseline followed edits")
	}
	// Immutable storage columns cannot be changed using the application role.
	if err := h.tx(func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE openfox.workspace_proposals SET sha256=$2 WHERE id=$1`, a.ID, strings.Repeat("0", 64))
		return err
	}); err == nil {
		t.Fatal("candidate metadata mutable")
	}
	// Decision receipts survive missing candidate bytes, and the replay never reapplies.
	if err := h.storage.Delete(e.ctx, ps[winner].StorageKey); err != nil {
		t.Fatal(err)
	}
	owner.must(200, "POST", h.path(ps[winner].ID)+"/decision", decisions[winner], nil)
}
func TestWorkspaceProposalArtifactRestoreAndPermissions(t *testing.T) {
	h := newProposalEnv(t)
	e := h.e
	owner := e.owner()
	id := s3NewID(t)
	key := e.org + "/" + h.run + "/" + id
	stored, err := h.storage.Put(e.ctx, key, strings.NewReader("original"), 10<<20)
	if err != nil {
		t.Fatal(err)
	}
	e.exec(`INSERT INTO openfox.artifacts(organization_id,id,run_id,name,storage_key,size_bytes,sha256,mime_type) VALUES($1,$2,$3,'test.md',$4,$5,$6,'text/markdown')`, e.org, id, h.run, key, stored.Size, stored.SHA256)
	source := artifactreview.Input{ID: s3NewID(t), ArtifactID: id, SHA256: stored.SHA256}
	a, b := h.record(source, "candidate A"), h.record(source, "candidate B")
	if e.count(`SELECT count(*) FROM openfox.artifacts WHERE run_id=$1`, h.run) != 1 {
		t.Fatal("published before acceptance")
	}
	// Viewer can preview but not decide; removing shared access revokes both.
	e.exec(`UPDATE openfox.conversations SET visibility='SHARED' WHERE id=$1`, h.conversation)
	e.addSpaceMember(e.space.ID, s3AdminID, "MEMBER")
	e.exec(`INSERT INTO openfox.conversation_members(organization_id,conversation_id,user_id) VALUES($1,$2,$3)`, e.org, h.conversation, s3AdminID)
	e.admin().must(200, "GET", h.path(a.ID), nil, nil)
	d := artifactreview.ProposalDecision{OperationID: s3NewID(t), Version: 1, Action: "accept"}
	e.admin().must(403, "POST", h.path(a.ID)+"/decision", d, nil)
	e.exec(`DELETE FROM openfox.conversation_members WHERE conversation_id=$1 AND user_id=$2`, h.conversation, s3AdminID)
	e.admin().must(404, "GET", h.path(a.ID)+"/content?side=candidate", nil, nil)
	// A corrupted candidate must fail before publishing anything.
	if err := h.storage.Delete(e.ctx, a.StorageKey); err != nil {
		t.Fatal(err)
	}
	if _, err := h.storage.Put(e.ctx, a.StorageKey, strings.NewReader("corrupted"), 10<<20); err != nil {
		t.Fatal(err)
	}
	owner.must(410, "POST", h.path(a.ID)+"/decision", d, nil)
	if e.count(`SELECT count(*) FROM openfox.artifacts WHERE run_id=$1`, h.run) != 1 {
		t.Fatal("corrupt candidate applied")
	}
	if err := h.storage.Delete(e.ctx, a.StorageKey); err != nil {
		t.Fatal(err)
	}
	if _, err := h.storage.Put(e.ctx, a.StorageKey, strings.NewReader("candidate A"), 10<<20); err != nil {
		t.Fatal(err)
	}
	e.exec(`INSERT INTO openfox.conversation_members(organization_id,conversation_id,user_id,access_level) VALUES($1,$2,$3,'COMMENTER')`, e.org, h.conversation, s3AdminID)
	clients := []*s3Client{owner, e.admin()}
	ps := []artifactreview.Proposal{a, b}
	ds := []artifactreview.ProposalDecision{d, {OperationID: s3NewID(t), Version: 1, Action: "accept"}}
	results := parallel(2, func(i int) int { return clients[i].do("POST", h.path(ps[i].ID)+"/decision", ds[i]).Status })
	if countStatus(results, 200) != 1 || countStatus(results, 409) != 1 {
		t.Fatal("concurrent artifact acceptance", results)
	}
	winner := 0
	if results[1] == 200 {
		winner = 1
	}
	clients[winner].must(200, "POST", h.path(ps[winner].ID)+"/decision", ds[winner], nil)
	if e.count(`SELECT count(*) FROM openfox.artifacts WHERE run_id=$1`, h.run) != 2 {
		t.Fatal("duplicate published revision")
	}
	// Historical restoration is itself a candidate; accepting creates revision 3.
	restore := s3NewID(t)
	request := map[string]string{"source_artifact_id": id, "base_artifact_id": ps[winner].ID}
	var p artifactreview.Proposal
	owner.must(200, "PUT", h.path(restore)+"/restore", request, &p)
	owner.must(200, "PUT", h.path(restore)+"/restore", request, nil)
	if e.count(`SELECT count(*) FROM openfox.artifacts WHERE run_id=$1`, h.run) != 2 {
		t.Fatal("restore bypassed review")
	}
	owner.must(200, "POST", h.path(restore)+"/decision", artifactreview.ProposalDecision{OperationID: s3NewID(t), Version: 1, Action: "accept"}, nil)
	var revision int
	e.scalar(&revision, `SELECT content_revision FROM openfox.artifacts WHERE id=$1`, restore)
	if revision != 3 {
		t.Fatal("history was rewound", revision)
	}
	request["base_artifact_id"] = restore
	owner.must(409, "PUT", h.path(p.ID)+"/restore", request, nil)
	var page artifactreview.ProposalPage
	owner.must(200, "GET", "conversations/"+h.conversation+"/proposals", nil, &page)
	if len(page.Proposals) != 3 {
		t.Fatal("proposal listing lost records")
	}
}
