package collaboration

import (
	"encoding/base64"
	"encoding/json"
	"testing"
)

func TestViewDisplayOrderingValidation(t *testing.T) {
	for _, raw := range []string{`{}`, `{"layout":"gantt","existing":{"custom":true}}`,
		`{"sortDirection":"asc","groupOrder":{"status":"reverse","assignee":"default"}}`,
		`{"sortDirection":"desc","groupOrder":{}}`} {
		if err := validateViewJSON(nil, json.RawMessage(raw)); err != nil {
			t.Fatalf("valid display %s: %v", raw, err)
		}
	}
	for _, raw := range []string{`{"sortDirection":"sideways"}`, `{"sortDirection":null}`, `{"sortDirection":{}}`,
		`{"groupOrder":[]}`, `{"groupOrder":null}`, `{"groupOrder":{"status":"asc"}}`,
		`{"groupOrder":{"unknown":"reverse"}}`, `{"groupOrder":{"status":{}}}`} {
		if err := validateViewJSON(nil, json.RawMessage(raw)); err == nil {
			t.Fatalf("invalid display accepted: %s", raw)
		}
	}
}

func TestIterationCursorUsesSQLNullSentinel(t *testing.T) {
	for _, order := range []string{"asc", "desc"} {
		sorting, err := resolveTaskSort("iteration", order, "manual", "asc")
		if err != nil {
			t.Fatal(err)
		}
		for _, tc := range []struct {
			number int
			key    string
		}{{0, "2147483647"}, {1, "1"}, {27, "27"}} {
			task := Task{ID: "00000000-0000-0000-0000-000000000001", IterationNumber: tc.number}
			cursor, err := sorting.decodeCursor(sorting.encodeCursor(task))
			if err != nil || cursor.Key != tc.key {
				t.Fatalf("%s iteration %d: cursor %+v, error %v", order, tc.number, cursor, err)
			}
		}
		legacy, _ := json.Marshal(taskCursor{Version: 2, Sort: "iteration", Order: order, Key: "0", ID: "00000000-0000-0000-0000-000000000001"})
		decoded, err := sorting.decodeCursor(base64.RawURLEncoding.EncodeToString(legacy))
		if err != nil || decoded.Key != "2147483647" {
			t.Fatalf("legacy iteration cursor: %+v %v", decoded, err)
		}
	}
}
