package cliagent

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDevicePathRejectsDifferentProviderWithNumericVersion(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pi")
	if err := os.WriteFile(path, []byte("#!/bin/sh\necho 1.2.3\n"), 0700); err != nil {
		t.Fatal(err)
	}
	adapter := NewPi(path)
	if _, err := adapter.Detect(t.Context()); err == nil {
		t.Fatal("探测应拒绝其他供应商的同名 CLI")
	}
	if _, err := adapter.ValidateDevicePath(t.Context(), path); err == nil {
		t.Fatal("拒绝只有版本号、缺少 Pi 协议的候选")
	}
}
