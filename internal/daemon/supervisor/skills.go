package supervisor

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	"openfox/internal/runtime"
	"openfox/internal/skillpack"
)

// maxRunSkills 是单个 Agent 装配技能数的上限（与中心一致）。
const maxRunSkills = 20

// downloadSkills 在启动 Runtime 前逐个下载 RunContext.skills；任何一个失败即返回错误（调用方走 prepare-failed）。
// 清单为空时直接返回 nil，行为与旧版完全一致。错误信息只含技能名与错误码，不含内容。
func (r *runState) downloadSkills(ctx context.Context, runContext RunContext) ([]runtime.Skill, error) {
	if len(runContext.Skills) == 0 {
		return nil, nil
	}
	if len(runContext.Skills) > maxRunSkills {
		return nil, errors.New("too many skills")
	}
	client := r.supervisor.config.Client
	seen := map[string]struct{}{}
	skills := make([]runtime.Skill, 0, len(runContext.Skills))
	for _, entry := range runContext.Skills {
		// 大小写不敏感地去重：macOS 默认文件系统不区分大小写。
		key := strings.ToLower(entry.Name)
		if _, dup := seen[key]; dup || !skillpack.ValidName(entry.Name) || entry.ID == "" {
			return nil, fmt.Errorf("invalid skill manifest entry %q", entry.Name)
		}
		seen[key] = struct{}{}
		bundle, err := client.RunSkill(ctx, r.offer.OrganizationID, r.offer.RunID, entry.ID, r.offer.Generation)
		if err != nil {
			return nil, fmt.Errorf("download skill %q: %s", entry.Name, downloadErrorText(err))
		}
		skill, err := decodeSkill(entry.Name, bundle)
		if err != nil {
			return nil, fmt.Errorf("skill %q: %w", entry.Name, err)
		}
		skills = append(skills, skill)
	}
	return skills, nil
}

func downloadErrorText(err error) string {
	if code := errorCode(err); code != "" {
		return code
	}
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		return fmt.Sprintf("status %d", apiErr.Status)
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err.Error()
	}
	return "request failed"
}

// decodeSkill 校验并解码中心下发的技能包：名称与清单一致、路径合法且不重复、文件数与字节数在限制内、含根目录 SKILL.md。
func decodeSkill(name string, bundle SkillBundle) (runtime.Skill, error) {
	if bundle.Name != name || bundle.ErrorCode != "" {
		return runtime.Skill{}, errors.New("bundle does not match manifest")
	}
	if len(bundle.Files) == 0 || len(bundle.Files) > skillpack.MaxFiles {
		return runtime.Skill{}, errors.New("invalid file count")
	}
	skill := runtime.Skill{Name: name, Files: make([]runtime.SkillFile, 0, len(bundle.Files))}
	paths := map[string]struct{}{}
	var total int64
	hasEntry := false
	for _, file := range bundle.Files {
		if !skillpack.ValidPath(file.Path) {
			return runtime.Skill{}, errors.New("invalid file path")
		}
		key := strings.ToLower(file.Path)
		if _, dup := paths[key]; dup {
			return runtime.Skill{}, errors.New("duplicate file path")
		}
		paths[key] = struct{}{}
		// 解码前先按 base64 长度估算，避免为超限内容分配内存。
		if int64(len(file.ContentBase64)) > (skillpack.MaxBytes+2)/3*4 {
			return runtime.Skill{}, errors.New("skill is too large")
		}
		content, err := base64.StdEncoding.DecodeString(file.ContentBase64)
		if err != nil {
			return runtime.Skill{}, errors.New("invalid file content")
		}
		if total += int64(len(content)); total > skillpack.MaxBytes {
			return runtime.Skill{}, errors.New("skill is too large")
		}
		hasEntry = hasEntry || file.Path == skillpack.SkillFile
		skill.Files = append(skill.Files, runtime.SkillFile{Path: file.Path, Content: content})
	}
	if !hasEntry {
		return runtime.Skill{}, errors.New("missing SKILL.md")
	}
	return skill, nil
}
