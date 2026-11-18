package storage

import "testing"

func TestParseCleanupFilterConversationID(t *testing.T) {
	const id = "11111111-1111-4111-8111-111111111111"
	base := `{"action":"preview","days":0,"categories":["conversation"],"conversation_id":"` + id + `"}`
	f, err := ParseCleanupFilter([]byte(base))
	if err != nil || f.ConversationID != id {
		t.Fatalf("valid conversation filter: %+v %v", f, err)
	}
	for _, bad := range []string{
		`{"action":"preview","days":0,"categories":["conversation"],"conversation_id":"not-a-uuid"}`,
		`{"action":"preview","days":0,"categories":["conversation"],"conversation_id":"00000000-0000-0000-0000-000000000000"}`,
		`{"action":"preview","days":0,"categories":["task"],"statuses":["DONE"],"conversation_id":"` + id + `"}`,
	} {
		if _, err := ParseCleanupFilter([]byte(bad)); err == nil {
			t.Fatalf("accepted %s", bad)
		}
	}
	if f, err := ParseCleanupFilter([]byte(`{"action":"preview","days":0,"categories":["task"]}`)); err != nil || f.ConversationID != "" {
		t.Fatalf("org-wide filter changed: %+v %v", f, err)
	}
}
