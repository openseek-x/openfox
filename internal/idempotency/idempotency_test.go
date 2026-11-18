package idempotency

import (
	"context"
	"errors"
	"testing"
)

func TestValidKey(t *testing.T) {
	cases := map[string]bool{
		"":                                 true,
		"abcdefgh":                         true,
		"0123456789abcdef0123456789abcdef": true,
		"short":                            false,
		"has space here":                   false,
		"中文中文中文中文":                         false,
	}
	for key, want := range cases {
		if got := ValidKey(key); got != want {
			t.Errorf("ValidKey(%q) = %v, want %v", key, got, want)
		}
	}
}

func TestRequestHashIsStableAndScoped(t *testing.T) {
	type input struct {
		Name  string `json:"name"`
		Color string `json:"color"`
	}
	a, err := RequestHash("label.create", input{Name: "Bug", Color: "#DC2626"})
	if err != nil {
		t.Fatal(err)
	}
	b, _ := RequestHash("label.create", input{Name: "Bug", Color: "#DC2626"})
	if a != b || len(a) != 64 {
		t.Fatalf("hash not stable: %q %q", a, b)
	}
	c, _ := RequestHash("label.create", input{Name: "Bug", Color: "#2563EB"})
	d, _ := RequestHash("label_group.create", input{Name: "Bug", Color: "#DC2626"})
	if a == c || a == d {
		t.Fatal("hash must depend on request body and operation")
	}
}

func TestBeginWithoutKeyIsInactive(t *testing.T) {
	claim, err := Begin(context.Background(), nil, "task.create", "", nil)
	if err != nil || claim.Active {
		t.Fatalf("claim = %+v, err = %v", claim, err)
	}
	if err := Complete(context.Background(), nil, claim, "x"); err != nil {
		t.Fatal(err)
	}
}

func TestBeginRejectsInvalidKey(t *testing.T) {
	if _, err := Begin(context.Background(), nil, "task.create", "bad", nil); !errors.Is(err, ErrInvalidKey) {
		t.Fatalf("err = %v", err)
	}
	if _, err := Begin(context.Background(), nil, "Bad Operation", "abcdefgh", nil); !errors.Is(err, ErrInvalidKey) {
		t.Fatalf("err = %v", err)
	}
}
