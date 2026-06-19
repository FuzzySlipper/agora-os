package main

import (
	"testing"

	"github.com/patch/agora-os/internal/shellui"
)

func TestParseServeConfigDefaultsShellConfigDir(t *testing.T) {
	t.Setenv(shellConfigDirEnv, "")

	cfg, err := parseServeConfig(nil)
	if err != nil {
		t.Fatalf("parseServeConfig: %v", err)
	}
	if cfg.shellConfigDir != shellui.DefaultShellConfigDir {
		t.Fatalf("got shell config dir %q, want %q", cfg.shellConfigDir, shellui.DefaultShellConfigDir)
	}
}

func TestParseServeConfigUsesShellConfigDirEnv(t *testing.T) {
	t.Setenv(shellConfigDirEnv, "/tmp/agora-shell-env")

	cfg, err := parseServeConfig(nil)
	if err != nil {
		t.Fatalf("parseServeConfig: %v", err)
	}
	if cfg.shellConfigDir != "/tmp/agora-shell-env" {
		t.Fatalf("got shell config dir %q", cfg.shellConfigDir)
	}
}

func TestParseServeConfigFlagOverridesShellConfigDirEnv(t *testing.T) {
	t.Setenv(shellConfigDirEnv, "/tmp/agora-shell-env")

	cfg, err := parseServeConfig([]string{"--shell-config-dir", "/tmp/agora-shell-flag"})
	if err != nil {
		t.Fatalf("parseServeConfig: %v", err)
	}
	if cfg.shellConfigDir != "/tmp/agora-shell-flag" {
		t.Fatalf("got shell config dir %q", cfg.shellConfigDir)
	}
}

func TestParseServeConfigShellDevDirDefaultAndFlag(t *testing.T) {
	t.Setenv(shellConfigDirEnv, "")

	cfg, err := parseServeConfig(nil)
	if err != nil {
		t.Fatalf("parseServeConfig: %v", err)
	}
	if cfg.shellDevDir != "" {
		t.Fatalf("default shellDevDir = %q, want empty", cfg.shellDevDir)
	}

	cfg, err = parseServeConfig([]string{"--shell-dev-dir", "/home/dev/agora-os/shell/dist"})
	if err != nil {
		t.Fatalf("parseServeConfig: %v", err)
	}
	if cfg.shellDevDir != "/home/dev/agora-os/shell/dist" {
		t.Fatalf("got shellDevDir %q", cfg.shellDevDir)
	}
}
