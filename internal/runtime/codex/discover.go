package codex

import (
	"context"

	"openfox/internal/runtime"
	"openfox/internal/runtime/discovery"
)

var codexTool = discovery.Tool{Name: "codex", EnvKeys: []string{"OPENFOX_CODEX_PATH", "CODEX_PATH"}}

// withExecutableDir 把可执行文件所在目录放到 PATH 最前面，
// 使 nvm 等安装的 `#!/usr/bin/env node` 脚本能找到同目录的 node。
func withExecutableDir(environment map[string]string, executable string) map[string]string {
	current := environment["PATH"]
	if current == "" {
		current = "/usr/bin:/bin:/usr/sbin:/sbin"
	}
	environment["PATH"] = discovery.ExecutablePath(current, executable)
	return environment
}

// ValidateDevicePath 仅验证设备自己的候选清单中的 Codex。
func (a *Adapter) ValidateDevicePath(ctx context.Context, path string) (string, error) {
	if path == "" {
		return "", nil
	}
	known, err := discovery.Local().KnownPath(ctx, codexTool, a.Executable, path)
	if err != nil {
		return "", err
	}
	adapter := New(known)
	adapter.SupportedVersions = a.SupportedVersions
	report, err := adapter.Detect(ctx)
	if err != nil || report.Version == "unknown" || (len(a.SupportedVersions) > 0 && !contains(a.SupportedVersions, report.Version)) {
		return "", runtime.ErrUnavailable
	}
	return known, nil
}

func (a *Adapter) UseDevicePath(path string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.devicePath = path
}
