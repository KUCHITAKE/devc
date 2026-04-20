package meta

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

	"github.com/closer/devc/internal/config"
	"github.com/closer/devc/internal/docker"
	dockercontainer "github.com/docker/docker/api/types/container"
)

const (
	DevcContainerEnv = "DEVC_CONTAINER"
	DevcMetaPath     = "/opt/devc/meta.json"
	DevcBinPath      = "/opt/devc/bin/devc"
)

// ContainerMeta holds metadata about the devcontainer, written to /opt/devc/meta.json
// at container creation time and read by the in-container devc command.
type ContainerMeta struct {
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

// IsInsideContainer returns true if running inside a devc-managed container.
func IsInsideContainer() bool {
	return os.Getenv(DevcContainerEnv) == "1"
}

// BuildContainerMeta constructs metadata from the current up context.
func BuildContainerMeta(ws config.Workspace, cfg *config.DevcontainerConfig, ports []string, features map[string]map[string]interface{}, dotfiles []string, mode string, imageTag string, version string) *ContainerMeta {
	featureList := make([]string, 0, len(features))
	for ref := range features {
		featureList = append(featureList, ref)
	}
	sort.Strings(featureList)

	return &ContainerMeta{
		Version:        version,
		Project:        ws.Name,
		WorkspaceDir:   ws.Dir,
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

// InjectIntoContainer writes meta, binary, and PATH setup into the container.
// Meta and binary are written to the host-side daemon dir (bind-mounted at /opt/devc/).
// The PATH setup is written directly into the container filesystem.
func InjectIntoContainer(ctx context.Context, containerID string, wsName string, meta *ContainerMeta, daemonSockDir string) error {
	sockDir := daemonSockDir

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
	cli, err := docker.GetClient()
	if err != nil {
		return err
	}

	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	if err := tw.WriteHeader(&tar.Header{
		Typeflag: tar.TypeSymlink,
		Name:     "devc",
		Linkname: DevcBinPath, // /opt/devc/bin/devc
		Mode:     0o777,
	}); err != nil {
		return err
	}
	if err := tw.Close(); err != nil {
		return err
	}

	return cli.CopyToContainer(ctx, containerID, "/usr/local/bin/", &buf, dockercontainer.CopyToContainerOptions{})
}

// EnsureDevcBinary ensures the devc binary exists in the daemon socket directory.
// Called on container restart to handle /tmp cleanup between stops.
func EnsureDevcBinary(daemonSockDir string) error {
	binPath := filepath.Join(daemonSockDir, "bin", "devc")
	if _, err := os.Stat(binPath); err == nil {
		return nil // binary already present
	}
	if err := os.MkdirAll(filepath.Join(daemonSockDir, "bin"), 0o755); err != nil {
		return fmt.Errorf("create bin dir: %w", err)
	}
	return copyExecutable(binPath)
}

// LoadContainerMeta reads meta.json from /opt/devc/meta.json (inside container).
func LoadContainerMeta() (*ContainerMeta, error) {
	data, err := os.ReadFile(DevcMetaPath)
	if err != nil {
		return nil, fmt.Errorf("read meta: %w (not inside a devc container?)", err)
	}
	var meta ContainerMeta
	if err := json.Unmarshal(data, &meta); err != nil {
		return nil, fmt.Errorf("parse meta: %w", err)
	}
	return &meta, nil
}
