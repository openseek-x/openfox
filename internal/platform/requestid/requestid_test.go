package requestid

import "testing"

func TestNewReturnsHexRequestID(t *testing.T) {
	first := New()
	second := New()
	if len(first) != 32 || len(second) != 32 || first == second {
		t.Fatalf("request IDs are not unique 32-character values: %q %q", first, second)
	}
}
