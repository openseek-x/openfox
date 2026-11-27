package skills

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
)

// OPE-19：docs/agent-skills 下随仓库提交的技能包必须始终能被「上传技能」导入，
// 这里用导入时同一套解析与校验规则检查测试智能体的取证技能。
func TestTestEvidenceDeliverySkillIsImportable(t *testing.T) {
	path := filepath.Join("..", "..", "docs", "agent-skills", "test-evidence-delivery", SkillFile)
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read skill: %v", err)
	}
	parsed, err := ParseSkillMD(content)
	if err != nil {
		t.Fatalf("ParseSkillMD: %v", err)
	}
	if parsed.Name != "test-evidence-delivery" || !ValidName(parsed.Name) {
		t.Fatalf("name = %q, want valid test-evidence-delivery", parsed.Name)
	}
	if parsed.Description == "" || utf8.RuneCountInString(parsed.Description) > MaxDescriptionRunes {
		t.Fatalf("description has %d runes, want 1-%d", utf8.RuneCountInString(parsed.Description), MaxDescriptionRunes)
	}
	if len(content) > MaxInstructionBytes {
		t.Fatalf("skill is %d bytes, want at most %d", len(content), MaxInstructionBytes)
	}
	// 正文要覆盖需求 F01–F07、F10 的关键约定，防止后续误删章节。
	for _, keyword := range []string{"artifact_ids", "WebM", "脱敏", "report.html", "progress.json", "20"} {
		if !strings.Contains(parsed.Body, keyword) {
			t.Errorf("skill body is missing %q", keyword)
		}
	}
	files, err := NormalizeFiles([]File{{Path: SkillFile, Content: content}})
	if err != nil || len(files) != 1 || files[0].Path != SkillFile {
		t.Fatalf("NormalizeFiles = %v, %v, want the single SKILL.md", files, err)
	}
}
