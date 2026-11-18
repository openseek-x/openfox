package stage3

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5"
	"openfox/internal/artifacts"
	"openfox/internal/deviceauth"
	"openfox/internal/executiondelivery"
	"openfox/internal/tenancy"
)

func TestArtifactContentVersions(t *testing.T) {
	e := newS3Env(t)
	agent := e.insertAgent(e.space.ID)
	e.configureAgent(agent)
	run := e.insertRun(e.space.ID, agent, "", "RUNNING")
	var conversation, device string
	e.scalar(&conversation, `SELECT conversation_id::text FROM openfox.runs WHERE id=$1`, run)
	e.scalar(&device, `SELECT computer_id::text FROM openfox.runs WHERE id=$1`, run)
	service := executiondelivery.Service{Pool: e.database}
	publish := func(id, parent string) (executiondelivery.Artifact, error) {
		var result executiondelivery.Artifact
		selection := tenancy.OrganizationSelection{OrganizationID: e.org, ActorID: device, AuthorizationVersion: 1}
		err := e.database.RunForOrganization(e.ctx, e.org, tenancy.SourceDeviceRequest,
			tenancy.OrganizationSelectorFunc(func(context.Context, string) (tenancy.OrganizationSelection, error) { return selection, nil }),
			func(ctx context.Context, tx pgx.Tx) error {
				var err error
				stored := artifacts.Stored{Size: 4, SHA256: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}
				if parent == "" {
					result, _, err = service.AddArtifact(ctx, tx, e.org, deviceauth.Principal{DeviceID: device}, run, 1, id, "report.md", e.org+"/"+id, "text/markdown", stored, nil)
				} else {
					result, _, err = service.AddArtifactRevision(ctx, tx, e.org, deviceauth.Principal{DeviceID: device}, run, 1, id, "report.md", e.org+"/"+id, "text/markdown", stored, nil, parent)
				}
				return err
			})
		return result, err
	}
	first, err := publish(s3NewID(t), "")
	if err != nil || first.ContentRevision != 1 || first.HeadArtifactID != first.ID {
		t.Fatalf("first: %+v %v", first, err)
	}
	// Equal bytes are still a distinct explicit revision. Replays are coordinated
	// by the existing review context, not global hash deduplication.
	second, err := publish(s3NewID(t), first.ID)
	if err != nil || second.ID == first.ID || second.ContentID != first.ContentID || second.ContentRevision != 2 || second.HeadArtifactID != second.ID {
		t.Fatalf("second: %+v %v", second, err)
	}
	ids := []string{s3NewID(t), s3NewID(t)}
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for _, id := range ids {
		wg.Add(1)
		go func(id string) { defer wg.Done(); _, err := publish(id, second.ID); results <- err }(id)
	}
	wg.Wait()
	close(results)
	successes, conflicts := 0, 0
	for err := range results {
		if err == nil {
			successes++
		} else if errors.Is(err, executiondelivery.ErrResultConflict) {
			conflicts++
		} else {
			t.Fatal(err)
		}
	}
	if successes != 1 || conflicts != 1 {
		t.Fatalf("concurrent results: %d success, %d conflict", successes, conflicts)
	}
	path := "conversations/" + conversation + "/artifact-preview"
	var fixed, head executiondelivery.Artifact
	e.owner().must(200, http.MethodGet, path+"?artifact_id="+first.ID, nil, &fixed)
	e.owner().must(200, http.MethodGet, path+"?content_id="+first.ContentID, nil, &head)
	if fixed.ID != first.ID || fixed.ContentRevision != 1 || head.ContentRevision != 3 || fixed.HeadArtifactID != head.ID {
		t.Fatalf("fixed/head: %+v %+v", fixed, head)
	}
	if _, err := e.superuser.Exec(e.ctx, `UPDATE openfox.artifacts SET sha256=repeat('b',64) WHERE id=$1`, first.ID); err == nil {
		t.Fatal("mutated immutable bytes")
	}
	e.owner().expectError(409, "REVIEW_CONFLICT", http.MethodPost, "conversations/"+conversation+"/messages", map[string]any{
		"body": "edit old version", "client_message_id": s3NewID(t),
		"file_contexts": []any{map[string]any{"id": s3NewID(t), "artifact_id": first.ID, "sha256": first.SHA256,
			"comments": []any{map[string]any{"thread_id": s3NewID(t), "body": "change", "anchor": map[string]any{"kind": "text", "start": 0, "end": 1, "quote": "a"}}}}},
	})
	// An unavailable head stays the head; an older available revision is not substituted.
	e.exec(`UPDATE openfox.artifacts SET state='MISSING',version=version+1 WHERE id=$1`, head.ID)
	e.owner().must(200, http.MethodGet, path+"?content_id="+first.ContentID, nil, &head)
	if head.State != "MISSING" || head.ContentRevision != 3 {
		t.Fatal("silently fell back to older revision")
	}
	e.admin().must(404, http.MethodGet, path+"?artifact_id="+first.ID, nil, nil)
	if r := e.owner().doIn(e.otherOrg, http.MethodGet, path+"?content_id="+first.ContentID, nil); r.Status < 400 {
		t.Fatal("cross tenant read")
	}
	otherRun := e.insertRun(e.space.ID, agent, "", "RUNNING")
	var otherConversation string
	e.scalar(&otherConversation, `SELECT conversation_id::text FROM openfox.runs WHERE id=$1`, otherRun)
	e.owner().must(404, http.MethodGet, "conversations/"+otherConversation+"/artifact-preview?artifact_id="+first.ID, nil, nil)
	e.owner().must(400, http.MethodGet, path, nil, nil)
	e.owner().must(400, http.MethodGet, path+"?artifact_id="+first.ID+"&content_id="+first.ContentID, nil, nil)
	// Old pinned revisions resolve even after they fall outside the recent 200.
	e.exec(`INSERT INTO openfox.artifacts(organization_id,run_id,name,storage_key,size_bytes,sha256,mime_type)
 SELECT $1::uuid,$2::uuid,'filler.txt','filler/'||gen_random_uuid(),1,repeat('c',64),'text/plain' FROM generate_series(1,201)`, e.org, run)
	var list struct {
		Artifacts []executiondelivery.Artifact `json:"artifacts"`
	}
	e.owner().must(200, http.MethodGet, "conversations/"+conversation+"/artifacts", nil, &list)
	if len(list.Artifacts) != 200 {
		t.Fatal("list limit changed")
	}
	for _, item := range list.Artifacts {
		if item.ID == first.ID {
			t.Fatal("fixture failed to age out fixed revision")
		}
	}
	e.owner().must(200, http.MethodGet, path+"?artifact_id="+first.ID, nil, &fixed)
	if fixed.ID != first.ID {
		t.Fatal("lost pinned revision")
	}
	// The DB guard also rejects linking across conversations, independent of API callers.
	_, err = e.superuser.Exec(e.ctx, `INSERT INTO openfox.artifacts(organization_id,run_id,name,storage_key,size_bytes,sha256,parent_artifact_id)
 VALUES($1,$2,'foreign','foreign/'||gen_random_uuid(),1,repeat('d',64),$3)`, e.org, otherRun, head.ID)
	if err == nil {
		t.Fatal("cross conversation lineage allowed")
	}
}
