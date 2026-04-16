package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func writeDevcontainerJSON(t *testing.T, dir, content string) workspace {
	t.Helper()
	dcDir := filepath.Join(dir, ".devcontainer")
	if err := os.MkdirAll(dcDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dcDir, "devcontainer.json"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	name := filepath.Base(dir)
	return workspace{dir: dir, name: name, id: name}
}

func TestParseDevcontainerConfig_MinimalImage(t *testing.T) {
	ws := writeDevcontainerJSON(t, t.TempDir(), `{"image": "ubuntu:22.04"}`)
	cfg, err := parseDevcontainerConfig(ws)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Image != "ubuntu:22.04" {
		t.Fatalf("Image = %q, want %q", cfg.Image, "ubuntu:22.04")
	}
}

func TestParseDevcontainerConfig_RemoteUserDefault(t *testing.T) {
	ws := writeDevcontainerJSON(t, t.TempDir(), `{"image": "ubuntu:22.04"}`)
	cfg, err := parseDevcontainerConfig(ws)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.RemoteUser != "" {
		t.Fatalf("RemoteUser = %q, want empty (resolved at runtime from container)", cfg.RemoteUser)
	}
}

func TestParseDevcontainerConfig_RemoteUserExplicit(t *testing.T) {
	ws := writeDevcontainerJSON(t, t.TempDir(), `{"image": "ubuntu:22.04", "remoteUser": "root"}`)
	cfg, err := parseDevcontainerConfig(ws)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.RemoteUser != "root" {
		t.Fatalf("RemoteUser = %q, want %q", cfg.RemoteUser, "root")
	}
}

func TestParseDevcontainerConfig_RemoteWorkspaceFolderDefault(t *testing.T) {
	ws := writeDevcontainerJSON(t, t.TempDir(), `{"image": "ubuntu:22.04"}`)
	cfg, err := parseDevcontainerConfig(ws)
	if err != nil {
		t.Fatal(err)
	}
	want := "/workspaces/" + ws.name
	if cfg.RemoteWorkspaceFolder != want {
		t.Fatalf("RemoteWorkspaceFolder = %q, want %q", cfg.RemoteWorkspaceFolder, want)
	}
}

func TestParseDevcontainerConfig_RemoteWorkspaceFolderExplicit(t *testing.T) {
	ws := writeDevcontainerJSON(t, t.TempDir(), `{"image": "ubuntu:22.04", "workspaceFolder": "/home/dev/project"}`)
	cfg, err := parseDevcontainerConfig(ws)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.RemoteWorkspaceFolder != "/home/dev/project" {
		t.Fatalf("RemoteWorkspaceFolder = %q, want %q", cfg.RemoteWorkspaceFolder, "/home/dev/project")
	}
}

func TestParseDevcontainerConfig_BuildDockerfile(t *testing.T) {
	ws := writeDevcontainerJSON(t, t.TempDir(), `{
		"build": {
			"dockerfile": "Dockerfile",
			"context": "..",
			"args": {"GO_VERSION": "1.25"},
			"target": "dev"
		}
	}`)
	cfg, err := parseDevcontainerConfig(ws)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Build == nil {
		t.Fatal("Build is nil")
	}
	if cfg.Build.Dockerfile != "Dockerfile" {
		t.Fatalf("Dockerfile = %q, want %q", cfg.Build.Dockerfile, "Dockerfile")
	}
	if cfg.Build.Context != ".." {
		t.Fatalf("Context = %q, want %q", cfg.Build.Context, "..")
	}
	if cfg.Build.Args["GO_VERSION"] != "1.25" {
		t.Fatalf("Args[GO_VERSION] = %q, want %q", cfg.Build.Args["GO_VERSION"], "1.25")
	}
	if cfg.Build.Target != "dev" {
		t.Fatalf("Target = %q, want %q", cfg.Build.Target, "dev")
	}
}

func TestParseDevcontainerConfig_Features(t *testing.T) {
	ws := writeDevcontainerJSON(t, t.TempDir(), `{
		"image": "ubuntu:22.04",
		"features": {
			"ghcr.io/devcontainers/features/github-cli:1": {},
			"ghcr.io/duduribeiro/devcontainer-features/neovim:1": {"version": "nightly"}
		}
	}`)
	cfg, err := parseDevcontainerConfig(ws)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Features) != 2 {
		t.Fatalf("Features count = %d, want 2", len(cfg.Features))
	}
	nvim, ok := cfg.Features["ghcr.io/duduribeiro/devcontainer-features/neovim:1"]
	if !ok {
		t.Fatal("neovim feature not found")
	}
	if nvim["version"] != "nightly" {
		t.Fatalf("neovim version = %v, want %q", nvim["version"], "nightly")
	}
}

func TestParseDevcontainerConfig_ContainerEnv(t *testing.T) {
	ws := writeDevcontainerJSON(t, t.TempDir(), `{
		"image": "ubuntu:22.04",
		"containerEnv": {"FOO": "bar", "BAZ": "qux"}
	}`)
	cfg, err := parseDevcontainerConfig(ws)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ContainerEnv["FOO"] != "bar" {
		t.Fatalf("ContainerEnv[FOO] = %q, want %q", cfg.ContainerEnv["FOO"], "bar")
	}
	if cfg.ContainerEnv["BAZ"] != "qux" {
		t.Fatalf("ContainerEnv[BAZ] = %q, want %q", cfg.ContainerEnv["BAZ"], "qux")
	}
}

func TestParseDevcontainerConfig_LifecycleHookString(t *testing.T) {
	ws := writeDevcontainerJSON(t, t.TempDir(), `{
		"image": "ubuntu:22.04",
		"onCreateCommand": "echo hello",
		"postCreateCommand": "apt-get update",
		"postStartCommand": "echo started"
	}`)
	cfg, err := parseDevcontainerConfig(ws)
	if err != nil {
		t.Fatal(err)
	}
	// Verify raw messages are preserved
	var s string
	if err := json.Unmarshal(cfg.OnCreateCommand, &s); err != nil {
		t.Fatalf("OnCreateCommand unmarshal: %v", err)
	}
	if s != "echo hello" {
		t.Fatalf("OnCreateCommand = %q, want %q", s, "echo hello")
	}
}

func TestParseDevcontainerConfig_LifecycleHookArray(t *testing.T) {
	ws := writeDevcontainerJSON(t, t.TempDir(), `{
		"image": "ubuntu:22.04",
		"postCreateCommand": ["echo", "hello"]
	}`)
	cfg, err := parseDevcontainerConfig(ws)
	if err != nil {
		t.Fatal(err)
	}
	var arr []string
	if err := json.Unmarshal(cfg.PostCreateCommand, &arr); err != nil {
		t.Fatalf("PostCreateCommand unmarshal as array: %v", err)
	}
	if len(arr) != 2 || arr[0] != "echo" || arr[1] != "hello" {
		t.Fatalf("PostCreateCommand = %v, want [echo hello]", arr)
	}
}

func TestParseDevcontainerConfig_LifecycleHookMap(t *testing.T) {
	ws := writeDevcontainerJSON(t, t.TempDir(), `{
		"image": "ubuntu:22.04",
		"postCreateCommand": {
			"install": "npm install",
			"build": ["make", "build"]
		}
	}`)
	cfg, err := parseDevcontainerConfig(ws)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(cfg.PostCreateCommand, &m); err != nil {
		t.Fatalf("PostCreateCommand unmarshal as map: %v", err)
	}
	if len(m) != 2 {
		t.Fatalf("PostCreateCommand map length = %d, want 2", len(m))
	}
}

func TestParseDevcontainerConfig_DockerComposeFile(t *testing.T) {
	ws := writeDevcontainerJSON(t, t.TempDir(), `{
		"dockerComposeFile": "docker-compose.yml",
		"service": "app"
	}`)
	cfg, err := parseDevcontainerConfig(ws)
	if err != nil {
		t.Fatal(err)
	}
	// Verify Raw map contains dockerComposeFile
	if _, ok := cfg.Raw["dockerComposeFile"]; !ok {
		t.Fatal("Raw map should contain dockerComposeFile")
	}
}

func TestParseDevcontainerConfig_RawPreservesUnknownFields(t *testing.T) {
	ws := writeDevcontainerJSON(t, t.TempDir(), `{
		"image": "ubuntu:22.04",
		"customizations": {"vscode": {"extensions": ["ms-go.vscode"]}}
	}`)
	cfg, err := parseDevcontainerConfig(ws)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := cfg.Raw["customizations"]; !ok {
		t.Fatal("Raw map should preserve customizations field")
	}
}
