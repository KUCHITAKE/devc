package main

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
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

// writeMetaToContainer writes meta.json into the container at /opt/devc/.
func writeMetaToContainer(ctx context.Context, containerID string, meta *containerMeta) error {
	cli, err := getDockerClient()
	if err != nil {
		return fmt.Errorf("docker client: %w", err)
	}

	data, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal meta: %w", err)
	}

	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)

	// Create /opt/devc/ directory entry
	if err := tw.WriteHeader(&tar.Header{
		Typeflag: tar.TypeDir,
		Name:     "devc/",
		Mode:     0o755,
	}); err != nil {
		return err
	}

	// Create /opt/devc/bin/ directory entry
	if err := tw.WriteHeader(&tar.Header{
		Typeflag: tar.TypeDir,
		Name:     "devc/bin/",
		Mode:     0o755,
	}); err != nil {
		return err
	}

	// Write meta.json
	if err := tw.WriteHeader(&tar.Header{
		Name: "devc/meta.json",
		Mode: 0o644,
		Size: int64(len(data)),
	}); err != nil {
		return err
	}
	if _, err := tw.Write(data); err != nil {
		return err
	}

	if err := tw.Close(); err != nil {
		return err
	}

	return cli.CopyToContainer(ctx, containerID, "/opt/", &buf, containerCopyOptions())
}

// copyBinaryToContainer copies the current devc binary into the container.
func copyBinaryToContainer(ctx context.Context, containerID string) error {
	cli, err := getDockerClient()
	if err != nil {
		return fmt.Errorf("docker client: %w", err)
	}

	execPath, err := os.Executable()
	if err != nil {
		return fmt.Errorf("get executable path: %w", err)
	}

	binData, err := os.ReadFile(execPath)
	if err != nil {
		return fmt.Errorf("read binary: %w", err)
	}

	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)

	if err := tw.WriteHeader(&tar.Header{
		Name: "devc/bin/devc",
		Mode: 0o755,
		Size: int64(len(binData)),
	}); err != nil {
		return err
	}
	if _, err := tw.Write(binData); err != nil {
		return err
	}
	if err := tw.Close(); err != nil {
		return err
	}

	return cli.CopyToContainer(ctx, containerID, "/opt/", &buf, containerCopyOptions())
}

// containerCopyOptions returns default options for CopyToContainer.
func containerCopyOptions() container.CopyToContainerOptions {
	return container.CopyToContainerOptions{}
}

// addDevcToPath adds /opt/devc/bin to PATH via /etc/profile.d/devc.sh.
func addDevcToPath(ctx context.Context, containerID string) error {
	script := `#!/bin/sh
export PATH="/opt/devc/bin:$PATH"
`
	cli, err := getDockerClient()
	if err != nil {
		return err
	}

	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	if err := tw.WriteHeader(&tar.Header{
		Name: "devc.sh",
		Mode: 0o644,
		Size: int64(len(script)),
	}); err != nil {
		return err
	}
	if _, err := tw.Write([]byte(script)); err != nil {
		return err
	}
	if err := tw.Close(); err != nil {
		return err
	}

	return cli.CopyToContainer(ctx, containerID, "/etc/profile.d/", &buf, containerCopyOptions())
}

// injectDevcIntoContainer writes the devc binary, metadata, and PATH setup into the container.
func injectDevcIntoContainer(ctx context.Context, containerID string, meta *containerMeta) error {
	if err := writeMetaToContainer(ctx, containerID, meta); err != nil {
		return fmt.Errorf("write meta: %w", err)
	}
	if err := copyBinaryToContainer(ctx, containerID); err != nil {
		return fmt.Errorf("copy binary: %w", err)
	}
	if err := addDevcToPath(ctx, containerID); err != nil {
		return fmt.Errorf("add to PATH: %w", err)
	}
	return nil
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
