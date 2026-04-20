package meta

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/closer/devc/internal/config"
)

func TestBuildContainerMeta(t *testing.T) {
	ws := config.Workspace{Dir: "/home/user/myproject", Name: "myproject", ID: "myproject-abc12345"}
	cfg := &config.DevcontainerConfig{
		RemoteUser:            "vscode",
		RemoteWorkspaceFolder: "/workspaces/myproject",
		ContainerEnv:          map[string]string{"FOO": "bar"},
	}
	features := map[string]map[string]interface{}{
		"ghcr.io/devcontainers/features/node:1": {"version": "18"},
	}
	ports := []string{"3000:3000", "5432:5432"}
	dotfiles := []string{"~/.config/nvim", "~/.ssh"}

	meta := BuildContainerMeta(ws, cfg, ports, features, dotfiles, "image", "devc-myproject:abc123", "v0.1.0")

	if meta.Project != "myproject" {
		t.Fatalf("Project = %q, want %q", meta.Project, "myproject")
	}
	if meta.Mode != "image" {
		t.Fatalf("Mode = %q, want %q", meta.Mode, "image")
	}
	if meta.RemoteUser != "vscode" {
		t.Fatalf("RemoteUser = %q, want %q", meta.RemoteUser, "vscode")
	}
	if meta.Image != "devc-myproject:abc123" {
		t.Fatalf("Image = %q, want %q", meta.Image, "devc-myproject:abc123")
	}
	if len(meta.Ports) != 2 {
		t.Fatalf("Ports count = %d, want 2", len(meta.Ports))
	}
	if len(meta.Features) != 1 {
		t.Fatalf("Features count = %d, want 1", len(meta.Features))
	}
	if len(meta.Dotfiles) != 2 {
		t.Fatalf("Dotfiles count = %d, want 2", len(meta.Dotfiles))
	}
	if meta.ContainerEnv["FOO"] != "bar" {
		t.Fatalf("ContainerEnv[FOO] = %q, want %q", meta.ContainerEnv["FOO"], "bar")
	}
	if meta.CreatedAt == "" {
		t.Fatal("CreatedAt should not be empty")
	}
}

func TestLoadContainerMeta(t *testing.T) {
	// Create a temp meta.json
	dir := t.TempDir()
	metaFile := dir + "/meta.json"

	meta := ContainerMeta{
		Version:        "v0.1.0",
		Project:        "testproject",
		WorkspaceMount: "/workspaces/testproject",
		RemoteUser:     "vscode",
		Mode:           "image",
	}
	data, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(metaFile, data, 0o644); err != nil {
		t.Fatal(err)
	}

	// Can't test LoadContainerMeta directly since it reads from a fixed path,
	// but we can test the JSON round-trip
	var loaded ContainerMeta
	if err := json.Unmarshal(data, &loaded); err != nil {
		t.Fatal(err)
	}
	if loaded.Project != "testproject" {
		t.Fatalf("Project = %q, want %q", loaded.Project, "testproject")
	}
}

func TestIsInsideContainer(t *testing.T) {
	orig := os.Getenv(DevcContainerEnv)
	defer func() { _ = os.Setenv(DevcContainerEnv, orig) }()

	_ = os.Setenv(DevcContainerEnv, "1")
	if !IsInsideContainer() {
		t.Fatal("should return true when DEVC_CONTAINER=1")
	}

	_ = os.Unsetenv(DevcContainerEnv)
	if IsInsideContainer() {
		t.Fatal("should return false when DEVC_CONTAINER is not set")
	}
}
