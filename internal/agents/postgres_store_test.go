package agents

import "testing"

func TestNormalizeConfigInputDefaultsAndBounds(t *testing.T) {
	input, err := normalizeConfigInput(ConfigInput{
		ComputerID: "00000000-0000-0000-0000-000000000001",
		GrantID:    "00000000-0000-0000-0000-000000000002",
		Runtime:    "codex",
		Model:      "gpt-5",
	})
	if err != nil {
		t.Fatal(err)
	}
	if input.MaxConcurrency != 1 || input.QueueTimeoutSeconds != 3600 || input.RunTimeoutSeconds != 3600 || input.DirectoryMode != DirectoryNone {
		t.Fatalf("defaults = %+v", input)
	}
}

func TestNormalizeConfigInputRejectsUnsafeDirectoryRoots(t *testing.T) {
	base := ConfigInput{
		ComputerID:    "00000000-0000-0000-0000-000000000001",
		GrantID:       "00000000-0000-0000-0000-000000000002",
		Runtime:       "codex",
		Model:         "gpt-5",
		DirectoryMode: DirectoryExplicit,
	}
	for _, root := range []string{"/tmp/work", "../work", "work/../other", "work//other", "~/work"} {
		input := base
		input.DirectoryRoots = []string{root}
		if _, err := normalizeConfigInput(input); err != ErrInvalidConfig {
			t.Fatalf("root %q error = %v, want invalid config", root, err)
		}
	}
}

func TestNormalizeCreateInputRequiresManagedKindsAndVisibility(t *testing.T) {
	base := CreateInput{
		Name:       "Build Agent",
		Kind:       KindCode,
		Visibility: VisibilityShared,
		Config: ConfigInput{
			ComputerID: "00000000-0000-0000-0000-000000000001",
			GrantID:    "00000000-0000-0000-0000-000000000002",
			Runtime:    "codex",
			Model:      "gpt-5",
		},
	}
	if _, err := normalizeCreateInput(base); err != nil {
		t.Fatal(err)
	}
	base.Kind = Kind("GENERAL")
	if _, err := normalizeCreateInput(base); err != ErrInvalidRequest {
		t.Fatalf("kind error = %v, want invalid request", err)
	}
}
