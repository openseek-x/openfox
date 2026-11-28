package cliagent

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"openfox/internal/runtime"
)

func TestClaudeLoadsRunSkills(t *testing.T) {
	dir := t.TempDir()
	req := runtime.RunRequest{Skills: []runtime.Skill{{Name: "alpha", Files: []runtime.SkillFile{{Path: "SKILL.md", Content: []byte("example")}}}}}
	inv, err := buildClaude(nil, req, nil, dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	i := slices.Index(inv.args, "--plugin-dir")
	if i < 0 || i+1 >= len(inv.args) {
		t.Fatal(inv.args)
	}
	content, err := os.ReadFile(filepath.Join(inv.args[i+1], "skills", "alpha", "SKILL.md"))
	if err != nil || string(content) != "example" {
		t.Fatalf("skill: %q %v", content, err)
	}
	req.Skills[0].Name = "../escape"
	if _, err := buildClaude(nil, req, nil, t.TempDir(), nil); err == nil {
		t.Fatal("接受了非法技能名")
	}
}
