package stage3

import (
	"fmt"
	"net/http"
	"openfox/internal/docs"
	"sync"
	"testing"
)

func TestDocRevisionsRestorePermissionsConflictAndPagination(t *testing.T) {
	env := newS3Env(t)
	manager, space := env.manager(), env.space.ID
	env.addSpaceMember(space, s3OtherID, "VIEWER")
	set := env.newDocSet(manager, space, "History")
	node := env.newDocNode(manager, space, map[string]any{"container": docInSet(set.ID), "kind": "doc", "title": "First", "body": "ONE"})
	path := fmt.Sprintf("%s/docs/%s", docSpacePath(space), node.ID)
	var edited docView
	manager.must(http.StatusOK, http.MethodPatch, path, map[string]any{"version": node.Version, "title": "Second", "body": "TWO"}, &edited)
	var list struct {
		Revisions []docs.Revision `json:"revisions"`
		Next      uint64          `json:"next_before"`
	}
	manager.must(http.StatusOK, http.MethodGet, path+"/revisions", nil, &list)
	if len(list.Revisions) != 2 || list.Revisions[0].Revision != 2 || list.Revisions[0].Body != nil {
		t.Fatalf("history = %+v", list)
	}
	var old docs.Revision
	manager.must(http.StatusOK, http.MethodGet, path+"/revisions/1", nil, &old)
	if old.Body == nil || *old.Body != "ONE" || old.Title != "First" {
		t.Fatalf("preview = %+v", old)
	}
	env.other().must(http.StatusOK, http.MethodGet, path+"/revisions/1", nil, &old)
	env.other().expectError(http.StatusForbidden, "ACTION_FORBIDDEN", http.MethodPost, path+"/revisions/1/restore", map[string]any{"version": edited.Version})
	env.outsider().expectError(http.StatusNotFound, "NOT_FOUND", http.MethodGet, path+"/revisions", nil)
	manager.expectError(http.StatusConflict, "VERSION_CONFLICT", http.MethodPost, path+"/revisions/1/restore", map[string]any{"version": node.Version})
	manager.expectError(http.StatusNotFound, "NOT_FOUND", http.MethodPost, path+"/revisions/999/restore", map[string]any{"version": edited.Version})
	var restored docView
	manager.must(http.StatusOK, http.MethodPost, path+"/revisions/1/restore", map[string]any{"version": edited.Version}, &restored)
	if restored.Version != edited.Version+1 || restored.Body == nil || *restored.Body != "ONE" || restored.Title != "First" {
		t.Fatalf("restored = %+v", restored)
	}
	manager.must(http.StatusOK, http.MethodGet, path+"/revisions", nil, &list)
	if len(list.Revisions) != 3 || list.Revisions[0].Revision != 3 {
		t.Fatalf("restore must append: %+v", list)
	}
	manager.must(http.StatusOK, http.MethodGet, path+"/revisions/2", nil, &old)
	if *old.Body != "TWO" {
		t.Fatal("restore overwrote history")
	}
	manager.must(http.StatusOK, http.MethodPost, path+"/revisions/1/restore", map[string]any{"version": restored.Version}, &restored)
	manager.must(http.StatusOK, http.MethodGet, path+"/revisions", nil, &list)
	if len(list.Revisions) != 4 || list.Revisions[0].Revision != 4 {
		t.Fatalf("identical restoration must append: %+v", list)
	}
	secret := env.newDocNode(manager, space, map[string]any{"container": docPersonal(), "kind": "doc", "title": "Private", "body": "SECRET"})
	secretPath := fmt.Sprintf("%s/docs/%s/revisions", docSpacePath(space), secret.ID)
	env.member().expectError(http.StatusNotFound, "NOT_FOUND", http.MethodGet, secretPath, nil)
	env.member().expectError(http.StatusNotFound, "NOT_FOUND", http.MethodGet, secretPath+"/1", nil)
	env.member().expectError(http.StatusNotFound, "NOT_FOUND", http.MethodPost, secretPath+"/1/restore", map[string]any{"version": secret.Version})
	for i := 0; i < 51; i++ {
		manager.must(http.StatusOK, http.MethodPatch, path, map[string]any{"version": restored.Version, "body": fmt.Sprintf("page %d", i)}, &restored)
	}
	manager.must(http.StatusOK, http.MethodGet, path+"/revisions", nil, &list)
	if len(list.Revisions) != 50 || list.Next != 6 {
		t.Fatalf("page = %+v", list)
	}
	manager.must(http.StatusOK, http.MethodGet, path+"/revisions?before=6", nil, &list)
	if len(list.Revisions) != 5 || list.Next != 0 || list.Revisions[4].Revision != 1 {
		t.Fatalf("tail = %+v", list)
	}
}

func TestDocRevisionsLegacyMoveAndConcurrentRestore(t *testing.T) {
	env := newS3Env(t)
	manager, space := env.manager(), env.space.ID
	env.addSpaceMember(space, s3OtherID, "VIEWER")
	set := env.newDocSet(manager, space, "History movement")
	node := env.newDocNode(manager, space, map[string]any{"container": docPersonal(), "kind": "doc", "title": "Legacy", "body": "BEFORE"})
	path := fmt.Sprintf("%s/docs/%s", docSpacePath(space), node.ID)
	// Model a pre-migration document with no snapshots; no production backfill.
	env.exec(`DELETE FROM openfox.doc_revisions WHERE node_id=$1::uuid`, node.ID)
	manager.must(http.StatusOK, http.MethodPatch, path, map[string]any{"version": node.Version, "body": "AFTER"}, &node)
	var old docs.Revision
	manager.must(http.StatusOK, http.MethodGet, path+"/revisions/1", nil, &old)
	if old.Body == nil || *old.Body != "BEFORE" {
		t.Fatalf("legacy content lost: %+v", old)
	}
	env.other().expectError(http.StatusNotFound, "NOT_FOUND", http.MethodGet, path+"/revisions", nil)
	manager.must(http.StatusOK, http.MethodPost, path+"/move", map[string]any{"version": node.Version, "target_space_id": space, "container": docInSet(set.ID)}, &node)
	env.other().must(http.StatusOK, http.MethodGet, path+"/revisions/1", nil, &old)
	if count := env.count(`SELECT count(*) FROM openfox.doc_revisions WHERE node_id=$1::uuid`, node.ID); count != 2 {
		t.Fatalf("move added history: %d", count)
	}
	start := make(chan struct{})
	results := make(chan s3Response, 2)
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			results <- manager.do(http.MethodPost, path+"/revisions/1/restore", map[string]any{"version": node.Version})
		}()
	}
	close(start)
	wg.Wait()
	close(results)
	statuses := map[int]int{}
	for result := range results {
		statuses[result.Status]++
		if result.Status == http.StatusConflict && result.code() != "VERSION_CONFLICT" {
			t.Fatalf("unexpected conflict: %s", result.Body)
		}
	}
	if statuses[http.StatusOK] != 1 || statuses[http.StatusConflict] != 1 {
		t.Fatalf("concurrent restoration statuses: %v", statuses)
	}
	if count := env.count(`SELECT count(*) FROM openfox.doc_revisions WHERE node_id=$1::uuid`, node.ID); count != 3 {
		t.Fatalf("concurrent restore history count: %d", count)
	}
	manager.must(http.StatusOK, http.MethodGet, path, nil, &node)
	manager.must(http.StatusOK, http.MethodPost, path+"/move", map[string]any{"version": node.Version, "target_space_id": space, "container": docPersonal()}, &node)
	env.other().expectError(http.StatusNotFound, "NOT_FOUND", http.MethodGet, path+"/revisions/1", nil)
}
