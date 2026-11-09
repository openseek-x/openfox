package daemon

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// 本地设备配置（`openfox computer register` 写入，`openfox daemon run --config` 读取）。
// config.json、device-key、device-token 都是 0600 的普通文件，目录 0700；
// 写入一律“同目录临时文件 + rename”的原子替换，目标是符号链接时拒绝。
const (
	DeviceConfigVersion  = 1
	DeviceConfigFileName = "config.json"
	DeviceKeyFileName    = "device-key"
	DeviceTokenFileName  = "device-token"
	// 私钥文件格式：一行 `ed25519-seed:<base64url 32 字节种子>`。
	deviceKeyPrefix = "ed25519-seed:"
	// 配置文件上限：只有少量字段，超出视为损坏。
	maxConfigBytes = 64 * 1024
)

var (
	ErrConfigInvalid = errors.New("daemon: device config is invalid")
	ErrConfigExists  = errors.New("daemon: device config already exists")
	ErrUnsafeFile    = errors.New("daemon: credential file is not a user-private regular file")
)

// DeviceConfig 是 config.json 的内容。路径字段都是绝对路径。
type DeviceConfig struct {
	Version        int    `json:"version"`
	CenterURL      string `json:"center_url"`
	WSSURL         string `json:"wss_url"`
	DeviceID       string `json:"device_id"`
	OrganizationID string `json:"organization_id"`
	KeyID          string `json:"key_id"`
	PrivateKeyFile string `json:"private_key_file"`
	TokenFile      string `json:"token_file"`
	WorkspaceRoot  string `json:"workspace_root"`
	CenterEpoch    int64  `json:"center_epoch"`
	// 以下为可选字段：launchd 只传 --config，执行所需的本机设置也放在这里。
	SupportedVersions []string          `json:"supported_versions,omitempty"`
	RuntimeExecutable string            `json:"runtime_executable,omitempty"`
	DirectoryRoots    map[string]string `json:"directory_roots,omitempty"`
}

// DefaultConfigDir 返回默认配置目录：macOS 为 ~/Library/Application Support/OpenFox，
// 其他平台为 $XDG_CONFIG_HOME/openfox（默认 ~/.config/openfox）。
func DefaultConfigDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return "", fmt.Errorf("resolve home directory: %w", err)
	}
	if runtime.GOOS == "darwin" {
		return filepath.Join(home, "Library", "Application Support", "OpenFox"), nil
	}
	if xdg := strings.TrimSpace(os.Getenv("XDG_CONFIG_HOME")); xdg != "" && filepath.IsAbs(xdg) {
		return filepath.Join(xdg, "openfox"), nil
	}
	return filepath.Join(home, ".config", "openfox"), nil
}

// APIBaseURL 返回中心 API 根（中心路由挂在 /api 前缀下）。
func APIBaseURL(centerURL string) string {
	return strings.TrimRight(centerURL, "/") + "/api"
}

// CenterWSSURL 由中心 URL 推导设备 WSS 端点：https → wss，http → ws（仅本地开发）。
func CenterWSSURL(centerURL string) (string, error) {
	parsed, err := url.Parse(strings.TrimRight(strings.TrimSpace(centerURL), "/"))
	if err != nil || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", ErrConfigInvalid
	}
	switch parsed.Scheme {
	case "https":
		parsed.Scheme = "wss"
	case "http":
		parsed.Scheme = "ws"
	default:
		return "", ErrConfigInvalid
	}
	return parsed.Scheme + "://" + parsed.Host + strings.TrimRight(parsed.Path, "/") + "/api/v1/device/connect", nil
}

// Validate 检查字段完整性与路径形状；不访问文件系统。
func (c DeviceConfig) Validate() error {
	if c.Version != DeviceConfigVersion || c.CenterEpoch <= 0 {
		return fmt.Errorf("%w: unsupported version or center epoch", ErrConfigInvalid)
	}
	for name, value := range map[string]string{
		"center_url": c.CenterURL, "wss_url": c.WSSURL, "device_id": c.DeviceID, "organization_id": c.OrganizationID,
		"key_id": c.KeyID, "private_key_file": c.PrivateKeyFile, "token_file": c.TokenFile, "workspace_root": c.WorkspaceRoot,
	} {
		if strings.TrimSpace(value) == "" || strings.ContainsAny(value, "\x00\r\n") {
			return fmt.Errorf("%w: %s is required", ErrConfigInvalid, name)
		}
	}
	for name, value := range map[string]string{
		"private_key_file": c.PrivateKeyFile, "token_file": c.TokenFile, "workspace_root": c.WorkspaceRoot,
	} {
		if !filepath.IsAbs(value) || filepath.Clean(value) != value {
			return fmt.Errorf("%w: %s must be a clean absolute path", ErrConfigInvalid, name)
		}
	}
	if parsed, err := url.Parse(c.WSSURL); err != nil || (parsed.Scheme != "ws" && parsed.Scheme != "wss") || parsed.Host == "" {
		return fmt.Errorf("%w: wss_url must be a ws(s) URL", ErrConfigInvalid)
	}
	if parsed, err := url.Parse(c.CenterURL); err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return fmt.Errorf("%w: center_url must be an http(s) URL", ErrConfigInvalid)
	}
	return nil
}

// LoadDeviceConfig 读取并校验 config.json；文件必须是本用户私有的普通文件（不跟随符号链接）。
func LoadDeviceConfig(path string) (DeviceConfig, error) {
	contents, err := readPrivateFile(path, maxConfigBytes)
	if err != nil {
		return DeviceConfig{}, err
	}
	var config DeviceConfig
	decoder := json.NewDecoder(strings.NewReader(string(contents)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&config); err != nil {
		return DeviceConfig{}, fmt.Errorf("%w: %s is not valid JSON", ErrConfigInvalid, filepath.Base(path))
	}
	if err := config.Validate(); err != nil {
		return DeviceConfig{}, err
	}
	return config, nil
}

// WriteDeviceConfig 以 0600 原子写入 config.json。
func WriteDeviceConfig(path string, config DeviceConfig) error {
	if err := config.Validate(); err != nil {
		return err
	}
	encoded, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return err
	}
	return WritePrivateFile(path, append(encoded, '\n'))
}

// WritePrivateFile 原子写入 0600 文件：目录不存在时以 0700 创建；目标已存在且不是普通文件
// （例如符号链接）时拒绝；临时文件以 O_EXCL 创建，rename 替换目录项本身而不跟随链接。
func WritePrivateFile(path string, contents []byte) error {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return fmt.Errorf("%w: path must be a clean absolute path", ErrUnsafeFile)
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create directory: %w", err)
	}
	if info, err := os.Lstat(dir); err != nil || !info.IsDir() {
		return fmt.Errorf("%w: parent is not a directory", ErrUnsafeFile)
	}
	if info, err := os.Lstat(path); err == nil {
		if !info.Mode().IsRegular() {
			return fmt.Errorf("%w: %s exists and is not a regular file", ErrUnsafeFile, filepath.Base(path))
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	temp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	name := temp.Name()
	fail := func(err error) error {
		_ = temp.Close()
		_ = os.Remove(name)
		return err
	}
	if err := temp.Chmod(0o600); err != nil {
		return fail(err)
	}
	if _, err := temp.Write(contents); err != nil {
		return fail(err)
	}
	if err := temp.Sync(); err != nil {
		return fail(err)
	}
	if err := temp.Close(); err != nil {
		_ = os.Remove(name)
		return err
	}
	if err := os.Rename(name, path); err != nil {
		_ = os.Remove(name)
		return err
	}
	return nil
}

// readPrivateFile 读取本用户私有的普通文件（拒绝符号链接与组/其他可读权限）。
func readPrivateFile(path string, limit int64) ([]byte, error) {
	if strings.TrimSpace(path) == "" || !filepath.IsAbs(path) {
		return nil, fmt.Errorf("%w: path must be absolute", ErrUnsafeFile)
	}
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 {
		return nil, fmt.Errorf("%w: %s", ErrUnsafeFile, filepath.Base(path))
	}
	if info.Size() > limit {
		return nil, fmt.Errorf("%w: %s is too large", ErrUnsafeFile, filepath.Base(path))
	}
	return os.ReadFile(path)
}

// GenerateDeviceKey 生成 ed25519 密钥对，返回私钥与 key_id（公钥 sha256 前 16 字节 hex）。
func GenerateDeviceKey() (ed25519.PrivateKey, string, error) {
	_, privateKey, err := ed25519.GenerateKey(nil)
	if err != nil {
		return nil, "", err
	}
	return privateKey, DeviceKeyID(privateKey.Public().(ed25519.PublicKey)), nil
}

// DeviceKeyID 是公钥 sha256 前 16 字节的 hex。
func DeviceKeyID(publicKey ed25519.PublicKey) string {
	sum := sha256.Sum256(publicKey)
	return hex.EncodeToString(sum[:16])
}

// WriteDeviceKey 以 0600 原子写入私钥种子。
func WriteDeviceKey(path string, privateKey ed25519.PrivateKey) error {
	if len(privateKey) != ed25519.PrivateKeySize {
		return ErrConfigInvalid
	}
	line := deviceKeyPrefix + base64.RawURLEncoding.EncodeToString(privateKey.Seed()) + "\n"
	return WritePrivateFile(path, []byte(line))
}

// ReadDeviceKey 读取私钥文件；格式或权限不对时返回不含文件内容的错误。
func ReadDeviceKey(path string) (ed25519.PrivateKey, error) {
	contents, err := readPrivateFile(path, 1024)
	if err != nil {
		return nil, err
	}
	encoded, ok := strings.CutPrefix(strings.TrimSpace(string(contents)), deviceKeyPrefix)
	if !ok {
		return nil, fmt.Errorf("%w: device key file has an unknown format", ErrConfigInvalid)
	}
	seed, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil || len(seed) != ed25519.SeedSize {
		return nil, fmt.Errorf("%w: device key file has an unknown format", ErrConfigInvalid)
	}
	return ed25519.NewKeyFromSeed(seed), nil
}
