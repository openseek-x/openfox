package runtime

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"openfox/internal/skillpack"
)

// WriteSkills 把技能写入独立运行目录的 skills/<name>/（目录 0700、文件 0600）。
// 写前校验名称与路径（拒绝绝对路径、..、空段、反斜杠、控制字符）；文件用 O_EXCL 创建，不跟随符号链接，
// 中间目录由本函数逐级创建并确认不是符号链接。调用方须在 Run 结束时删除整个隔离目录。
// 错误信息只含技能名，不含内容。
func WriteSkills(home string, skills []Skill) error {
	if len(skills) == 0 {
		return nil
	}
	root := filepath.Join(home, "skills")
	if err := mkdirPrivate(root); err != nil {
		return fmt.Errorf("create skills directory: %w", err)
	}
	for _, skill := range skills {
		if !skillpack.ValidName(skill.Name) {
			return fmt.Errorf("%w: invalid skill name", ErrInvalidRequest)
		}
		if len(skill.Files) == 0 || len(skill.Files) > skillpack.MaxFiles {
			return fmt.Errorf("%w: skill %q has an invalid file count", ErrInvalidRequest, skill.Name)
		}
		skillDir := filepath.Join(root, skill.Name)
		// 同名（含大小写不同在不区分大小写的文件系统上）会使 Mkdir 失败，不会覆盖已有技能。
		if err := mkdirPrivate(skillDir); err != nil {
			return fmt.Errorf("create skill %q directory: %w", skill.Name, err)
		}
		var total int64
		for _, file := range skill.Files {
			if total += int64(len(file.Content)); total > skillpack.MaxBytes {
				return fmt.Errorf("%w: skill %q is too large", ErrInvalidRequest, skill.Name)
			}
			if err := WriteSkillFile(skillDir, file); err != nil {
				return fmt.Errorf("write skill %q: %w", skill.Name, err)
			}
		}
	}
	return nil
}

func WriteSkillFile(skillDir string, file SkillFile) error {
	if !skillpack.ValidPath(file.Path) {
		return fmt.Errorf("%w: invalid skill file path", ErrInvalidRequest)
	}
	segments := strings.Split(file.Path, "/")
	dir := skillDir
	for _, segment := range segments[:len(segments)-1] {
		dir = filepath.Join(dir, segment)
		info, err := os.Lstat(dir)
		switch {
		case err == nil:
			if !info.IsDir() {
				return errors.New("path component is not a directory")
			}
		case errors.Is(err, os.ErrNotExist):
			if err := mkdirPrivate(dir); err != nil {
				return err
			}
		default:
			return err
		}
	}
	target := filepath.Join(dir, segments[len(segments)-1])
	// O_EXCL 与 O_CREATE 同用时不跟随最后一级符号链接，已存在的路径一律失败。
	handle, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if _, err := handle.Write(file.Content); err != nil {
		_ = handle.Close()
		return err
	}
	if err := handle.Chmod(0o600); err != nil {
		_ = handle.Close()
		return err
	}
	return handle.Close()
}

// mkdirPrivate 创建单级目录（父目录必须已存在，目录已存在则失败）并设为 0700。
func mkdirPrivate(path string) error {
	if err := os.Mkdir(path, 0o700); err != nil {
		return err
	}
	return os.Chmod(path, 0o700)
}
