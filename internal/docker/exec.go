package docker

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/closer/devc/internal/config"
	"github.com/closer/devc/internal/ui"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
)

// credentialSource is one host credential to stage: the file name it is
// written under and how to read its value.
type credentialSource struct {
	name string
	read func() ([]byte, error)
}

func hostCredentialSources() []credentialSource {
	return []credentialSource{
		{"git-user-name", exec.Command("git", "config", "--global", "user.name").Output},
		{"git-user-email", exec.Command("git", "config", "--global", "user.email").Output},
		{"gh-token", exec.Command("gh", "auth", "token").Output},
	}
}

// ExtractCredentials stages host git/gh credentials into CredentialsDir so
// they can be bind-mounted into the container.
func ExtractCredentials() error {
	return extractCredentials(config.CredentialsDir(), hostCredentialSources())
}

func extractCredentials(dir string, sources []credentialSource) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("credentials dir: %w", err)
	}
	for _, s := range sources {
		path := filepath.Join(dir, s.name)
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("remove stale credential: %w", err)
		}
		// An unavailable source (e.g. gh not installed or not logged in) is
		// not an error — the file stays absent and container setup skips it.
		out, err := s.read()
		if err != nil {
			continue
		}
		if err := os.WriteFile(path, bytes.TrimSpace(out), 0o644); err != nil {
			return fmt.Errorf("stage credential: %w", err)
		}
	}
	return nil
}

// ListDevcContainers returns containers managed by devc, identified by the
// devcontainer.local_folder label. When all is true, stopped containers are
// included; otherwise only running ones are returned.
func ListDevcContainers(ctx context.Context, all bool) ([]container.Summary, error) {
	cli, err := GetClient()
	if err != nil {
		return nil, fmt.Errorf("docker client: %w", err)
	}
	return cli.ContainerList(ctx, container.ListOptions{
		All: all,
		Filters: filters.NewArgs(
			filters.Arg("label", "devcontainer.local_folder"),
		),
	})
}

// ListComposeContainers returns containers that belong to a docker compose
// project (labeled com.docker.compose.project). Callers filter these down to
// devc-managed projects. When all is true, stopped containers are included.
func ListComposeContainers(ctx context.Context, all bool) ([]container.Summary, error) {
	cli, err := GetClient()
	if err != nil {
		return nil, fmt.Errorf("docker client: %w", err)
	}
	return cli.ContainerList(ctx, container.ListOptions{
		All: all,
		Filters: filters.NewArgs(
			filters.Arg("label", "com.docker.compose.project"),
		),
	})
}

// ListRunningContainers returns all running containers. It is used by the
// pre-flight port check, which must consider every port holder — including
// compose service containers that do not carry the devcontainer.local_folder
// label.
func ListRunningContainers(ctx context.Context) ([]container.Summary, error) {
	cli, err := GetClient()
	if err != nil {
		return nil, fmt.Errorf("docker client: %w", err)
	}
	return cli.ContainerList(ctx, container.ListOptions{All: false})
}

// IsContainerRunning checks if a specific container is running.
func IsContainerRunning(containerID string) bool {
	cli, err := GetClient()
	if err != nil {
		return false
	}
	info, err := cli.ContainerInspect(context.Background(), containerID)
	if err != nil {
		return false
	}
	return info.State != nil && info.State.Running
}

// FindContainerByWorkspace finds a devcontainer by its workspace folder label.
func FindContainerByWorkspace(ws config.Workspace) (string, error) {
	cli, err := GetClient()
	if err != nil {
		return "", fmt.Errorf("docker client: %w", err)
	}
	containers, err := cli.ContainerList(context.Background(), container.ListOptions{
		All: true,
		Filters: filters.NewArgs(
			filters.Arg("label", "devcontainer.local_folder="+ws.Dir),
		),
	})
	if err != nil {
		return "", fmt.Errorf("container list failed: %w", err)
	}
	if len(containers) == 0 {
		return "", fmt.Errorf("no devcontainer found for %s", ws.Dir)
	}
	return containers[0].ID, nil
}

// ResolveRemoteUser determines the effective remote user for the container.
//
// If remoteUser is explicitly set (non-empty), it verifies the user exists
// in the container and falls back to "root" with a warning if not.
//
// If remoteUser is empty (not specified in devcontainer.json), it uses the
// container image's default USER, falling back to "root" if unset.
// This matches the devcontainer spec behavior.
func ResolveRemoteUser(ctx context.Context, containerID, remoteUser string) string {
	if remoteUser == "" {
		// Use container's default user (from Dockerfile USER directive)
		return ContainerDefaultUser(ctx, containerID)
	}
	if remoteUser == "root" {
		return remoteUser
	}
	_, err := ExecOutput(ctx, containerID, "root", []string{"id", "-u", remoteUser})
	if err != nil {
		ui.PrintWarn("Remote user not found", fmt.Sprintf("%q does not exist in the container, falling back to root", remoteUser))
		return "root"
	}
	return remoteUser
}

// ContainerDefaultUser returns the default user configured in the container image.
// It checks the image's devcontainer.metadata label for remoteUser first (matching
// the devcontainer spec), then falls back to the Dockerfile USER directive.
// Falls back to "root" if no user is configured or on error.
func ContainerDefaultUser(ctx context.Context, containerID string) string {
	cli, err := GetClient()
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
		if u := RemoteUserFromMetadata(metadata); u != "" {
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

// ImageDefaultUser returns the default user configured in an image, mirroring
// ContainerDefaultUser but for an image reference (used at build time before a
// container exists). It checks the image's devcontainer.metadata label for
// remoteUser first (matching the devcontainer spec), then falls back to the
// image's USER directive, then "root".
func ImageDefaultUser(ctx context.Context, imageRef string) string {
	cli, err := GetClient()
	if err != nil {
		return "root"
	}
	info, err := cli.ImageInspect(ctx, imageRef)
	if err != nil || info.Config == nil {
		return "root"
	}

	if metadata, ok := info.Config.Labels["devcontainer.metadata"]; ok {
		if u := RemoteUserFromMetadata(metadata); u != "" {
			return u
		}
	}

	user := info.Config.User
	if user == "" {
		return "root"
	}
	if i := strings.Index(user, ":"); i >= 0 {
		user = user[:i]
	}
	return user
}

// RemoteUserFromMetadata extracts remoteUser from a devcontainer.metadata JSON label.
// The label value is a JSON array of objects; the last non-empty remoteUser wins.
func RemoteUserFromMetadata(metadata string) string {
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

// dotfileLinkCommands returns the commands that replace target with a symlink
// to staging. The existing target is removed first: a devcontainer feature may
// pre-create it as a populated directory (e.g. claude-code creates ~/.claude),
// and `ln -sfn` cannot replace a directory — it would instead create the link
// inside it, leaving host credentials unused.
func dotfileLinkCommands(staging, target string) [][]string {
	return [][]string{
		{"mkdir", "-p", filepath.Dir(target)},
		{"rm", "-rf", target},
		{"ln", "-sfn", staging, target},
	}
}

func SetupContainer(containerID, remoteUser string, dotfiles []string) error {
	ctx := context.Background()

	// Discover remote home
	remoteHome, err := ExecOutput(ctx, containerID, remoteUser, []string{"sh", "-c", "echo $HOME"})
	if err != nil {
		return fmt.Errorf("get remote home: %w", err)
	}

	// Create symlinks for dotfiles
	for _, df := range dotfiles {
		rel := config.DotfileRelPath(df)
		staging := filepath.Join(config.DotfilesDir, rel)
		target := filepath.Join(remoteHome, rel)
		for _, cmd := range dotfileLinkCommands(staging, target) {
			_ = Exec(ctx, containerID, remoteUser, cmd)
		}
	}

	// Git config (non-fatal). Values are read from the host staging dir;
	// commands run inside the container against the mounted CredentialsTarget.
	credDir := config.CredentialsDir()
	if data, err := os.ReadFile(filepath.Join(credDir, "git-user-name")); err == nil {
		_ = Exec(ctx, containerID, remoteUser, []string{"git", "config", "--global", "user.name", strings.TrimSpace(string(data))})
	}
	if data, err := os.ReadFile(filepath.Join(credDir, "git-user-email")); err == nil {
		_ = Exec(ctx, containerID, remoteUser, []string{"git", "config", "--global", "user.email", strings.TrimSpace(string(data))})
	}
	// gh auth (non-fatal)
	if _, err := os.Stat(filepath.Join(credDir, "gh-token")); err == nil {
		if _, err := ExecOutput(ctx, containerID, remoteUser, []string{"sh", "-c", "command -v gh"}); err == nil {
			_ = Exec(ctx, containerID, remoteUser, []string{"sh", "-c", "gh auth login --with-token < " + config.CredentialsTarget + "/gh-token && gh auth setup-git"})
		}
	}

	return nil
}
