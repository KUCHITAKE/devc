package main

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/charmbracelet/log"
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

func buildMountArgs(mounts []hostMount) []string {
	var args []string
	for _, m := range mounts {
		if _, err := os.Stat(m.source); err == nil {
			args = append(args, "--mount", fmt.Sprintf("type=bind,source=%s,target=%s", m.source, m.target))
		}
	}
	return args
}

// isContainerRunning checks if a devcontainer is already running for the workspace.
func isContainerRunning(ws workspace) bool {
	out, err := exec.Command("docker", "ps", "-q",
		"--filter", "label=devcontainer.local_folder="+ws.dir,
	).Output()
	return err == nil && strings.TrimSpace(string(out)) != ""
}

// findContainerByWorkspace finds a devcontainer by its workspace folder label.
func findContainerByWorkspace(ws workspace) (string, error) {
	out, err := exec.Command("docker", "ps", "-aq",
		"--filter", "label=devcontainer.local_folder="+ws.dir,
	).Output()
	if err != nil {
		return "", fmt.Errorf("docker ps failed: %w", err)
	}
	id := strings.TrimSpace(string(out))
	if id == "" {
		return "", fmt.Errorf("no devcontainer found for %s", ws.dir)
	}
	// If multiple containers match (shouldn't happen for non-compose), take the first.
	if lines := strings.Split(id, "\n"); len(lines) > 1 {
		id = lines[0]
	}
	return id, nil
}

func setupContainer(containerID, remoteUser string) error {
	dockerExec := func(args ...string) error {
		cmdArgs := append([]string{"exec", "-u", remoteUser, containerID}, args...)
		cmd := exec.Command("docker", cmdArgs...)
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		return cmd.Run()
	}
	dockerExecOutput := func(args ...string) (string, error) {
		cmdArgs := append([]string{"exec", "-u", remoteUser, containerID}, args...)
		out, err := exec.Command("docker", cmdArgs...).Output()
		return strings.TrimSpace(string(out)), err
	}

	// Discover remote home
	remoteHome, err := dockerExecOutput("sh", "-c", "echo $HOME")
	if err != nil {
		return fmt.Errorf("get remote home: %w", err)
	}
	log.Info("Remote home", "path", remoteHome)

	// Create .config and symlinks
	_ = dockerExec("mkdir", "-p", remoteHome+"/.config")
	_ = dockerExec("ln", "-sfn", dotfilesDir+"/config-nvim", remoteHome+"/.config/nvim")
	_ = dockerExec("ln", "-sfn", dotfilesDir+"/claude", remoteHome+"/.claude")
	_ = dockerExec("ln", "-sfn", dotfilesDir+"/claude.json", remoteHome+"/.claude.json")
	_ = dockerExec("ln", "-sfn", dotfilesDir+"/ssh", remoteHome+"/.ssh")

	// Git config (non-fatal)
	if data, err := os.ReadFile("/tmp/devc-credentials/git-user-name"); err == nil {
		_ = dockerExec("git", "config", "--global", "user.name", strings.TrimSpace(string(data)))
	}
	if data, err := os.ReadFile("/tmp/devc-credentials/git-user-email"); err == nil {
		_ = dockerExec("git", "config", "--global", "user.email", strings.TrimSpace(string(data)))
	}
	// gh auth (non-fatal)
	if _, err := os.Stat("/tmp/devc-credentials/gh-token"); err == nil {
		_ = dockerExec("bash", "-c", "gh auth login --with-token < /tmp/devc-credentials/gh-token && gh auth setup-git")
	}

	return nil
}
