package codex

import (
	"path/filepath"

	"openfox/internal/runtime"
)

// writeSkills keeps Codex's isolated-home layout; Pi uses the same bounded writer.
func writeSkills(home string, skills []runtime.Skill) error { return runtime.WriteSkills(home, skills) }
func writeSkillFile(dir string, file runtime.SkillFile) error {
	return runtime.WriteSkillFile(dir, file)
}

// SourceHome 返回用户的源 CODEX_HOME（环境变量 CODEX_HOME，否则 ~/.codex），
// 与 Run 准备独立 CODEX_HOME 时使用的来源一致。
func (a *Adapter) SourceHome() (string, error) {
	return sourceCodexHome(a.baseEnvironment())
}

// UserSkillDirs 返回 Codex 读取用户技能的目录，按优先顺序：$HOME/.agents/skills（标准位置），
// 然后是源 $CODEX_HOME/skills（旧位置，官方已标为 deprecated，仍兼容）。.system 内置缓存不在扫描范围。
func (a *Adapter) UserSkillDirs() ([]string, error) {
	environment := a.baseEnvironment()
	source, err := sourceCodexHome(environment)
	if err != nil {
		return nil, err
	}
	dirs := []string{}
	if home := environment["HOME"]; home != "" {
		dirs = append(dirs, filepath.Join(home, ".agents", "skills"))
	}
	return append(dirs, filepath.Join(source, "skills")), nil
}
