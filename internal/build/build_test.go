package build

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGenerateDockerfile_NoFeatures(t *testing.T) {
	df := GenerateDockerfile("ubuntu:22.04", nil)
	if df != "FROM ubuntu:22.04\n" {
		t.Fatalf("Dockerfile = %q, want %q", df, "FROM ubuntu:22.04\n")
	}
}

func TestGenerateDockerfile_OneFeatureNoOptions(t *testing.T) {
	features := []FeatureInstall{
		{
			ID:      "github-cli",
			Files:   &FeatureFiles{InstallSh: []byte("#!/bin/bash\necho hi")},
			Options: nil,
		},
	}
	df := GenerateDockerfile("ubuntu:22.04", features)
	if !strings.HasPrefix(df, "FROM ubuntu:22.04\n") {
		t.Fatalf("should start with FROM, got %q", df)
	}
	if !strings.Contains(df, "COPY github-cli/") {
		t.Fatalf("should contain COPY, got %q", df)
	}
	if !strings.Contains(df, "chmod +x install.sh && ./install.sh") {
		t.Fatalf("should contain install.sh execution, got %q", df)
	}
}

func TestGenerateDockerfile_MultipleFeatures(t *testing.T) {
	features := []FeatureInstall{
		{
			ID:      "neovim",
			Files:   &FeatureFiles{InstallSh: []byte("#!/bin/bash")},
			Options: map[string]interface{}{"version": "nightly"},
		},
		{
			ID:      "ripgrep",
			Files:   &FeatureFiles{InstallSh: []byte("#!/bin/bash")},
			Options: nil,
		},
	}
	df := GenerateDockerfile("mcr.microsoft.com/devcontainers/base:ubuntu", features)
	if !strings.Contains(df, "COPY neovim/") {
		t.Fatalf("should contain COPY neovim, got %q", df)
	}
	if !strings.Contains(df, "COPY ripgrep/") {
		t.Fatalf("should contain COPY ripgrep, got %q", df)
	}
	if !strings.Contains(df, "VERSION=nightly") {
		t.Fatalf("should contain VERSION env var, got %q", df)
	}
}

func TestFeatureEnvVars(t *testing.T) {
	tests := []struct {
		name    string
		options map[string]interface{}
		want    map[string]string
	}{
		{
			name:    "nil options",
			options: nil,
			want:    map[string]string{},
		},
		{
			name:    "string option",
			options: map[string]interface{}{"version": "nightly"},
			want:    map[string]string{"VERSION": "nightly"},
		},
		{
			name:    "boolean option",
			options: map[string]interface{}{"install": true},
			want:    map[string]string{"INSTALL": "true"},
		},
		{
			name:    "number option",
			options: map[string]interface{}{"count": float64(42)},
			want:    map[string]string{"COUNT": "42"},
		},
		{
			name: "multiple options uppercase",
			options: map[string]interface{}{
				"version":     "nightly",
				"installPath": "/usr/local",
			},
			want: map[string]string{
				"VERSION":     "nightly",
				"INSTALLPATH": "/usr/local",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := FeatureEnvVars(tt.options)
			if len(got) != len(tt.want) {
				t.Fatalf("envVars count = %d, want %d (%v vs %v)", len(got), len(tt.want), got, tt.want)
			}
			for k, v := range tt.want {
				if got[k] != v {
					t.Fatalf("envVars[%q] = %q, want %q", k, got[k], v)
				}
			}
		})
	}
}

func TestMergeFeatures(t *testing.T) {
	t.Run("project overrides user", func(t *testing.T) {
		user := map[string]map[string]interface{}{
			"ghcr.io/feature-a:1": {"version": "1.0"},
			"ghcr.io/feature-b:1": {},
		}
		project := map[string]map[string]interface{}{
			"ghcr.io/feature-a:1": {"version": "2.0"},
			"ghcr.io/feature-c:1": {},
		}
		result := MergeFeatures(user, project)
		if len(result) != 3 {
			t.Fatalf("expected 3 features, got %d", len(result))
		}
		// Project version wins
		if result["ghcr.io/feature-a:1"]["version"] != "2.0" {
			t.Fatalf("expected project version 2.0, got %v", result["ghcr.io/feature-a:1"]["version"])
		}
		if _, ok := result["ghcr.io/feature-b:1"]; !ok {
			t.Fatal("missing user-only feature-b")
		}
		if _, ok := result["ghcr.io/feature-c:1"]; !ok {
			t.Fatal("missing project-only feature-c")
		}
	})

	t.Run("nil inputs", func(t *testing.T) {
		result := MergeFeatures(nil, nil)
		if len(result) != 0 {
			t.Fatalf("expected empty map, got %d entries", len(result))
		}
	})

	t.Run("user only", func(t *testing.T) {
		user := map[string]map[string]interface{}{
			"ghcr.io/feature-a:1": {"version": "nightly"},
		}
		result := MergeFeatures(user, nil)
		if len(result) != 1 {
			t.Fatalf("expected 1 feature, got %d", len(result))
		}
		if result["ghcr.io/feature-a:1"]["version"] != "nightly" {
			t.Fatal("user feature not preserved")
		}
	})

	t.Run("project only", func(t *testing.T) {
		project := map[string]map[string]interface{}{
			"ghcr.io/feature-a:1": {},
		}
		result := MergeFeatures(nil, project)
		if len(result) != 1 {
			t.Fatalf("expected 1 feature, got %d", len(result))
		}
	})
}

func TestComputeImageTag(t *testing.T) {
	features := map[string]map[string]interface{}{
		"ghcr.io/devcontainers/features/github-cli:1": {},
	}

	// Same input -> same tag
	tag1 := ComputeImageTag("myws", "ubuntu:22.04", features)
	tag2 := ComputeImageTag("myws", "ubuntu:22.04", features)
	if tag1 != tag2 {
		t.Fatalf("same input should produce same tag: %q vs %q", tag1, tag2)
	}

	// Different workspace -> different tag
	tag3 := ComputeImageTag("otherws", "ubuntu:22.04", features)
	if tag1 == tag3 {
		t.Fatal("different workspace should produce different tag")
	}

	// Different image -> different tag
	tag4 := ComputeImageTag("myws", "debian:12", features)
	if tag1 == tag4 {
		t.Fatal("different image should produce different tag")
	}

	// Different features -> different tag
	tag5 := ComputeImageTag("myws", "ubuntu:22.04", nil)
	if tag1 == tag5 {
		t.Fatal("different features should produce different tag")
	}

	// Tag format check
	if !strings.HasPrefix(tag1, "devc-myws:") {
		t.Fatalf("tag should start with 'devc-myws:', got %q", tag1)
	}
}

func TestPrepareBuildContext(t *testing.T) {
	features := []FeatureInstall{
		{
			ID: "test-feature",
			Files: &FeatureFiles{
				InstallSh: []byte("#!/bin/bash\necho hi"),
				AllFiles: map[string][]byte{
					"install.sh": []byte("#!/bin/bash\necho hi"),
					"config.sh":  []byte("export FOO=bar"),
				},
			},
		},
	}
	dockerfile := GenerateDockerfile("ubuntu:22.04", features)

	reader, err := PrepareBuildContext(dockerfile, features)
	if err != nil {
		t.Fatal(err)
	}

	// Read the tar and verify contents
	contents := ReadTarContents(t, reader)

	if _, ok := contents["Dockerfile"]; !ok {
		t.Fatal("tar should contain Dockerfile")
	}
	if _, ok := contents["test-feature/install.sh"]; !ok {
		t.Fatal("tar should contain test-feature/install.sh")
	}
	if _, ok := contents["test-feature/config.sh"]; !ok {
		t.Fatal("tar should contain test-feature/config.sh")
	}
}

func TestCreateDirTar_IncludesAllFiles(t *testing.T) {
	// Create a temporary context directory with multiple files
	contextDir := t.TempDir()
	dcDir := contextDir // Dockerfile in same dir as context

	// Create files in context dir
	if err := os.WriteFile(filepath.Join(contextDir, "Dockerfile"), []byte("FROM ubuntu\nCOPY app.py /app/\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(contextDir, "app.py"), []byte("print('hello')"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(contextDir, "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(contextDir, "src/main.py"), []byte("import app"), 0o644); err != nil {
		t.Fatal(err)
	}

	reader, err := CreateDirTar(contextDir, "Dockerfile", dcDir)
	if err != nil {
		t.Fatal(err)
	}

	contents := ReadTarContents(t, reader)

	// Verify all files are present
	if _, ok := contents["Dockerfile"]; !ok {
		t.Fatal("tar should contain Dockerfile")
	}
	if _, ok := contents["app.py"]; !ok {
		t.Fatal("tar should contain app.py")
	}
	if _, ok := contents["src/main.py"]; !ok {
		t.Fatal("tar should contain src/main.py")
	}
}

func TestCreateDirTar_DockerfileOutsideContext(t *testing.T) {
	// Context dir is workspace root, Dockerfile is in .devcontainer
	wsDir := t.TempDir()
	dcDir := filepath.Join(wsDir, ".devcontainer")
	if err := os.MkdirAll(dcDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// Dockerfile in .devcontainer
	if err := os.WriteFile(filepath.Join(dcDir, "Dockerfile"), []byte("FROM ubuntu\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// App file in workspace root
	if err := os.WriteFile(filepath.Join(wsDir, "main.go"), []byte("package main"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Context is workspace root, Dockerfile is in dcDir
	reader, err := CreateDirTar(wsDir, "Dockerfile", dcDir)
	if err != nil {
		t.Fatal(err)
	}

	contents := ReadTarContents(t, reader)

	if _, ok := contents["Dockerfile"]; !ok {
		t.Fatal("tar should contain Dockerfile (from dcDir)")
	}
	if _, ok := contents["main.go"]; !ok {
		t.Fatal("tar should contain main.go (from context dir)")
	}
	// .devcontainer should be skipped
	for name := range contents {
		if strings.HasPrefix(name, ".devcontainer") {
			t.Fatalf("tar should not contain .devcontainer files, found %q", name)
		}
	}
}
