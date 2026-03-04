package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadUserConfig_FileNotFound(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	cfg, err := loadUserConfig()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Features != nil {
		t.Fatalf("expected nil features, got %v", cfg.Features)
	}
	if cfg.Dotfiles != nil {
		t.Fatalf("expected nil dotfiles, got %v", cfg.Dotfiles)
	}
	if cfg.Mounts != nil {
		t.Fatalf("expected nil mounts, got %v", cfg.Mounts)
	}
}

func TestLoadUserConfig_ValidConfig(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)

	configDir := filepath.Join(dir, "devc")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatal(err)
	}

	content := `{
  "features": {
    "ghcr.io/devcontainers/features/github-cli:1": {},
    "ghcr.io/jungaretti/features/ripgrep:1": {}
  },
  "dotfiles": ["~/.config/nvim", "~/.ssh"],
  "mounts": [{"source": "~/work", "target": "~/work"}]
}`
	if err := os.WriteFile(filepath.Join(configDir, "config.json"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := loadUserConfig()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(cfg.Features) != 2 {
		t.Fatalf("expected 2 features, got %d", len(cfg.Features))
	}
	if _, ok := cfg.Features["ghcr.io/devcontainers/features/github-cli:1"]; !ok {
		t.Fatal("missing github-cli feature")
	}
	if len(cfg.Dotfiles) != 2 {
		t.Fatalf("expected 2 dotfiles, got %d", len(cfg.Dotfiles))
	}
	if cfg.Dotfiles[0] != "~/.config/nvim" {
		t.Fatalf("expected ~/.config/nvim, got %s", cfg.Dotfiles[0])
	}
	if len(cfg.Mounts) != 1 {
		t.Fatalf("expected 1 mount, got %d", len(cfg.Mounts))
	}
	if cfg.Mounts[0].Source != "~/work" {
		t.Fatalf("expected ~/work source, got %s", cfg.Mounts[0].Source)
	}
}

func TestLoadUserConfig_ParseError(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)

	configDir := filepath.Join(dir, "devc")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(configDir, "config.json"), []byte("{invalid json"), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := loadUserConfig()
	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}
}

func TestExpandHome(t *testing.T) {
	home := os.Getenv("HOME")

	tests := []struct {
		input string
		want  string
	}{
		{"~/.config/nvim", filepath.Join(home, ".config/nvim")},
		{"~/.ssh", filepath.Join(home, ".ssh")},
		{"/absolute/path", "/absolute/path"},
		{"relative/path", "relative/path"},
	}
	for _, tt := range tests {
		got := expandHome(tt.input)
		if got != tt.want {
			t.Errorf("expandHome(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestDotfileRelPath(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"~/.config/nvim", ".config/nvim"},
		{"~/.ssh", ".ssh"},
		{"~/.claude.json", ".claude.json"},
		{"/absolute/path/foo", "foo"},
	}
	for _, tt := range tests {
		got := dotfileRelPath(tt.input)
		if got != tt.want {
			t.Errorf("dotfileRelPath(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestBuildHostMounts(t *testing.T) {
	home := os.Getenv("HOME")

	ucfg := &userConfig{
		Dotfiles: []string{"~/.config/nvim", "~/.ssh"},
		Mounts:   []mountEntry{{Source: "~/work", Target: "~/work"}},
	}

	mounts := buildHostMounts(ucfg)

	// 2 dotfiles + 1 user mount + 1 credentials = 4
	if len(mounts) != 4 {
		t.Fatalf("expected 4 mounts, got %d: %+v", len(mounts), mounts)
	}

	// Dotfile: ~/.config/nvim → /opt/devc-dotfiles/.config/nvim
	if mounts[0].source != filepath.Join(home, ".config/nvim") {
		t.Errorf("mount[0].source = %q", mounts[0].source)
	}
	if mounts[0].target != "/opt/devc-dotfiles/.config/nvim" {
		t.Errorf("mount[0].target = %q", mounts[0].target)
	}

	// Dotfile: ~/.ssh → /opt/devc-dotfiles/.ssh
	if mounts[1].source != filepath.Join(home, ".ssh") {
		t.Errorf("mount[1].source = %q", mounts[1].source)
	}
	if mounts[1].target != "/opt/devc-dotfiles/.ssh" {
		t.Errorf("mount[1].target = %q", mounts[1].target)
	}

	// User mount: ~/work → ~/work
	if mounts[2].source != filepath.Join(home, "work") {
		t.Errorf("mount[2].source = %q", mounts[2].source)
	}
	if mounts[2].target != filepath.Join(home, "work") {
		t.Errorf("mount[2].target = %q", mounts[2].target)
	}

	// Credentials always last
	if mounts[3].source != "/tmp/devc-credentials" {
		t.Errorf("mount[3].source = %q", mounts[3].source)
	}
}

func TestBuildHostMounts_EmptyConfig(t *testing.T) {
	ucfg := &userConfig{}
	mounts := buildHostMounts(ucfg)

	// Only credentials mount
	if len(mounts) != 1 {
		t.Fatalf("expected 1 mount, got %d: %+v", len(mounts), mounts)
	}
	if mounts[0].source != "/tmp/devc-credentials" {
		t.Errorf("expected credentials mount, got %q", mounts[0].source)
	}
}
