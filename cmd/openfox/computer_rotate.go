package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"openfox/internal/daemon"
)

// runComputerRotate 实现 `openfox computer rotate`（阶段 12e，D-01）：
//   - daemon 正在运行：经本地控制 socket 交给 daemon 执行，daemon 在进程内切换令牌并让 WSS 连接换用新会话；
//   - daemon 未运行：CLI 直接执行同一套两步式轮换（先落盘新密钥与配置，再请求中心吊销旧密钥）。
//
// 输出只含公钥摘要编号（key_id），不含私钥、令牌与签名。
func runComputerRotate(args []string, deps computerDeps) error {
	flags := flag.NewFlagSet("computer rotate", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	configDir := flags.String("config-dir", "", "configuration directory (default: platform config directory)")
	offline := flags.Bool("offline", false, "rotate without the running daemon (the daemon must be stopped)")
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 {
		return errors.New("usage: openfox computer rotate [--config-dir <dir>] [--offline]")
	}
	dir := strings.TrimSpace(*configDir)
	if dir == "" {
		var err error
		if dir, err = daemon.DefaultConfigDir(); err != nil {
			return err
		}
	}
	dir, err := filepath.Abs(dir)
	if err != nil {
		return err
	}
	configPath := filepath.Join(dir, daemon.DeviceConfigFileName)
	config, err := daemon.LoadDeviceConfig(configPath)
	if errors.Is(err, os.ErrNotExist) {
		return errors.New("this computer is not registered; run `openfox computer register` first")
	}
	if err != nil {
		return fmt.Errorf("read computer configuration: %w", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute+10*time.Second)
	defer cancel()
	socketPath := filepath.Join(dir, "daemon.sock")
	if *offline {
		// 离线轮换会改写本机密钥与配置；运行中的 daemon 仍持有旧密钥，确认后它的会话与续期都会失效。
		probe, cancelProbe := context.WithTimeout(ctx, 2*time.Second)
		_, err := daemon.QuerySocket(probe, socketPath)
		cancelProbe()
		if err == nil {
			return errors.New("the daemon is running; run `openfox computer rotate` without --offline so the daemon rotates its own key")
		}
	}
	if !*offline {
		result, err := daemon.RequestKeyRotation(ctx, socketPath)
		switch {
		case err == nil:
			printRotation(deps.stdout, result)
			return nil
		case errors.Is(err, daemon.ErrControlUnsupported):
			return errors.New("the running daemon does not support key rotation; restart it with this version of openfox, or stop it and run `openfox computer rotate --offline`")
		case !daemonNotRunning(err):
			return fmt.Errorf("key rotation failed (the previous key stays valid): %w", err)
		}
	}
	client := &http.Client{Timeout: 30 * time.Second}
	if deps.httpClient != nil {
		client = deps.httpClient
	}
	rotator := &daemon.KeyRotator{ConfigPath: configPath, Client: daemon.AuthClient{BaseURL: daemon.APIBaseURL(config.CenterURL), HTTP: client}}
	result, err := rotator.Rotate(ctx)
	if errors.Is(err, daemon.ErrKeyRotationUnsupported) {
		return errors.New("the OpenFox center does not support device key rotation yet")
	}
	if err != nil {
		return fmt.Errorf("key rotation failed (the previous key stays valid; run the command again to resume): %w", err)
	}
	printRotation(deps.stdout, result)
	return nil
}

// daemonNotRunning：socket 不存在或拒绝连接时视为 daemon 未运行。
func daemonNotRunning(err error) bool {
	return errors.Is(err, os.ErrNotExist) || errors.Is(err, syscall.ENOENT) || errors.Is(err, syscall.ECONNREFUSED)
}

func printRotation(out io.Writer, result daemon.KeyRotationResult) {
	fmt.Fprintf(out, "key rotated: key_id=%s previous_key_id=%s resumed=%t\n", result.KeyID, result.PreviousKeyID, result.Resumed)
}
