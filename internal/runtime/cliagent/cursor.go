package cliagent

import (
	"context"
	"fmt"
	"os"
	"regexp"
	"time"

	"openfox/internal/runtime"
	"openfox/internal/runtime/discovery"
)

// CursorDiscovery 只持有发现和路径设置能力，不实现 runtime.Adapter 的执行接口。
type CursorDiscovery struct{ finder *Adapter }

var cursorVersion = regexp.MustCompile(`^\s*(\d{4}\.\d{2}\.\d{2})(?:-[[:alnum:].-]+)?\s*$`)

func NewCursorDiscovery(executable string) *CursorDiscovery {
	finder := &Adapter{Executable: executable, ProbeTimeout: 5 * time.Second, proto: protocol{
		name: "cursor", display: "Cursor", executables: []string{"cursor-agent"}, envPath: "OPENFOX_CURSOR_PATH",
		probe: cursorVersionProbe,
	}}
	return &CursorDiscovery{finder: finder}
}

func cursorVersionProbe(ctx context.Context, _ *Adapter, path string) (string, error) {
	env := map[string]string{"HOME": os.Getenv("HOME"), "PATH": discovery.ExecutablePath(os.Getenv("PATH"), path)}
	out, err := runProbe(ctx, path, env, "--version")
	if err != nil {
		return "", err
	}
	match := cursorVersion.FindStringSubmatch(out)
	if match == nil {
		return "", fmt.Errorf("%w: invalid Cursor CLI version", runtime.ErrUnsupported)
	}
	return match[1], nil
}

func (c *CursorDiscovery) Detect(ctx context.Context) (runtime.Capabilities, error) {
	item, version, err := c.finder.locate(ctx)
	cap := runtime.Capabilities{Runtime: "cursor", Login: runtime.LoginUnknown, Executable: item.Path, DiscoveryOnly: true}
	if err == nil {
		cap.Version = version
		cap.DiscoverySource = item.Source
		cap.Reason = "No execution adapter is available for Cursor"
	}
	return cap, err
}

func (c *CursorDiscovery) ListModels(context.Context) ([]Model, error) { return nil, nil }

func (c *CursorDiscovery) UseDevicePath(path string) { c.finder.UseDevicePath(path) }

func (c *CursorDiscovery) ValidateDevicePath(ctx context.Context, path string) (string, error) {
	if path == "" {
		return "", nil
	}
	known, err := discovery.Local().KnownPath(ctx, c.finder.tool(), c.finder.Executable, path)
	if err != nil {
		return "", err
	}
	_, err = NewCursorDiscovery(known).Detect(ctx)
	return known, err
}
