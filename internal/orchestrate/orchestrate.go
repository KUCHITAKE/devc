// Package orchestrate provides shared container lifecycle logic used by both
// the image-based and compose-based devcontainer flows.
package orchestrate

import (
	"context"
	"fmt"
	"os"

	"github.com/closer/devc/internal/config"
	"github.com/closer/devc/internal/container"
	"github.com/closer/devc/internal/daemon"
	"github.com/closer/devc/internal/ui"
)

// Deps holds injectable dependencies for orchestration functions.
// Each field wraps a call that would normally go to docker/meta/container packages,
// allowing the orchestration logic to be tested without Docker.
type Deps struct {
	// IsRunning checks if a container is currently running.
	IsRunning func(containerID string) bool

	// Restart restarts a stopped container and returns the (possibly new) container ID.
	Restart func(ctx context.Context) (string, error)

	// EnsureBinary ensures the devc binary exists in the daemon socket directory.
	EnsureBinary func(sockDir string) error

	// ResolveUser determines the effective remote user for the container.
	ResolveUser func(ctx context.Context, containerID, remoteUser string) string

	// Setup runs container setup (dotfiles, git config, gh auth).
	Setup func(containerID, remoteUser string, dotfiles []string) error

	// RunHooks runs lifecycle hooks inside the container.
	RunHooks func(ctx context.Context, containerID, user string, hooks ...[]container.LifecycleCommand) error

	// Enter starts the daemon and enters the container with an interactive shell.
	Enter func(ctx context.Context, containerID, remoteUser, workspaceFolder string) error

	// PreHooks runs before lifecycle hooks on new container creation (e.g. InstallFeaturesRuntime).
	// May be nil.
	PreHooks func(ctx context.Context, containerID string) error

	// RestartLabel is the UI label shown when restarting a stopped container.
	// Defaults to "Restarting container" if empty.
	RestartLabel string
}

// HandleExistingContainer handles attaching to a running container or restarting
// a stopped one, then setting up and entering it.
func HandleExistingContainer(ctx context.Context, containerID, wsID string, cfg *config.DevcontainerConfig, dotfiles []string, deps *Deps) error {
	if deps.IsRunning(containerID) {
		ui.PrintDone("Attaching to container", containerID[:12])
	} else {
		label := deps.RestartLabel
		if label == "" {
			label = "Restarting container"
		}
		ui.PrintProgress(label, containerID[:12])

		// Ensure daemon socket directory exists (may be lost after host reboot)
		sockDir := daemon.SockDir(wsID)
		if err := os.MkdirAll(sockDir, 0o755); err != nil {
			return fmt.Errorf("create daemon socket dir: %w", err)
		}

		newID, err := deps.Restart(ctx)
		if err != nil {
			return err
		}
		containerID = newID

		// Run postStartCommand only (container already created)
		postStartHooks := container.ParseLifecycleHook(cfg.PostStartCommand)
		if err := deps.RunHooks(ctx, containerID, cfg.RemoteUser, postStartHooks); err != nil {
			ui.PrintWarn("Lifecycle hooks had errors", err.Error())
		}
	}

	// Ensure devc binary is present (may be lost if /tmp was cleaned)
	if err := deps.EnsureBinary(daemon.SockDir(wsID)); err != nil {
		ui.PrintWarn("devc binary restore failed", err.Error())
	}

	// Resolve remote user (fall back to root if user doesn't exist)
	cfg.RemoteUser = deps.ResolveUser(ctx, containerID, cfg.RemoteUser)

	// Setup container
	if err := ui.RunWithSpinner("Setting up container", "", func() error {
		if err := deps.Setup(containerID, cfg.RemoteUser, dotfiles); err != nil {
			ui.PrintWarn("Container setup had errors", err.Error())
		}
		return nil
	}); err != nil {
		return err
	}

	return deps.Enter(ctx, containerID, cfg.RemoteUser, cfg.RemoteWorkspaceFolder)
}

// FinalizeNewContainer runs post-creation steps: optional pre-hooks,
// lifecycle hooks, setup, and enter. The caller must resolve the remote user
// and inject metadata before calling this.
func FinalizeNewContainer(ctx context.Context, containerID, wsID string, cfg *config.DevcontainerConfig, dotfiles []string, deps *Deps) error {
	// Run pre-hooks if provided (e.g. InstallFeaturesRuntime for compose)
	if deps.PreHooks != nil {
		if err := deps.PreHooks(ctx, containerID); err != nil {
			ui.PrintWarn("Feature installation had errors", err.Error())
		}
	}

	// Lifecycle hooks
	onCreateHooks := container.ParseLifecycleHook(cfg.OnCreateCommand)
	postCreateHooks := container.ParseLifecycleHook(cfg.PostCreateCommand)
	postStartHooks := container.ParseLifecycleHook(cfg.PostStartCommand)
	if err := deps.RunHooks(ctx, containerID, cfg.RemoteUser, onCreateHooks, postCreateHooks, postStartHooks); err != nil {
		ui.PrintWarn("Lifecycle hooks had errors", err.Error())
	}

	// Setup container
	if err := ui.RunWithSpinner("Setting up container", "", func() error {
		if err := deps.Setup(containerID, cfg.RemoteUser, dotfiles); err != nil {
			ui.PrintWarn("Container setup had errors", err.Error())
		}
		return nil
	}); err != nil {
		return err
	}

	return deps.Enter(ctx, containerID, cfg.RemoteUser, cfg.RemoteWorkspaceFolder)
}
