package identity

import (
	"errors"
	"slices"
	"testing"
)

func TestValidNotificationChannel(t *testing.T) {
	cases := map[string]bool{"desktop": true, "browser": true, "feishu": true, "email": true, "wecom": true, "Desktop": false, "": false}
	for input, want := range cases {
		if got := validNotificationChannel(input); got != want {
			t.Fatalf("validNotificationChannel(%q) = %v, want %v", input, got, want)
		}
	}
}

func TestNormalizeDisabledEvents(t *testing.T) {
	cases := []struct {
		name  string
		input []string
		want  []string
	}{
		{name: "empty is kept as empty, not nil", input: nil, want: []string{}},
		{name: "deduplicated and sorted", input: []string{"mention", "assign", "mention"}, want: []string{"assign", "mention"}},
		{name: "all six events", input: []string{"automation", "agent", "mention", "comment", "status", "assign"},
			want: []string{"agent", "assign", "automation", "comment", "mention", "status"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := normalizeDisabledEvents(tc.input)
			if err != nil || got == nil || !slices.Equal(got, tc.want) {
				t.Fatalf("normalizeDisabledEvents(%v) = %v, %v; want %v", tc.input, got, err, tc.want)
			}
		})
	}
	for _, input := range [][]string{{"unknown"}, {"assign", "Status"}, {""}} {
		if _, err := normalizeDisabledEvents(input); !errors.Is(err, ErrInvalidNotificationSettings) {
			t.Fatalf("normalizeDisabledEvents(%v) error = %v, want invalid", input, err)
		}
	}
}

func TestDefaultNotificationChannel(t *testing.T) {
	item := defaultNotificationChannel("desktop")
	if item.Channel != "desktop" || !item.Enabled || item.Customized || item.DisabledEvents == nil || len(item.DisabledEvents) != 0 {
		t.Fatalf("default channel = %+v, want enabled, no disabled events, not customized", item)
	}
	email := defaultNotificationChannel("email")
	if email.Enabled || email.DirectMessages == nil || *email.DirectMessages {
		t.Fatalf("default email channel = %+v, want both notifications and direct messages off", email)
	}
	wecom := defaultNotificationChannel("wecom")
	if wecom.Enabled || wecom.DirectMessages == nil || *wecom.DirectMessages {
		t.Fatalf("default wecom channel = %+v, want both notifications and direct messages off", wecom)
	}
}
