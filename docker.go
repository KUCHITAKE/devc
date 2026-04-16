package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
)

func extractCredentials() error {
	dir := "/tmp/devc-credentials"
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	// git user.name
	if out, err := exec.Command("git", "config", "--global", "user.name").Output(); err == nil {
		_ = os.WriteFile(filepath.Join(dir, "git-user-name"), bytes.TrimSpace(out), 0o644)
	}
	// git user.email
	if out, err := exec.Command("git", "config", "--global", "user.email").Output(); err == nil {
		_ = os.WriteFile(filepath.Join(dir, "git-user-email"), bytes.TrimSpace(out), 0o644)
	}
	// gh auth token
	if out, err := exec.Command("gh", "auth", "token").Output(); err == nil {
		_ = os.WriteFile(filepath.Join(dir, "gh-token"), bytes.TrimSpace(out), 0o644)
	}
	return nil
}

// isContainerRunning checks if a specific container is running.
func isContainerRunning(containerID string) bool {
	cli, err := getDockerClient()
	if err != nil {
		return false
	}
	info, err := cli.ContainerInspect(context.Background(), containerID)
	if err != nil {
		return false
	}
	return info.State != nil && info.State.Running
}

// findContainerByWorkspace finds a devcontainer by its workspace folder label.
func findContainerByWorkspace(ws workspace) (string, error) {
	cli, err := getDockerClient()
	if err != nil {
		return "", fmt.Errorf("docker client: %w", err)
	}
	containers, err := cli.ContainerList(context.Background(), container.ListOptions{
		All: true,
		Filters: filters.NewArgs(
			filters.Arg("label", "devcontainer.local_folder="+ws.dir),
		),
	})
	if err != nil {
		return "", fmt.Errorf("container list failed: %w", err)
	}
	if len(containers) == 0 {
		return "", fmt.Errorf("no devcontainer found for %s", ws.dir)
	}
	return containers[0].ID, nil
}

// resolveRemoteUser determines the effective remote user for the container.
//
// If remoteUser is explicitly set (non-empty), it verifies the user exists
// in the container and falls back to "root" with a warning if not.
//
// If remoteUser is empty (not specified in devcontainer.json), it uses the
// container image's default USER, falling back to "root" if unset.
// This matches the devcontainer spec behavior.
func resolveRemoteUser(ctx context.Context, containerID, remoteUser string) string {
	if remoteUser == "" {
		// Use container's default user (from Dockerfile USER directive)
		return containerDefaultUser(ctx, containerID)
	}
	if remoteUser == "root" {
		return remoteUser
	}
	_, err := containerExecOutput(ctx, containerID, "root", []string{"id", "-u", remoteUser})
	if err != nil {
		printWarn("Remote user not found", fmt.Sprintf("%q does not exist in the container, falling back to root", remoteUser))
		return "root"
	}
	return remoteUser
}

// containerDefaultUser returns the default user configured in the container image.
// It checks the image's devcontainer.metadata label for remoteUser first (matching
// the devcontainer spec), then falls back to the Dockerfile USER directive.
// Falls back to "root" if no user is configured or on error.
func containerDefaultUser(ctx context.Context, containerID string) string {
	cli, err := getDockerClient()
	if err != nil {
		return "root"
	}
	info, err := cli.ContainerInspect(ctx, containerID)
	if err != nil {
		return "root"
	}

	// Check devcontainer.metadata label for remoteUser (set by base images like
	// mcr.microsoft.com/devcontainers/* which don't use Dockerfile USER directive).
	if metadata, ok := info.Config.Labels["devcontainer.metadata"]; ok {
		if u := remoteUserFromMetadata(metadata); u != "" {
			return u
		}
	}

	user := info.Config.User
	if user == "" {
		return "root"
	}
	// Config.User can be "uid:gid" — extract the user part
	if i := strings.Index(user, ":"); i >= 0 {
		user = user[:i]
	}
	return user
}

// remoteUserFromMetadata extracts remoteUser from a devcontainer.metadata JSON label.
// The label value is a JSON array of objects; the last non-empty remoteUser wins.
func remoteUserFromMetadata(metadata string) string {
	var entries []map[string]json.RawMessage
	if err := json.Unmarshal([]byte(metadata), &entries); err != nil {
		return ""
	}
	var user string
	for _, entry := range entries {
		if v, ok := entry["remoteUser"]; ok {
			var u string
			if err := json.Unmarshal(v, &u); err == nil && u != "" {
				user = u
			}
		}
	}
	return user
}

func setupContainer(containerID, remoteUser string, dotfiles []string) error {
	ctx := context.Background()

	// Discover remote home
	remoteHome, err := containerExecOutput(ctx, containerID, remoteUser, []string{"sh", "-c", "echo $HOME"})
	if err != nil {
		return fmt.Errorf("get remote home: %w", err)
	}

	// Create symlinks for dotfiles
	for _, df := range dotfiles {
		rel := dotfileRelPath(df)
		staging := filepath.Join(dotfilesDir, rel)
		target := filepath.Join(remoteHome, rel)
		_ = containerExec(ctx, containerID, remoteUser, []string{"mkdir", "-p", filepath.Dir(target)})
		_ = containerExec(ctx, containerID, remoteUser, []string{"ln", "-sfn", staging, target})
	}

	// Git config (non-fatal)
	if data, err := os.ReadFile("/tmp/devc-credentials/git-user-name"); err == nil {
		_ = containerExec(ctx, containerID, remoteUser, []string{"git", "config", "--global", "user.name", strings.TrimSpace(string(data))})
	}
	if data, err := os.ReadFile("/tmp/devc-credentials/git-user-email"); err == nil {
		_ = containerExec(ctx, containerID, remoteUser, []string{"git", "config", "--global", "user.email", strings.TrimSpace(string(data))})
	}
	// gh auth (non-fatal)
	if _, err := os.Stat("/tmp/devc-credentials/gh-token"); err == nil {
		_ = containerExec(ctx, containerID, remoteUser, []string{"bash", "-c", "gh auth login --with-token < /tmp/devc-credentials/gh-token && gh auth setup-git"})
	}

	return nil
}
