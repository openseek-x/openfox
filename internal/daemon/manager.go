// Package daemon contains the user-level WP-05 local service boundary.
// It deliberately has no HTTP listener; the management interface is a
// user-private Unix socket and the service is owned by launchd on macOS.
package daemon

import (
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

const LaunchAgentLabel = "io.openfox.daemon"

type CommandRunner func(context.Context, string, ...string) error

type Manager struct {
	SocketPath      string
	LaunchAgentPath string
	// LogPath 是 launchd 重定向 stdout / stderr 的文件（~/Library/Logs/OpenFox/daemon.log）。
	LogPath    string
	RunCommand CommandRunner
	UserID     int
	// GOOS 为空时取 runtime.GOOS；测试可替换以在非 macOS 上验证 plist。
	GOOS string
}

func DefaultManager() (Manager, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return Manager{}, fmt.Errorf("resolve home directory: %w", err)
	}
	return Manager{
		SocketPath:      filepath.Join(home, "Library", "Application Support", "OpenFox", "daemon.sock"),
		LaunchAgentPath: filepath.Join(home, "Library", "LaunchAgents", LaunchAgentLabel+".plist"),
		LogPath:         filepath.Join(home, "Library", "Logs", "OpenFox", "daemon.log"),
		RunCommand:      defaultCommandRunner,
		UserID:          os.Getuid(),
	}, nil
}

func (m Manager) Install(binaryPath string) error {
	return m.InstallService(binaryPath, "")
}

// InstallService 写入 launchd plist。configPath 非空时服务以 `daemon run --config <configPath>` 启动
// （远程模式），否则只带 --socket（本地模式，兼容旧行为）。stdout / stderr 写入 LogPath。
func (m Manager) InstallService(binaryPath, configPath string) error {
	goos := m.GOOS
	if goos == "" {
		goos = runtime.GOOS
	}
	if goos != "darwin" {
		return errors.New("daemon service installation is supported only on macOS")
	}
	binaryPath, err := filepath.Abs(strings.TrimSpace(binaryPath))
	if err != nil {
		return fmt.Errorf("resolve daemon binary: %w", err)
	}
	info, err := os.Stat(binaryPath)
	if err != nil || !info.Mode().IsRegular() {
		return fmt.Errorf("daemon binary is not a regular file: %w", err)
	}
	if m.LaunchAgentPath == "" || m.SocketPath == "" {
		return errors.New("daemon paths are required")
	}
	if configPath != "" && !filepath.IsAbs(configPath) {
		return errors.New("daemon config path must be absolute")
	}
	if err := os.MkdirAll(filepath.Dir(m.LaunchAgentPath), 0o700); err != nil {
		return fmt.Errorf("create launch agent directory: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(m.SocketPath), 0o700); err != nil {
		return fmt.Errorf("create daemon state directory: %w", err)
	}
	if m.LogPath != "" {
		if err := os.MkdirAll(filepath.Dir(m.LogPath), 0o700); err != nil {
			return fmt.Errorf("create daemon log directory: %w", err)
		}
	}
	arguments := []string{binaryPath, "daemon", "run", "--socket", m.SocketPath}
	if configPath != "" {
		arguments = append(arguments, "--config", configPath)
	}
	contents, err := launchAgentPlist(arguments, m.LogPath)
	if err != nil {
		return err
	}
	return WritePrivateFile(m.LaunchAgentPath, contents)
}

// Load 把（可能已更新的）plist 载入 launchd：先 bootout 旧实例（未加载时的失败忽略），再 bootstrap。
// launchd 的 RunAtLoad 会在载入后启动服务。
func (m Manager) Load(ctx context.Context) error {
	_ = m.launchctl(ctx, "bootout", m.domainLabel())
	return m.launchctl(ctx, "bootstrap", fmt.Sprintf("gui/%d", m.UserID), m.LaunchAgentPath)
}

// Start 启动服务：已载入时 kickstart，未载入（例如被 stop / bootout 过）时重新 bootstrap plist。
func (m Manager) Start(ctx context.Context) error {
	if m.launchctl(ctx, "kickstart", m.domainLabel()) == nil {
		return nil
	}
	return m.launchctl(ctx, "bootstrap", fmt.Sprintf("gui/%d", m.UserID), m.LaunchAgentPath)
}

func (m Manager) Stop(ctx context.Context) error {
	return m.launchctl(ctx, "bootout", m.domainLabel())
}

// Restart 重启服务（例如 CLI 二进制更新后）：已载入时 kickstart -k 杀掉并重启；未载入时重新 bootstrap。
// 不能先 bootout 再 kickstart：bootout 会把服务从 launchd 卸载，之后 kickstart 必然失败，服务就停在未载入状态。
func (m Manager) Restart(ctx context.Context) error {
	if m.launchctl(ctx, "kickstart", "-k", m.domainLabel()) == nil {
		return nil
	}
	return m.launchctl(ctx, "bootstrap", fmt.Sprintf("gui/%d", m.UserID), m.LaunchAgentPath)
}

func (m Manager) QueryStatus(ctx context.Context) (Status, error) {
	return QuerySocket(ctx, m.SocketPath)
}

func (m Manager) domainLabel() string {
	return fmt.Sprintf("gui/%d/%s", m.UserID, LaunchAgentLabel)
}

func (m Manager) launchctl(ctx context.Context, args ...string) error {
	if m.RunCommand == nil {
		return errors.New("launchctl command runner is not configured")
	}
	return m.RunCommand(ctx, "launchctl", args...)
}

func defaultCommandRunner(ctx context.Context, name string, args ...string) error {
	return exec.CommandContext(ctx, name, args...).Run()
}

func launchAgentPlist(arguments []string, logPath string) ([]byte, error) {
	escape := func(value string) (string, error) {
		var escaped strings.Builder
		if err := xml.EscapeText(&escaped, []byte(value)); err != nil {
			return "", err
		}
		return escaped.String(), nil
	}
	var program strings.Builder
	for _, argument := range arguments {
		escaped, err := escape(argument)
		if err != nil {
			return nil, err
		}
		program.WriteString("<string>" + escaped + "</string>")
	}
	logs := ""
	if logPath != "" {
		escaped, err := escape(logPath)
		if err != nil {
			return nil, err
		}
		logs = "  <key>StandardOutPath</key><string>" + escaped + "</string>\n" +
			"  <key>StandardErrorPath</key><string>" + escaped + "</string>\n"
	}
	contents := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key><string>%s</string>
  <key>ProgramArguments</key>
  <array>%s</array>
%s  <key>RunAtLoad</key><true/>
  <key>KeepAlive</key><true/>
  <key>ProcessType</key><string>Background</string>
</dict>
</plist>
`, LaunchAgentLabel, program.String(), logs)
	return []byte(contents), nil
}
