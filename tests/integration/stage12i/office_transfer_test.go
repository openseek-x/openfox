package stage12i

import (
	"bytes"
	"encoding/json"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"openfox/internal/artifactreview"
	"openfox/internal/artifacts"
	"openfox/internal/daemon"
	"openfox/internal/dispatch"
	"openfox/internal/docs"
	"openfox/internal/office"
)

// Actual device HTTP API and temporary PostgreSQL/storage; no model required.
func TestOfficeFileTransferBoundaries(t *testing.T) {
	s := newStack(t, "", 2*time.Minute)
	otherOrg := s.seed()
	s.startServer()
	d := s.registerDevice("office-transfer")
	space, agent := s.createAgent(d)
	conversation := s.createConversation(space, agent)
	grant := s.text(`SELECT id::text FROM openfox.device_grants WHERE organization_id=$1::uuid AND device_id=$2::uuid`, s.org, d.ID)
	sourceRun, run, artifact, message := newID(t), newID(t), newID(t), newID(t)
	for _, id := range []string{sourceRun, run} {
		state := "SUCCEEDED"
		if id == run {
			state = "RUNNING"
		}
		s.exec(`INSERT INTO openfox.runs(organization_id,id,space_id,conversation_id,agent_id,computer_id,grant_id,config_version,grant_version,actor_id,state,generation,finished_at,lease_expires_at,runtime,model,approval_mode,reasoning_effort) VALUES($1::uuid,$2::uuid,$3::uuid,$4::uuid,$5::uuid,$6::uuid,$7::uuid,1,1,$8::uuid,$9,1,CASE WHEN $9='SUCCEEDED' THEN clock_timestamp() ELSE NULL END,clock_timestamp()+interval '10 minutes','codex','default','WORKSPACE_WRITE','DEFAULT')`, s.org, id, space, conversation, agent, d.ID, grant, drillOwnerID, state)
	}
	raw, err := os.ReadFile("../../../internal/office/testdata/sample.docx")
	if err != nil {
		t.Fatal(err)
	}
	p, err := office.Read(raw, office.DOCX)
	if err != nil {
		t.Fatal(err)
	}
	u := p.View().Units[0]
	updated, err := p.Replace([]office.Change{{ID: u.ID, Expected: u.Text, Replacement: "Updated"}})
	if err != nil {
		t.Fatal(err)
	}
	key := s.org + "/" + sourceRun + "/" + artifact
	stored, err := (artifacts.Local{Root: s.storage}).Put(s.ctx, key, bytes.NewReader(raw), office.MaxBytes)
	if err != nil {
		t.Fatal(err)
	}
	s.exec(`INSERT INTO openfox.artifacts(organization_id,id,run_id,name,storage_key,size_bytes,sha256,mime_type,retention_until) VALUES($1::uuid,$2::uuid,$3::uuid,'source.docx',$4,$5,$6,$7,clock_timestamp()+interval '1 day')`, s.org, artifact, sourceRun, key, stored.Size, stored.SHA256, office.DOCX)
	s.exec(`INSERT INTO openfox.messages(organization_id,id,conversation_id,seq,author_kind,author_user_id,body) VALUES($1::uuid,$2::uuid,$3::uuid,1,'USER',$4::uuid,'change selected text')`, s.org, message, conversation, drillOwnerID)
	s.exec(`INSERT INTO openfox.conversation_inputs(organization_id,id,conversation_id,message_id,body,status,run_id,assigned_at) VALUES($1::uuid,gen_random_uuid(),$2::uuid,$3::uuid,'change selected text','ASSIGNED',$4::uuid,clock_timestamp())`, s.org, conversation, message, run)
	input := artifactreview.Input{ID: newID(t), ArtifactID: artifact, SHA256: stored.SHA256, Comments: []artifactreview.Snapshot{{ThreadID: newID(t), Anchor: artifactreview.Anchor{Kind: "office", Target: u.ID, Quote: u.Text}, Body: "Change text"}}}
	setSnapshot := func(in artifactreview.Input) {
		b, _ := json.Marshal(in)
		s.exec(`INSERT INTO openfox.message_file_contexts(organization_id,id,message_id,source_artifact_id,input_snapshot,payload_hash) VALUES($1::uuid,$2::uuid,$3::uuid,$4::uuid,$5::jsonb,$6) ON CONFLICT(organization_id,id) DO UPDATE SET input_snapshot=EXCLUDED.input_snapshot`, s.org, in.ID, message, artifact, b, artifactreview.Digest(string(b)))
	}
	setSnapshot(input)
	config, err := daemon.LoadDeviceConfig(d.ConfigPath)
	if err != nil {
		t.Fatal(err)
	}
	token, err := os.ReadFile(config.TokenFile)
	if err != nil {
		t.Fatal(err)
	}
	c := &client{s: s, http: &http.Client{Timeout: 20 * time.Second}}
	call := func(org, runID, tool string, generation int64, args any) (int, []byte) {
		return c.doWith(http.MethodPost, "/api/v1/device/organizations/"+org+"/runs/"+runID+"/tools/"+tool, map[string]any{"generation": generation, "call_id": "office-transfer", "arguments": args}, "Authorization", "Bearer "+strings.TrimSpace(string(token)))
	}
	args := map[string]any{"context_id": input.ID}
	replace := map[string]any{"context_id": input.ID, "sha256": stored.SHA256, "summary": "Update selected text", "content_base64": updated}
	expect := func(want int, tool string, args any) []byte {
		t.Helper()
		status, body := call(s.org, run, tool, 1, args)
		if status != want {
			t.Fatalf("%s: status %d want %d: %.500s", tool, status, want, body)
		}
		return body
	}
	// Source bytes only reach the authenticated owning device.
	body := expect(200, "file_comment_export", args)
	var result dispatch.ToolResult
	var source struct {
		Content []byte `json:"content_base64"`
	}
	if json.Unmarshal(body, &result) != nil || json.Unmarshal([]byte(result.Text), &source) != nil || !bytes.Equal(source.Content, raw) {
		t.Fatal("export mismatch")
	}
	for _, target := range []struct {
		org, run   string
		generation int64
		status     int
	}{{otherOrg, run, 1, 404}, {s.org, sourceRun, 1, 409}, {s.org, run, 2, 409}} {
		status, _ := call(target.org, target.run, "file_comment_export", target.generation, args)
		if status != target.status {
			t.Fatalf("authorization status %d want %d", status, target.status)
		}
	}
	expect(404, "file_comment_export", map[string]any{"context_id": newID(t)})
	s.exec(`UPDATE openfox.runs SET lease_expires_at=clock_timestamp()-interval '1 second' WHERE organization_id=$1::uuid AND id=$2::uuid`, s.org, run)
	expect(409, "file_comment_export", args)
	expect(409, "file_comment_replace", replace)
	s.exec(`UPDATE openfox.runs SET lease_expires_at=clock_timestamp()+interval '10 minutes' WHERE organization_id=$1::uuid AND id=$2::uuid`, s.org, run)
	s.exec(`INSERT INTO openfox.agent_config_versions(organization_id,agent_id,config_version,computer_id,grant_id,runtime,model,created_by,approval_mode,collaboration_mode) SELECT organization_id,agent_id,2,computer_id,grant_id,runtime,model,created_by,'WORKSPACE_WRITE','PLAN' FROM openfox.agent_config_versions WHERE organization_id=$1::uuid AND agent_id=$2::uuid AND config_version=1`, s.org, agent)
	for _, mode := range []string{"READ_ONLY", "PLAN"} {
		if mode == "READ_ONLY" {
			s.exec(`UPDATE openfox.runs SET approval_mode='READ_ONLY' WHERE organization_id=$1::uuid AND id=$2::uuid`, s.org, run)
		} else {
			s.exec(`UPDATE openfox.runs SET config_version=2 WHERE organization_id=$1::uuid AND id=$2::uuid`, s.org, run)
		}
		expect(409, "file_comment_export", args)
		expect(409, "file_comment_replace", replace)
		s.exec(`UPDATE openfox.runs SET approval_mode='WORKSPACE_WRITE' WHERE organization_id=$1::uuid AND id=$2::uuid`, s.org, run)
		s.exec(`UPDATE openfox.runs SET config_version=1 WHERE organization_id=$1::uuid AND id=$2::uuid`, s.org, run)
	}
	bad := input
	bad.SHA256 = strings.Repeat("0", 64)
	setSnapshot(bad)
	expect(409, "file_comment_export", args)
	expect(409, "file_comment_replace", replace)
	setSnapshot(input)
	bad = input
	bad.Comments = append([]artifactreview.Snapshot(nil), input.Comments...)
	bad.Comments[0].Anchor.Quote = "missing"
	setSnapshot(bad)
	expect(400, "file_comment_replace", replace)
	setSnapshot(input)
	replace["content_base64"] = []byte("not an Office file")
	expect(400, "file_comment_replace", replace)
	replace["content_base64"] = raw
	expect(400, "file_comment_replace", replace)
	replace["content_base64"] = updated
	replace["sha256"] = strings.Repeat("0", 64)
	expect(400, "file_comment_replace", replace)
	replace["sha256"] = stored.SHA256
	// Concurrent identical requests store one candidate; original/head are unchanged.
	type reply struct {
		status int
		body   []byte
	}
	replies := make(chan reply, 2)
	for range 2 {
		go func() {
			status, body := call(s.org, run, "file_comment_replace", 1, replace)
			replies <- reply{status, body}
		}()
	}
	var resultID string
	for range 2 {
		r := <-replies
		if r.status != 200 {
			t.Fatalf("concurrent publish %d: %.500s", r.status, r.body)
		}
		var payload struct {
			ProposalID string `json:"proposal_id"`
		}
		if json.Unmarshal(r.body, &result) != nil || json.Unmarshal([]byte(result.Text), &payload) != nil || payload.ProposalID == "" {
			t.Fatal("missing result")
		}
		if resultID != "" && resultID != payload.ProposalID {
			t.Fatal("duplicate artifact")
		}
		resultID = payload.ProposalID
	}
	expect(200, "file_comment_replace", replace)
	replace["summary"] = "different request"
	body = expect(200, "file_comment_replace", replace)
	var alternate struct {
		ProposalID string `json:"proposal_id"`
	}
	if json.Unmarshal(body, &result) != nil || json.Unmarshal([]byte(result.Text), &alternate) != nil {
		t.Fatal("alternate candidate missing")
	}
	s.expect("0", `SELECT count(*)::text FROM openfox.artifacts WHERE organization_id=$1::uuid AND run_id=$2::uuid`, s.org, run)
	s.expect("0", `SELECT count(*)::text FROM openfox.message_file_contexts WHERE organization_id=$1::uuid AND id=$2::uuid AND result_hash IS NOT NULL`, s.org, input.ID)
	path := "conversations/" + conversation + "/proposals/" + resultID
	for _, side := range []string{"base", "candidate"} {
		status, data := s.owner().do(http.MethodGet, path+"/content?side="+side, nil)
		expected := raw
		if side == "candidate" {
			expected = updated
		}
		if status != 200 || !bytes.Equal(data, expected) {
			t.Fatalf("preview bytes %s: %d", side, status)
		}
		status, _ = s.owner().do(http.MethodGet, path+"/office?side="+side, nil)
		if status != 200 {
			t.Fatalf("office projection %d", status)
		}
	}
	// A normal upload cannot occupy a pending proposal's identity.
	statusReserved, reservedBody := c.doWith(http.MethodPost, "/api/v1/device/organizations/"+s.org+"/runs/"+run+"/artifacts", nil, "Authorization", "Bearer "+strings.TrimSpace(string(token)), "X-Run-Generation", "1", "X-Artifact-ID", resultID)
	if statusReserved != 409 {
		t.Fatalf("reserved proposal ID upload %d: %s", statusReserved, reservedBody)
	}
	// Exact text and native-document tools also produce candidates without writing back.
	for _, native := range []bool{false, true} {
		in := artifactreview.Input{ID: newID(t), SHA256: artifactreview.Digest("original"), Comments: []artifactreview.Snapshot{{ThreadID: newID(t), Body: "Change text", Anchor: artifactreview.Anchor{Kind: "text", Start: 0, End: 8, Quote: "original"}}}}
		if native {
			var node docs.NodeDetail
			s.owner().must(201, http.MethodPost, "spaces/"+space+"/docs", map[string]any{"container": map[string]string{"kind": "personal"}, "kind": "doc", "title": "Native candidate", "body": "original"}, &node)
			in.SpaceID, in.NodeID, in.NodeVersion = space, node.ID, int64(node.Version)
		} else {
			in.ArtifactID = newID(t)
			textKey := s.org + "/" + sourceRun + "/" + in.ArtifactID
			textStored, e := (artifacts.Local{Root: s.storage}).Put(s.ctx, textKey, strings.NewReader("original"), office.MaxBytes)
			if e != nil {
				t.Fatal(e)
			}
			s.exec(`INSERT INTO openfox.artifacts(organization_id,id,run_id,name,storage_key,size_bytes,sha256,mime_type) VALUES($1::uuid,$2::uuid,$3::uuid,'text.md',$4,$5,$6,'text/markdown')`, s.org, in.ArtifactID, sourceRun, textKey, textStored.Size, textStored.SHA256)
		}
		snapshot, _ := json.Marshal(in)
		s.exec(`INSERT INTO openfox.message_file_contexts(organization_id,id,message_id,source_artifact_id,input_snapshot,payload_hash) VALUES($1::uuid,$2::uuid,$3::uuid,NULLIF($4,'')::uuid,$5::jsonb,$6)`, s.org, in.ID, message, in.ArtifactID, snapshot, artifactreview.Digest(string(snapshot)))
		output := expect(200, "file_comment_apply", map[string]any{"context_id": in.ID, "sha256": in.SHA256, "summary": "Review exact edit", "edits": []artifactreview.Edit{{ThreadID: in.Comments[0].ThreadID, Expected: "original", Replacement: "candidate"}}})
		var payload struct {
			ProposalID string `json:"proposal_id"`
			Applied    bool   `json:"applied"`
		}
		if json.Unmarshal(output, &result) != nil || json.Unmarshal([]byte(result.Text), &payload) != nil || payload.ProposalID == "" || payload.Applied {
			t.Fatal("exact edit bypassed review")
		}
		if native {
			s.expect("original", `SELECT body FROM openfox.doc_nodes WHERE organization_id=$1::uuid AND id=$2::uuid`, s.org, in.NodeID)
		}
		status, candidate := s.owner().do(http.MethodGet, "conversations/"+conversation+"/proposals/"+payload.ProposalID+"/content?side=candidate", nil)
		if status != 200 || string(candidate) != "candidate" {
			t.Fatalf("exact candidate %d: %s", status, candidate)
		}
		s.owner().must(200, http.MethodPost, "conversations/"+conversation+"/proposals/"+payload.ProposalID+"/decision", artifactreview.ProposalDecision{OperationID: newID(t), Version: 1, Action: "reject"}, nil)
	}

	// Human rejection neither applies changes nor resolves the source context.
	decision := artifactreview.ProposalDecision{OperationID: newID(t), Version: 1, Action: "reject"}
	status, data := s.owner().do(http.MethodPost, "conversations/"+conversation+"/proposals/"+alternate.ProposalID+"/decision", decision)
	if status != 200 {
		t.Fatalf("reject %d: %s", status, data)
	}
	decision = artifactreview.ProposalDecision{OperationID: newID(t), Version: 1, Action: "accept"}
	// Decisions remain valid after the model run has ended.
	s.exec(`UPDATE openfox.runs SET state='SUCCEEDED',finished_at=clock_timestamp() WHERE organization_id=$1::uuid AND id=$2::uuid`, s.org, run)
	for range 2 {
		status, data = s.owner().do(http.MethodPost, path+"/decision", decision)
		if status != 200 {
			t.Fatalf("accept/retry %d: %s", status, data)
		}
	}
	s.expect("1", `SELECT count(*)::text FROM openfox.artifacts WHERE organization_id=$1::uuid AND run_id=$2::uuid`, s.org, run)
	files := 0
	if err := filepath.WalkDir(filepath.Join(s.storage, s.org, run), func(_ string, d fs.DirEntry, e error) error {
		if e != nil {
			return e
		}
		if !d.IsDir() {
			files++
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if files != 4 {
		t.Fatalf("orphaned storage objects: %d", files)
	}
	status, original := s.owner().do(http.MethodGet, "runs/"+sourceRun+"/artifacts/"+artifact, nil)
	if status != 200 || !bytes.Equal(original, raw) {
		t.Fatal("original changed")
	}
	// Another context referring to the replaced original cannot overwrite it.
	stale := input
	stale.ID = newID(t)
	setSnapshot(stale)
	expect(409, "file_comment_export", map[string]any{"context_id": stale.ID})
	t.Log("live lease/tenant/context, read-only/plan, stale SHA/anchor, malformed file, concurrent retry and original preservation passed")
}

// Real-model fixtures also require an explicit user decision before expecting changes.
func (s *stack) acceptRunProposal(conversation, run string) {
	s.t.Helper()
	id := s.text(`SELECT id::text FROM openfox.workspace_proposals WHERE organization_id=$1::uuid AND run_id=$2::uuid AND status='PENDING' ORDER BY created_at DESC LIMIT 1`, s.org, run)
	if id == "" {
		s.t.Fatal("model did not produce a review candidate")
	}
	s.expect("0", `SELECT count(*)::text FROM openfox.message_file_contexts f JOIN openfox.conversation_inputs i ON i.organization_id=f.organization_id AND i.message_id=f.message_id WHERE f.organization_id=$1::uuid AND i.run_id=$2::uuid AND f.result_hash IS NOT NULL`, s.org, run)
	s.owner().must(200, http.MethodPost, "conversations/"+conversation+"/proposals/"+id+"/decision", artifactreview.ProposalDecision{OperationID: newID(s.t), Version: 1, Action: "accept"}, nil)
}
