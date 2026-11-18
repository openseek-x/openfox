package imports

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"openfox/internal/collaboration"
	"openfox/internal/spacebundle"
)

func codes(list []CloneIssue) string {
	out := make([]string, 0, len(list))
	for _, i := range list {
		out = append(out, i.Code)
	}
	return strings.Join(out, ",")
}

func TestNormalizeCloneRequest(t *testing.T) {
	name, prefix, err := NormalizeCloneRequest(CloneRequest{}, "Alpha", "ALP")
	if err != nil || name != "Alpha" || prefix != "ALP" {
		t.Fatalf("defaults = %q %q %v", name, prefix, err)
	}
	name, prefix, err = NormalizeCloneRequest(CloneRequest{Name: "  Copy  ", Prefix: "cp2"}, "Alpha", "ALP")
	if err != nil || name != "Copy" || prefix != "CP2" {
		t.Fatalf("explicit = %q %q %v", name, prefix, err)
	}
	if _, _, err := NormalizeCloneRequest(CloneRequest{Name: strings.Repeat("长", 33)}, "Alpha", "ALP"); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("33-rune name err = %v", err)
	}
	if _, _, err := NormalizeCloneRequest(CloneRequest{Prefix: "a-b"}, "Alpha", "ALP"); !errors.Is(err, collaboration.ErrInvalidIdentifier) {
		t.Fatalf("bad prefix err = %v", err)
	}
}

func TestBuildClonePreview(t *testing.T) {
	sum := spacebundle.Summary{SpaceName: "Alpha", SpacePrefix: "ALP", Tasks: 4, Comments: 3, Activities: 2, Agents: 2, AgentAssigned: 1,
		Links: 1, GitLinks: 2, Automations: 1, Iterations: 3}
	p := BuildClonePreview(sum, "Copy", "CPY", "org-t", CloneTargetCheck{OrganizationName: "Target"})
	if !p.CanStart || p.NameConflict || len(p.Blockers) != 0 || p.Counts.Spaces != 1 || p.Counts.Agents != 2 {
		t.Fatalf("clean preview = %+v", p)
	}
	if codes(p.Warnings) != "agents_need_reconfigure,actor_history_kept" || p.Warnings[0].Message != "需要重新配置 Agent。" {
		t.Fatalf("warnings = %+v", p.Warnings)
	}
	if codes(p.Skipped) != "agents,links,git_links,automations,iterations" {
		t.Fatalf("skipped = %s", codes(p.Skipped))
	}
	if codes(p.AcceptedRisks) != "people_external,agent_assignee_removed" {
		t.Fatalf("risks = %s", codes(p.AcceptedRisks))
	}

	// 命名冲突：阻塞，需要确认副本名称。
	p = BuildClonePreview(sum, "Alpha", "ALP", "org-t", CloneTargetCheck{NameTaken: true, PrefixTaken: true})
	if p.CanStart || !p.NameConflict || codes(p.Blockers) != "name_taken,identifier_taken" {
		t.Fatalf("conflict preview = %+v", p)
	}

	// 超出上限：阻塞；恰好等于上限不阻塞。
	over := spacebundle.Summary{Tasks: spacebundle.MaxTasks + 1, Comments: spacebundle.MaxComments + 1, Activities: spacebundle.MaxActivities + 1}
	p = BuildClonePreview(over, "A", "AA", "org-t", CloneTargetCheck{})
	if p.CanStart || codes(p.Blockers) != "task_limit,comment_limit,activity_limit" {
		t.Fatalf("over-limit preview = %+v", p)
	}
	p = BuildClonePreview(spacebundle.Summary{Tasks: spacebundle.MaxTasks, Comments: spacebundle.MaxComments}, "A", "AA", "org-t", CloneTargetCheck{})
	if !p.CanStart {
		t.Fatalf("at-limit preview blocked: %+v", p.Blockers)
	}

	// 没有 Agent、评论、动态时不出现对应警告。
	p = BuildClonePreview(spacebundle.Summary{Tasks: 1}, "A", "AA", "org-t", CloneTargetCheck{})
	if len(p.Warnings) != 0 || len(p.Skipped) != 0 {
		t.Fatalf("minimal preview = %+v", p)
	}
}

func TestClonePreviewDisclosesUploadedSpaceIcon(t *testing.T) {
	preview := BuildClonePreview(spacebundle.Summary{UploadedSpaceIcon: true}, "Copy", "CPY", "target", CloneTargetCheck{})
	if !preview.CanStart || !strings.Contains(codes(preview.Skipped), "space_icon") {
		t.Fatalf("uploaded image must be disclosed without blocking clone: %+v", preview)
	}
}

func TestCloneFieldValue(t *testing.T) {
	sel := cloneField{ID: "f1", Kind: "SELECT", Options: map[string]string{"src-a": "dst-a"}}
	if v, ok := cloneFieldValue(sel, json.RawMessage(`"src-a"`)); !ok || string(v) != `"dst-a"` {
		t.Fatalf("select = %s %v", v, ok)
	}
	if _, ok := cloneFieldValue(sel, json.RawMessage(`"unknown"`)); ok {
		t.Fatal("unknown option should be dropped")
	}
	multi := cloneField{ID: "f2", Kind: "MULTI_SELECT", Options: map[string]string{"a": "x", "b": "y"}}
	if v, ok := cloneFieldValue(multi, json.RawMessage(`["a","zzz","b"]`)); !ok || string(v) != `["x","y"]` {
		t.Fatalf("multi = %s %v", v, ok)
	}
	if _, ok := cloneFieldValue(multi, json.RawMessage(`["zzz"]`)); ok {
		t.Fatal("multi with no mapped option should be dropped")
	}
	if _, ok := cloneFieldValue(cloneField{ID: "f3", Kind: "MEMBER"}, json.RawMessage(`{"kind":"HUMAN","id":"u"}`)); ok {
		t.Fatal("member values are not cloned")
	}
	if v, ok := cloneFieldValue(cloneField{ID: "f4", Kind: "TEXT"}, json.RawMessage(`"hello"`)); !ok || string(v) != `"hello"` {
		t.Fatalf("text = %s %v", v, ok)
	}
	if _, ok := cloneFieldValue(cloneField{}, json.RawMessage(`"x"`)); ok {
		t.Fatal("unresolved field should be dropped")
	}
}

func TestCloneCommentBody(t *testing.T) {
	if cloneClientID("job", "c1") == cloneClientID("job", "c2") || cloneClientID("job", "c1") != cloneClientID("job", "c1") {
		t.Fatal("client ids must be stable and distinct")
	}
	if len(cloneClientID("job", "c1")) > 64 {
		t.Fatal("client id too long")
	}
}

func TestIsTransientCloneError(t *testing.T) {
	transient := []error{
		context.DeadlineExceeded, io.ErrUnexpectedEOF, fmt.Errorf("wrapped: %w", context.Canceled),
		&pgconn.PgError{Code: "40001"}, &pgconn.PgError{Code: "40P01"}, &pgconn.PgError{Code: "55P03"},
		&pgconn.PgError{Code: "57014"}, &pgconn.PgError{Code: "08006"}, &pgconn.PgError{Code: "53300"},
	}
	for _, err := range transient {
		if !isTransientCloneError(err) {
			t.Fatalf("%v should be transient", err)
		}
	}
	permanent := []error{
		pgx.ErrNoRows, errors.New("boom"), &pgconn.PgError{Code: "23505"}, &pgconn.PgError{Code: "23514"},
		fmt.Errorf("wrapped: %w", pgx.ErrNoRows), errBadBundle("x"),
	}
	for _, err := range permanent {
		if isTransientCloneError(err) {
			t.Fatalf("%v should be permanent", err)
		}
	}
}
