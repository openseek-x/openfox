package stage10d

import (
	"context"
	"github.com/jackc/pgx/v5"
	"os"
	"testing"
)

func TestReviewConversationInputLookupIndexes(t *testing.T) {
	url := os.Getenv("OPENFOX_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("未配置测试数据库")
	}
	conn, err := pgx.Connect(context.Background(), url)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(context.Background())
	for _, column := range []string{"run_id", "message_id"} {
		var found bool
		err := conn.QueryRow(context.Background(), `SELECT EXISTS(SELECT 1 FROM pg_indexes WHERE schemaname='openfox' AND tablename='conversation_inputs' AND indexdef LIKE $1 AND indexdef LIKE $2)`, "%(organization_id, "+column+")%", "%WHERE ("+column+" IS NOT NULL)%").Scan(&found)
		if err != nil {
			t.Fatal(err)
		}
		if !found {
			t.Errorf("缺少 %s 的租户范围部分索引", column)
		}
	}
}
