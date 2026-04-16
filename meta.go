package main

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"time"

	"github.com/docker/docker/api/types/container"
)

const (
	devcContainerEnv = "DEVC_CONTAINER"
	devcMetaDir      = "/opt/devc"
	devcMetaPath     = "/opt/devc/meta.json"
	devcBinPath      = "/opt/devc/bin/devc"
)

// containerMeta holds metadata about the devcontainer, written to /opt/devc/meta.json
// at container creation time and read by the in-container devc command.
type containerMeta struct {
	Version        string            `json:"version"`
	Project        string            `json:"project"`
	WorkspaceDir   string            `json:"workspaceDir"`
	WorkspaceMount string            `json:"workspaceMount"`
	RemoteUser     string            `json:"remoteUser"`
	Image          string            `json:"image"`
	Ports          []string          `json:"ports,omitempty"`
	Features       []string          `json:"features,omitempty"`
	Dotfiles       []string          `json:"dotfiles,omitempty"`
	ContainerEnv   map[string]string `json:"containerEnv,omitempty"`
	CreatedAt      string            `json:"createdAt"`
	Mode           string            `json:"mode"` // "image" or "compose"
	Arch           string            `json:"arch"`
}

// isInsideContainer returns true if running inside a devc-managed container.
func isInsideContainer() bool {
	return os.Getenv(devcContainerEnv) == "1"
}

// buildContainerMeta constructs metadata from the current up context.
func buildContainerMeta(ws workspace, cfg *devcontainerConfig, ports []string, features map[string]map[string]interface{}, dotfiles []string, mode string, imageTag string) *containerMeta {
	featureList := make([]string, 0, len(features))
	for ref := range features {
		featureList = append(featureList, ref)
	}
	sort.Strings(featureList)

	return &containerMeta{
		Version:        version,
		Project:        ws.name,
		WorkspaceDir:   ws.dir,
		WorkspaceMount: cfg.RemoteWorkspaceFolder,
		RemoteUser:     cfg.RemoteUser,
		Image:          imageTag,
		Ports:          ports,
		Features:       featureList,
		Dotfiles:       dotfiles,
		ContainerEnv:   cfg.ContainerEnv,
		CreatedAt:      time.Now().UTC().Format(time.RFC3339),
		Mode:           mode,
		Arch:           runtime.GOARCH,
	}
}

// injectDevcIntoContainer writes meta, binary, and PATH setup into the container.
// Meta and binary are written to the host-side daemon dir (bind-mounted at /opt/devc/).
// The PATH setup is written directly into the container filesystem.
func injectDevcIntoContainer(ctx context.Context, containerID string, wsName string, meta *containerMeta) error {
	sockDir := daemonSockDir(wsName)

	// Write meta.json to the host-side directory (visible in container via bind mount)
	data, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal meta: %w", err)
	}
	if err := os.WriteFile(filepath.Join(sockDir, "meta.json"), data, 0o644); err != nil {
		return fmt.Errorf("write meta: %w", err)
	}

	// Copy devc binary to host-side directory
	binDir := filepath.Join(sockDir, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		return fmt.Errorf("create bin dir: %w", err)
	}
	if err := copyExecutable(filepath.Join(binDir, "devc")); err != nil {
		return fmt.Errorf("copy binary: %w", err)
	}

	// Add /opt/devc/bin to PATH via /etc/profile.d/ (inside container filesystem)
	if err := addDevcToPath(ctx, containerID); err != nil {
		return fmt.Errorf("add to PATH: %w", err)
	}

	return nil
}

// copyExecutable copies the current process's binary to dst.
func copyExecutable(dst string) error {
	execPath, err := os.Executable()
	if err != nil {
		return err
	}
	src, err := os.Open(execPath)
	if err != nil {
		return err
	}
	defer func() { _ = src.Close() }()

	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
	if err != nil {
		return err
	}
	defer func() { _ = out.Close() }()

	_, err = io.Copy(out, src)
	return err
}

// addDevcToPath makes the devc binary discoverable inside the container by
// creating a symlink at /usr/local/bin/devc → /opt/devc/bin/devc.
// This works regardless of shell type or /etc/profile.d support.
func addDevcToPath(ctx context.Context, containerID string) error {
	cli, err := getDockerClient()
	if err != nil {
		return err
	}

	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	if err := tw.WriteHeader(&tar.Header{
		Typeflag: tar.TypeSymlink,
		Name:     "devc",
		Linkname: devcBinPath, // /opt/devc/bin/devc
		Mode:     0o777,
	}); err != nil {
		return err
	}
	if err := tw.Close(); err != nil {
		return err
	}

	return cli.CopyToContainer(ctx, containerID, "/usr/local/bin/", &buf, container.CopyToContainerOptions{})
}

// ensureDevcBinary ensures the devc binary exists in the daemon socket directory.
// Called on container restart to handle /tmp cleanup between stops.
func ensureDevcBinary(wsID string) error {
	sockDir := daemonSockDir(wsID)
	binPath := filepath.Join(sockDir, "bin", "devc")
	if _, err := os.Stat(binPath); err == nil {
		return nil // binary already present
	}
	if err := os.MkdirAll(filepath.Join(sockDir, "bin"), 0o755); err != nil {
		return fmt.Errorf("create bin dir: %w", err)
	}
	return copyExecutable(binPath)
}

// loadContainerMeta reads meta.json from /opt/devc/meta.json (inside container).
func loadContainerMeta() (*containerMeta, error) {
	data, err := os.ReadFile(devcMetaPath)
	if err != nil {
		return nil, fmt.Errorf("read meta: %w (not inside a devc container?)", err)
	}
	var meta containerMeta
	if err := json.Unmarshal(data, &meta); err != nil {
		return nil, fmt.Errorf("parse meta: %w", err)
	}
	return &meta, nil
}
