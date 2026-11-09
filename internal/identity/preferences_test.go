package identity

import (
	"errors"
	"testing"
)

func TestPreferencesDensity(t *testing.T) {
	current := defaultPreferences()
	if current.Density != "comfortable" {
		t.Fatalf("default density = %q", current.Density)
	}
	for _, density := range []string{"compact", "comfortable"} {
		next, err := applyPreferencesPatch(current, PreferencesPatch{Density: &density})
		if err != nil || next.Density != density {
			t.Fatalf("density %q: %+v, %v", density, next, err)
		}
		current = next
	}
	current.Density = "compact"
	if next, err := applyPreferencesPatch(current, PreferencesPatch{}); err != nil || next.Density != "compact" {
		t.Fatalf("omitted density: %+v, %v", next, err)
	}
	for _, density := range []string{"", "dense", "COMPACT", " compact "} {
		if _, err := applyPreferencesPatch(current, PreferencesPatch{Density: &density}); !errors.Is(err, ErrInvalidPreferences) {
			t.Fatalf("invalid density %q: %v", density, err)
		}
	}
}

func TestNormalizeShortcut(t *testing.T) {
	cases := map[string]string{
		"mod+k":           "mod+k",
		" Mod+K ":         "mod+k",
		"shift+mod+m":     "mod+shift+m",
		"alt+shift+mod+/": "mod+shift+alt+/",
		"mod+alt+1":       "mod+alt+1",
		"mod+shift+[":     "mod+shift+[",
	}
	for input, want := range cases {
		got, err := normalizeShortcut(input)
		if err != nil || got != want {
			t.Fatalf("normalizeShortcut(%q) = %q, %v; want %q", input, got, err, want)
		}
	}
	for _, input := range []string{"", "k", "mod", "mod+", "mod+kk", "mod+shift+shift+k", "mod+k+j", "cmd+k", "mod+-", "mod+shift", "mod+m+m", "mod++k"} {
		if got, err := normalizeShortcut(input); !errors.Is(err, ErrInvalidPreferences) {
			t.Fatalf("normalizeShortcut(%q) = %q, %v; want invalid", input, got, err)
		}
	}
}

func TestShortcutConflicts(t *testing.T) {
	cases := []struct {
		name   string
		custom map[string]string
		wantOK bool
	}{
		{name: "defaults are distinct", custom: nil, wantOK: true},
		{name: "custom value frees a default", custom: map[string]string{"command_menu": "mod+f"}, wantOK: false},
		{name: "custom values distinct", custom: map[string]string{"command_menu": "mod+j", "find_in_view": "mod+g"}, wantOK: true},
		{name: "two custom values equal", custom: map[string]string{"command_menu": "mod+j", "move_to_space": "mod+j"}, wantOK: false},
		{name: "custom equals other default", custom: map[string]string{"find_in_view": "mod+shift+m"}, wantOK: false},
	}
	for _, tc := range cases {
		err := checkShortcutConflicts(tc.custom)
		if tc.wantOK != (err == nil) {
			t.Fatalf("%s: checkShortcutConflicts = %v, want ok = %v", tc.name, err, tc.wantOK)
		}
		if err != nil && !errors.Is(err, ErrShortcutConflict) {
			t.Fatalf("%s: error = %v, want ErrShortcutConflict", tc.name, err)
		}
	}
}

func TestApplyPreferencesPatchDropsDefaultsAndReplacesShortcuts(t *testing.T) {
	current := defaultPreferences()
	current.CommandShortcuts = map[string]string{"find_in_view": "mod+g"}
	patch := PreferencesPatch{CommandShortcuts: map[string]string{"command_menu": "Mod+K", "move_to_space": "mod+j"}}
	next, err := applyPreferencesPatch(current, patch)
	if err != nil {
		t.Fatal(err)
	}
	if len(next.CommandShortcuts) != 1 || next.CommandShortcuts["move_to_space"] != "mod+j" {
		t.Fatalf("shortcuts = %v, want only move_to_space", next.CommandShortcuts)
	}
	if _, err := applyPreferencesPatch(current, PreferencesPatch{CommandShortcuts: map[string]string{"unknown": "mod+j"}}); !errors.Is(err, ErrInvalidPreferences) {
		t.Fatalf("unknown command error = %v", err)
	}
	if _, err := applyPreferencesPatch(current, PreferencesPatch{CommandShortcuts: map[string]string{"find_in_view": "mod+k"}}); !errors.Is(err, ErrShortcutConflict) {
		t.Fatalf("conflict error = %v", err)
	}
}
