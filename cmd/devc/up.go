package main

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/closer/devc/internal/build"
	"github.com/closer/devc/internal/compose"
	"github.com/closer/devc/internal/config"
	"github.com/closer/devc/internal/container"
	"github.com/closer/devc/internal/daemon"
	"github.com/closer/devc/internal/docker"
	"github.com/closer/devc/internal/meta"
	"github.com/closer/devc/internal/orchestrate"
	"github.com/closer/devc/internal/ui"
	dockercontainer "github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/image"
	"github.com/spf13/cobra"
)

type upOptions struct {
	ports   []string
	rebuild bool
}

func newUpCmd() *cobra.Command {
	opts := upOptions{}
	cmd := &cobra.Command{
		Use:   "up [flags] [workspace-dir]",
		Short: "Start the devcontainer and enter it",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			dir := "."
			if len(args) > 0 {
				dir = args[0]
			}
			return runUp(dir, opts)
		},
	}
	cmd.Flags().StringArrayVarP(&opts.ports, "publish", "p", nil, "Publish port (e.g. -p 3000:3000). Repeatable.")
	cmd.Flags().BoolVar(&opts.rebuild, "rebuild", false, "Rebuild the container from scratch")
	return cmd
}

func newRebuildCmd() *cobra.Command {
	var ports []string
	cmd := &cobra.Command{
		Use:   "rebuild [flags] [workspace-dir]",
		Short: "Rebuild and enter the devcontainer",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			dir := "."
			if len(args) > 0 {
				dir = args[0]
			}
			return runUp(dir, upOptions{ports: ports, rebuild: true})
		},
	}
	cmd.Flags().StringArrayVarP(&ports, "publish", "p", nil, "Publish port (e.g. -p 3000:3000). Repeatable.")
	return cmd
}

func runUp(dir string, opts upOptions) error {
	ctx := context.Background()

	// 1. Resolve workspace
	ws, err := config.ResolveWorkspace(dir)
	if err != nil {
		return err
	}
	ui.PrintDone("Workspace resolved", ws.Name)

	// 2. Load user config
	ucfg, err := config.LoadUserConfig()
	if err != nil {
		return fmt.Errorf("load user config: %w", err)
	}
	ui.PrintDone("Config loaded", fmt.Sprintf("%d features, %d dotfiles", len(ucfg.Features), len(ucfg.Dotfiles)))

	// 3. Extract credentials
	if err := docker.ExtractCredentials(); err != nil {
		return err
	}

	// 4. Ensure devcontainer.json exists
	if err := config.EnsureDevcontainerJSON(ws); err != nil {
		return err
	}

	// 5. Parse devcontainer.json
	cfg, err := config.ParseDevcontainerConfig(ws)
	if err != nil {
		return err
	}

	// 6. Compose-based devcontainer
	if config.ComposeFiles(ws, cfg.Raw) != nil {
		return runUpCompose(ctx, ws, cfg, ucfg, opts)
	}

	// 7. Check existing container (running or stopped)
	containerID, findErr := docker.FindContainerByWorkspace(ws)

	if findErr == nil && !opts.rebuild {
		deps := newOrchestrateDeps(ws, ucfg, nil)
		deps.Restart = func(ctx context.Context) (string, error) {
			cli, cliErr := docker.GetClient()
			if cliErr != nil {
				return "", fmt.Errorf("docker client: %w", cliErr)
			}
			if err := cli.ContainerStart(ctx, containerID, dockercontainer.StartOptions{}); err != nil {
				return "", fmt.Errorf("container restart: %w", err)
			}
			return containerID, nil
		}
		return orchestrate.HandleExistingContainer(ctx, containerID, ws.ID, cfg, ucfg.Dotfiles, deps)
	}

	// 8. Rebuild: remove existing container + cached image
	if opts.rebuild {
		if containerID, err := docker.FindContainerByWorkspace(ws); err == nil {
			ui.PrintProgress("Removing container", containerID[:12])
			cli, cliErr := docker.GetClient()
			if cliErr == nil {
				_ = cli.ContainerRemove(ctx, containerID, dockercontainer.RemoveOptions{Force: true})
			}
		}
		// Remove cached image
		allFeatures := build.MergeFeatures(ucfg.Features, cfg.Features)
		baseImage := cfg.Image
		if cfg.Build != nil && cfg.Build.Dockerfile != "" {
			baseImage = fmt.Sprintf("devc-%s-intermediate:latest", ws.ID)
		}
		if baseImage != "" {
			tag := build.ComputeImageTag(ws.ID, baseImage, allFeatures)
			cli, cliErr := docker.GetClient()
			if cliErr == nil {
				_, _ = cli.ImageRemove(ctx, tag, image.RemoveOptions{Force: true})
			}
		}
	}

	// 9. Collect and resolve ports
	ports := config.CollectPorts(cfg.Raw, opts.ports)
	resolvedPorts := config.ResolveAllPorts(ports)
	if len(resolvedPorts) > 0 {
		ui.PrintDone("Ports", strings.Join(resolvedPorts, ", "))
	}

	// 10. Build image (with spinner for feature pull, build output streamed)
	ui.PrintProgress("Building image", ws.Name)
	imageTag, err := build.BuildFeatureImage(ctx, ws, cfg, ucfg.Features, opts.rebuild)
	if err != nil {
		return fmt.Errorf("image build: %w", err)
	}
	ui.PrintDone("Image built", imageTag)

	// 11. Create and start container
	sockDir := daemon.SockDir(ws.ID)
	containerID, err = container.CreateAndStartContainer(ctx, ws, cfg, imageTag, resolvedPorts, config.BuildHostMounts(ucfg, ws.ID, sockDir))
	if err != nil {
		return fmt.Errorf("container create: %w", err)
	}

	// 12. Resolve remote user (fall back to root if user doesn't exist)
	cfg.RemoteUser = docker.ResolveRemoteUser(ctx, containerID, cfg.RemoteUser)

	// 13. Inject devc binary and metadata into container
	allFeatures := build.MergeFeatures(ucfg.Features, cfg.Features)
	m := meta.BuildContainerMeta(ws, cfg, resolvedPorts, allFeatures, ucfg.Dotfiles, "image", imageTag, version)
	if err := meta.InjectIntoContainer(ctx, containerID, ws.ID, m, sockDir); err != nil {
		ui.PrintWarn("devc injection failed", err.Error())
	}

	// 14. Finalize: lifecycle hooks, setup, enter
	deps := newOrchestrateDeps(ws, ucfg, resolvedPorts)
	return orchestrate.FinalizeNewContainer(ctx, containerID, ws.ID, cfg, ucfg.Dotfiles, deps)
}

func runUpCompose(ctx context.Context, ws config.Workspace, cfg *config.DevcontainerConfig, ucfg *config.UserConfig, opts upOptions) error {
	cc, err := compose.ParseConfig(ws, cfg.Raw)
	if err != nil {
		return err
	}
	project := compose.Project(ws)

	// Check existing service container
	containerID, findErr := compose.FindServiceContainer(ctx, project, cc.Service)
	if findErr == nil && !opts.rebuild {
		deps := newOrchestrateDeps(ws, ucfg, nil)
		deps.RestartLabel = "Restarting services"
		deps.Restart = func(ctx context.Context) (string, error) {
			if err := compose.Exec(ctx, cc.Files, project, "start"); err != nil {
				return "", fmt.Errorf("compose start: %w", err)
			}
			newID, err := compose.FindServiceContainer(ctx, project, cc.Service)
			if err != nil {
				return "", fmt.Errorf("find container after start: %w", err)
			}
			return newID, nil
		}
		return orchestrate.HandleExistingContainer(ctx, containerID, ws.ID, cfg, ucfg.Dotfiles, deps)
	}

	// Start compose services (rebuild, ports, override, up, find container)
	containerID, resolvedPorts, err := compose.StartComposeServices(ctx, ws, cfg, cc, ucfg, compose.UpOptions{
		Ports:   opts.ports,
		Rebuild: opts.rebuild,
	})
	if err != nil {
		return err
	}

	// Resolve remote user (fall back to root if user doesn't exist)
	cfg.RemoteUser = docker.ResolveRemoteUser(ctx, containerID, cfg.RemoteUser)

	// Inject devc binary and metadata into container
	allFeatures := build.MergeFeatures(ucfg.Features, cfg.Features)
	sockDir := daemon.SockDir(ws.ID)
	m := meta.BuildContainerMeta(ws, cfg, resolvedPorts, allFeatures, ucfg.Dotfiles, "compose", "", version)
	if err := meta.InjectIntoContainer(ctx, containerID, ws.ID, m, sockDir); err != nil {
		ui.PrintWarn("devc injection failed", err.Error())
	}

	// Finalize: install features, lifecycle hooks, setup, enter
	deps := newOrchestrateDeps(ws, ucfg, resolvedPorts)
	deps.PreHooks = func(ctx context.Context, cid string) error {
		return compose.InstallFeaturesRuntime(ctx, cid, ws.Dir, allFeatures, opts.rebuild)
	}
	return orchestrate.FinalizeNewContainer(ctx, containerID, ws.ID, cfg, ucfg.Dotfiles, deps)
}

// newOrchestrateDeps creates Deps with the standard implementations wired in.
// staticPorts controls which ports are passed to enterContainer.
func newOrchestrateDeps(ws config.Workspace, ucfg *config.UserConfig, staticPorts []string) *orchestrate.Deps {
	return &orchestrate.Deps{
		IsRunning:    docker.IsContainerRunning,
		EnsureBinary: meta.EnsureDevcBinary,
		ResolveUser:  docker.ResolveRemoteUser,
		Setup:        docker.SetupContainer,
		RunHooks:     container.RunLifecycleHooks,
		Enter: func(ctx context.Context, containerID, remoteUser, workspaceFolder string) error {
			return enterContainer(ctx, ws, containerID, remoteUser, workspaceFolder, staticPorts)
		},
	}
}

// enterContainer starts the daemon, runs an interactive shell, and handles rebuild requests.
// staticPorts are ports already forwarded via Docker port bindings (host:container format);
// auto port detection will skip these.
func enterContainer(ctx context.Context, ws config.Workspace, containerID, remoteUser, workspaceFolder string, staticPorts []string) error {
	sockDir := daemon.SockDir(ws.ID)
	d, err := daemon.Start(ctx, containerID, sockDir)
	if err != nil {
		ui.PrintWarn("Daemon start failed", err.Error())
	} else {
		defer d.Close()
		d.SetStaticPorts(staticPorts)
		d.StartAutoPortDetection(ctx)
	}

	ui.PrintDone("Ready", "")
	ui.PrintProgress("Entering container", remoteUser+"@devc-"+ws.ID)
	exitCode, err := docker.ExecInteractive(ctx, containerID, remoteUser, workspaceFolder, []string{"bash", "-l"})
	if err != nil {
		return fmt.Errorf("interactive exec failed: %w", err)
	}

	// Check if rebuild was requested from inside
	if d != nil && d.RebuildRequested() {
		ui.PrintProgress("Rebuild requested", "re-running with --rebuild")
		return runUp(ws.Dir, upOptions{rebuild: true})
	}

	os.Exit(exitCode)
	return nil // unreachable
}
