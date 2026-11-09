package integrations

// resolveTaskRefs 依次按当前前缀、历史前缀、移动前的旧编号（别名）解析任务编号。
// 需要真实 PostgreSQL（含迁移 000064）：未设置 OPENFOX_TEST_DATABASE_URL 时跳过；由 tests/integration/stage9c/run.sh 运行。

import (
	"context"
	"crypto/rand"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

func refsTestID(t *testing.T) string {
	t.Helper()
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		t.Fatal(err)
	}
	raw[6], raw[8] = (raw[6]&0x0f)|0x40, (raw[8]&0x3f)|0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", raw[0:4], raw[4:6], raw[6:8], raw[8:10], raw[10:16])
}

func TestResolveTaskRefsCurrentHistoryAndAlias(t *testing.T) {
	url := os.Getenv("OPENFOX_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("OPENFOX_TEST_DATABASE_URL is not configured")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	conn, err := pgx.Connect(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)
	tx, err := conn.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// 只在事务里造数据并回滚，不留下任何行。
	defer tx.Rollback(ctx)
	org, user, spaceA, spaceB := refsTestID(t), refsTestID(t), refsTestID(t), refsTestID(t)
	moved, stay := refsTestID(t), refsTestID(t)
	suffix := fmt.Sprintf("%X", org[:2])
	prefixA, oldA, prefixB := "RA"+suffix, "RO"+suffix, "RB"+suffix
	seedRefs(ctx, t, tx, []string{
		`INSERT INTO openfox.users (id, email) VALUES ('` + user + `', 'refs-` + user[:8] + `@example.test')`,
		`INSERT INTO openfox.organizations (id, slug, name) VALUES ('` + org + `', 'refs-` + org[:8] + `', 'Refs')`,
		`INSERT INTO openfox.organization_members (organization_id, user_id, role) VALUES ('` + org + `', '` + user + `', 'OWNER')`,
		`SELECT openfox_private.set_tenant_context('` + org + `'::uuid, '` + user + `'::uuid, 1, 'user_request')`,
		`INSERT INTO openfox.task_statuses (organization_id, name, category, position) VALUES ('` + org + `', 'Todo', 'TODO', 0)`,
		`INSERT INTO openfox.spaces (organization_id, id, name, visibility, prefix) VALUES
			('` + org + `', '` + spaceA + `', 'A', 'PRIVATE', '` + prefixA + `'), ('` + org + `', '` + spaceB + `', 'B', 'PRIVATE', '` + prefixB + `')`,
		`INSERT INTO openfox.space_prefix_history (organization_id, space_id, prefix, replaced_by) VALUES ('` + org + `', '` + spaceA + `', '` + oldA + `', '` + user + `')`,
		`INSERT INTO openfox.task_types (organization_id, space_id, name) VALUES ('` + org + `', '` + spaceA + `', 'T'), ('` + org + `', '` + spaceB + `', 'T')`,
		// stay：A-1（当前前缀与历史前缀都能命中）；moved：原 A-2，现在是 B-7（别名 A/2 → moved）。
		`INSERT INTO openfox.tasks (organization_id, space_id, id, sequence, title, type_id, status_id)
			SELECT '` + org + `', s.id, x.id, x.seq, 'task', tt.id, (SELECT id FROM openfox.task_statuses WHERE organization_id = '` + org + `')
			  FROM (VALUES ('` + spaceA + `'::uuid, '` + stay + `'::uuid, 1), ('` + spaceB + `'::uuid, '` + moved + `'::uuid, 7)) AS x(space, id, seq)
			  JOIN openfox.spaces s ON s.id = x.space JOIN openfox.task_types tt ON tt.space_id = s.id`,
		`INSERT INTO openfox.task_display_aliases (organization_id, space_id, sequence, task_id, moved_by)
			VALUES ('` + org + `', '` + spaceA + `', 2, '` + moved + `', '` + user + `')`,
	})
	refs := ExtractTaskRefs(RefSource{Name: "title", Text: fmt.Sprintf("fix %s-1, %s-1, %s-2 and %s-7; missing %s-9",
		prefixA, oldA, prefixA, prefixB, prefixB)})
	resolved, err := resolveTaskRefs(ctx, tx, refs)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]resolvedTask{
		refKey(prefixA, 1): {TaskID: stay, SpaceID: spaceA, DisplayID: prefixA + "-1"},  // 当前前缀
		refKey(oldA, 1):    {TaskID: stay, SpaceID: spaceA, DisplayID: prefixA + "-1"},  // 历史前缀
		refKey(prefixA, 2): {TaskID: moved, SpaceID: spaceB, DisplayID: prefixB + "-7"}, // 移动前的旧编号
		refKey(prefixB, 7): {TaskID: moved, SpaceID: spaceB, DisplayID: prefixB + "-7"}, // 新编号
	}
	if len(resolved) != len(want) {
		t.Fatalf("resolved = %+v", resolved)
	}
	for key, item := range want {
		if resolved[key] != item {
			t.Fatalf("resolved[%s] = %+v, want %+v", key, resolved[key], item)
		}
	}
	// 同一任务的旧编号与新编号只关联一次。
	matched := matchTasks(refs, resolved)
	if len(matched) != 2 || matched[0].TaskID != stay || matched[1].TaskID != moved {
		t.Fatalf("matched = %+v", matched)
	}
}

func refKey(prefix string, sequence int) string {
	return fmt.Sprintf("%s-%d", toLowerASCII(prefix), sequence)
}

func toLowerASCII(value string) string {
	out := []byte(value)
	for index, character := range out {
		if character >= 'A' && character <= 'Z' {
			out[index] = character + 'a' - 'A'
		}
	}
	return string(out)
}

func seedRefs(ctx context.Context, t *testing.T, tx pgx.Tx, statements []string) {
	t.Helper()
	for _, statement := range statements {
		if _, err := tx.Exec(ctx, statement); err != nil {
			t.Fatalf("seed %.60s: %v", statement, err)
		}
	}
}
