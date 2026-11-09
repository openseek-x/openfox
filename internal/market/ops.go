package market

import (
	"context"
	"strings"
	"unicode/utf8"

	"openfox/internal/artifacts"
	"openfox/internal/skills"
)

// ReadFiles 读出市场技能的全部文件内容（核对大小与 sha256）。
func ReadFiles(ctx context.Context, storage artifacts.Storage, src Source) ([]skills.File, error) {
	files := make([]skills.File, 0, len(src.Files))
	for _, record := range src.Files {
		content, err := skills.ReadBlob(ctx, storage, record.Entry)
		if err != nil {
			return nil, err
		}
		files = append(files, skills.File{Path: record.Path, Content: content})
	}
	return files, nil
}

// SpacePackage 把市场技能整理成空间技能内容。名称与描述取 SKILL.md 的 frontmatter；
// frontmatter 不合规（缺失、名称不合规则等）时，名称由市场 ID 末段规范化，描述取市场目录里的描述。
func SpacePackage(src Source, files []skills.File) skills.Package {
	pkg := skills.Package{Files: files}
	var parsed skills.Parsed
	var err error = skills.ErrInvalid
	for _, file := range files {
		pkg.TotalBytes += int64(len(file.Content))
		if file.Path == skills.SkillFile {
			parsed, err = skills.ParseSkillMD(file.Content)
		}
	}
	if err == nil {
		pkg.Name, pkg.Description = parsed.Name, parsed.Description
		return pkg
	}
	pkg.Name = NormalizeName(src.ID)
	description := strings.TrimSpace(pick(src.Description))
	if utf8.RuneCountInString(description) > skills.MaxDescriptionRunes {
		description = string([]rune(description)[:skills.MaxDescriptionRunes])
	}
	pkg.Description = description
	return pkg
}

// SourceLabel 返回空间技能的 source_label（不超过 200 个字符）。
func SourceLabel(src Source) string {
	return truncateRunes(src.Label, 200)
}
