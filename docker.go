package main

import (
	"bytes"
	"context"
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
