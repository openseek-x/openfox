package skills

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// ValidatePath 校验技能内的相对路径，规则与迁移 skill_files_path_valid 一致，并在应用层先行拒绝：
// 1–256 个字符；不以 / 开头；不含 \ 与控制字符；不含空段、「.」或「..」段。
func ValidatePath(path string) error {
	if path == "" || !utf8.ValidString(path) || utf8.RuneCountInString(path) > MaxPathRunes {
		return ErrInvalid
	}
	if strings.HasPrefix(path, "/") || strings.Contains(path, `\`) {
		return ErrInvalid
	}
	for _, r := range path {
		if unicode.IsControl(r) {
			return ErrInvalid
		}
	}
	for _, segment := range strings.Split(path, "/") {
		if segment == "" || segment == "." || segment == ".." {
			return ErrInvalid
		}
	}
	return nil
}

// NormalizeFiles 校验一组文件并整理成技能内容：
// 路径合法且不重复、没有「文件与目录同名」的冲突、≤ 100 个、合计 ≤ 5 MB；
// 根目录没有 SKILL.md 时，若所有文件都在唯一的一层子目录里且该目录下有 SKILL.md，则去掉这层前缀。
func NormalizeFiles(files []File) ([]File, error) {
	if len(files) > MaxFiles {
		return nil, ErrTooLarge
	}
	var total int64
	for _, file := range files {
		if err := ValidatePath(file.Path); err != nil {
			return nil, err
		}
		total += int64(len(file.Content))
		if total > MaxTotalBytes {
			return nil, ErrTooLarge
		}
	}
	files = stripSingleRoot(files)
	seen := make(map[string]struct{}, len(files))
	dirs := map[string]struct{}{}
	hasEntry := false
	for _, file := range files {
		if _, dup := seen[file.Path]; dup {
			return nil, ErrInvalid
		}
		seen[file.Path] = struct{}{}
		if file.Path == SkillFile {
			hasEntry = true
		}
		for i := 0; i < len(file.Path); i++ {
			if file.Path[i] == '/' {
				dirs[file.Path[:i]] = struct{}{}
			}
		}
	}
	for path := range seen {
		if _, clash := dirs[path]; clash {
			return nil, ErrInvalid
		}
	}
	if !hasEntry {
		return nil, ErrInvalid
	}
	return files, nil
}

// stripSingleRoot 在根目录没有 SKILL.md、且所有文件都在同一个顶层目录下并包含 <目录>/SKILL.md 时去掉该前缀。
func stripSingleRoot(files []File) []File {
	if len(files) == 0 {
		return files
	}
	for _, file := range files {
		if file.Path == SkillFile {
			return files
		}
	}
	root := ""
	for _, file := range files {
		head, _, found := strings.Cut(file.Path, "/")
		if !found || (root != "" && head != root) {
			return files
		}
		root = head
	}
	found := false
	for _, file := range files {
		if file.Path == root+"/"+SkillFile {
			found = true
		}
	}
	if !found {
		return files
	}
	stripped := make([]File, len(files))
	for i, file := range files {
		stripped[i] = File{Path: strings.TrimPrefix(file.Path, root+"/"), Content: file.Content}
	}
	return stripped
}

// BuildPackage 校验文件并解析 SKILL.md，得到可保存的技能内容。
func BuildPackage(files []File) (Package, error) {
	files, err := NormalizeFiles(files)
	if err != nil {
		return Package{}, err
	}
	pkg := Package{Files: files}
	for _, file := range files {
		pkg.TotalBytes += int64(len(file.Content))
		if file.Path == SkillFile {
			parsed, err := ParseSkillMD(file.Content)
			if err != nil {
				return Package{}, err
			}
			pkg.Name, pkg.Description = parsed.Name, parsed.Description
		}
	}
	return pkg, nil
}
