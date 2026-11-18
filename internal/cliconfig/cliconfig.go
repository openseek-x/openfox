// Package cliconfig 保存协作 CLI 的本地配置：profile 列表与个人访问令牌（PAT）。
//
// 目录布局（Dir 即 <DefaultConfigDir>/cli，与设备 config.json 完全分开）：
//
//	profiles.json        profile 列表与当前 profile（0600，不含令牌）
//	tokens/<profile>     令牌明文（0600），目录 0700
//
// 所有写入都是原子的（同目录临时文件 + rename），并拒绝符号链接。
package cliconfig

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

const (
	profilesFile = "profiles.json"
	tokensDir    = "tokens"
	// maxFileBytes 限制读取大小，防止被替换成巨型文件。
	maxFileBytes = 1 << 20
)

var (
	// ErrNoToken 表示该 profile 没有保存令牌。
	ErrNoToken = errors.New("no token stored for profile")

	profileNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$`)
)

// Profile 是一个服务器连接的非敏感部分。
type Profile struct {
	Server string `json:"server"`
	Org    string `json:"org,omitempty"`
}

// File 是 profiles.json 的内容。
type File struct {
	Version  int                `json:"version"`
	Current  string             `json:"current,omitempty"`
	Profiles map[string]Profile `json:"profiles"`
}

// Store 读写 Dir 下的配置。
type Store struct{ Dir string }

// ValidateProfileName 只允许字母数字、下划线、连字符，避免路径穿越与隐藏文件。
func ValidateProfileName(name string) error {
	if !profileNamePattern.MatchString(name) {
		return fmt.Errorf("invalid profile name %q: use 1-64 letters, digits, '_' or '-'", name)
	}
	return nil
}

// Load 读取 profiles.json；文件不存在时返回空配置。
func (s Store) Load() (File, error) {
	empty := File{Version: 1, Profiles: map[string]Profile{}}
	path := filepath.Join(s.Dir, profilesFile)
	data, err := readRegular(path)
	if errors.Is(err, os.ErrNotExist) {
		return empty, nil
	}
	if err != nil {
		return empty, err
	}
	var file File
	if err := json.Unmarshal(data, &file); err != nil {
		return empty, fmt.Errorf("parse %s: %w", path, err)
	}
	if file.Profiles == nil {
		file.Profiles = map[string]Profile{}
	}
	return file, nil
}

// Save 原子写入 profiles.json（0600）。
func (s Store) Save(file File) error {
	file.Version = 1
	if file.Profiles == nil {
		file.Profiles = map[string]Profile{}
	}
	data, err := json.MarshalIndent(file, "", "  ")
	if err != nil {
		return err
	}
	if err := s.ensureDir(s.Dir); err != nil {
		return err
	}
	return writeAtomic(filepath.Join(s.Dir, profilesFile), append(data, '\n'))
}

// WriteToken 保存 profile 的令牌（0600）。
func (s Store) WriteToken(profile, token string) error {
	if err := ValidateProfileName(profile); err != nil {
		return err
	}
	if err := s.ensureDir(s.Dir); err != nil {
		return err
	}
	dir := filepath.Join(s.Dir, tokensDir)
	if err := s.ensureDir(dir); err != nil {
		return err
	}
	return writeAtomic(filepath.Join(dir, profile), []byte(token))
}

// ReadToken 读取令牌；文件权限宽于 0600 时拒绝使用，提示修正。
func (s Store) ReadToken(profile string) (string, error) {
	if err := ValidateProfileName(profile); err != nil {
		return "", err
	}
	path := filepath.Join(s.Dir, tokensDir, profile)
	if info, err := os.Lstat(path); err == nil && info.Mode().IsRegular() && info.Mode().Perm()&0o077 != 0 {
		return "", fmt.Errorf("token file %s is accessible by other users (mode %o); run: chmod 600 %s", path, info.Mode().Perm(), path)
	}
	data, err := readRegular(path)
	if errors.Is(err, os.ErrNotExist) {
		return "", ErrNoToken
	}
	if err != nil {
		return "", err
	}
	token := strings.TrimSpace(string(data))
	if token == "" {
		return "", ErrNoToken
	}
	return token, nil
}

// DeleteToken 删除本地令牌文件；不存在不算错误。
func (s Store) DeleteToken(profile string) error {
	if err := ValidateProfileName(profile); err != nil {
		return err
	}
	err := os.Remove(filepath.Join(s.Dir, tokensDir, profile))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// ensureDir 创建 0700 目录；已存在时必须是真实目录（不是符号链接），并收紧权限。
func (s Store) ensureDir(dir string) error {
	info, err := os.Lstat(dir)
	switch {
	case errors.Is(err, os.ErrNotExist):
		if err := os.MkdirAll(filepath.Dir(dir), 0o700); err != nil {
			return err
		}
		if err := os.Mkdir(dir, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
			return err
		}
		return os.Chmod(dir, 0o700)
	case err != nil:
		return err
	case info.Mode()&os.ModeSymlink != 0:
		return fmt.Errorf("%s is a symbolic link; refusing to use it", dir)
	case !info.IsDir():
		return fmt.Errorf("%s is not a directory", dir)
	}
	if info.Mode().Perm() != 0o700 {
		return os.Chmod(dir, 0o700)
	}
	return nil
}

// readRegular 读取普通文件，拒绝符号链接与超大文件。
func readRegular(path string) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%s is not a regular file (symbolic links are refused)", path)
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maxFileBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxFileBytes {
		return nil, fmt.Errorf("%s is too large", path)
	}
	return data, nil
}

// writeAtomic 写同目录临时文件（O_EXCL、0600）后 rename 覆盖；目标若是符号链接或非普通文件则拒绝。
func writeAtomic(path string, data []byte) error {
	if info, err := os.Lstat(path); err == nil && !info.Mode().IsRegular() {
		return fmt.Errorf("%s is not a regular file (symbolic links are refused)", path)
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	var suffix [6]byte
	if _, err := rand.Read(suffix[:]); err != nil {
		return err
	}
	temp := path + ".tmp-" + hex.EncodeToString(suffix[:])
	file, err := os.OpenFile(temp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	cleanup := func() { _ = os.Remove(temp) }
	if _, err := file.Write(data); err != nil {
		file.Close()
		cleanup()
		return err
	}
	if err := file.Sync(); err != nil {
		file.Close()
		cleanup()
		return err
	}
	if err := file.Close(); err != nil {
		cleanup()
		return err
	}
	if err := os.Rename(temp, path); err != nil {
		cleanup()
		return err
	}
	return nil
}
