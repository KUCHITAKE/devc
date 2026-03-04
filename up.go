package main

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/docker/docker/api/types/container"
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
	ws, err := resolveWorkspace(dir)
	if err != nil {
		return err
	}
	printDone("Workspace resolved", ws.name)

	// 2. Load user config
	ucfg, err := loadUserConfig()
	if err != nil {
		return fmt.Errorf("load user config: %w", err)
	}
	printDone("Config loaded", fmt.Sprintf("%d features, %d dotfiles", len(ucfg.Features), len(ucfg.Dotfiles)))

	// 3. Extract credentials
	if err := extractCredentials(); err != nil {
		return err
	}

	// 4. Ensure devcontainer.json exists
	if err := ensureDevcontainerJSON(ws); err != nil {
		return err
	}

	// 5. Parse devcontainer.json
	cfg, err := parseDevcontainerConfig(ws)
	if err != nil {
		return err
	}

	// 6. Compose-based devcontainer → delegate to runUpCompose
	if composeFiles(ws, cfg.Raw) != nil {
		cc, err := parseComposeConfig(ws, cfg.Raw)
		if err != nil {
			return err
		}
		return runUpCompose(ctx, ws, cfg, cc, ucfg, opts)
	}

	// 7. Check existing container (running or stopped)
	containerID, findErr := findContainerByWorkspace(ws)

	if findErr == nil && !opts.rebuild {
		if isContainerRunning(containerID) {
			// Already running — attach directly
			printDone("Attaching to container", containerID[:12])
		} else {
			// Stopped container — restart it
			printProgress("Restarting container", containerID[:12])
			cli, cliErr := getDockerClient()
			if cliErr != nil {
				return fmt.Errorf("docker client: %w", cliErr)
			}
			if err := cli.ContainerStart(ctx, containerID, container.StartOptions{}); err != nil {
				return fmt.Errorf("container restart: %w", err)
			}
			// Run postStartCommand only (container already created)
			postStartHooks := parseLifecycleHook(cfg.PostStartCommand)
			if err := runLifecycleHooks(ctx, containerID, cfg.RemoteUser, postStartHooks); err != nil {
				printWarn("Lifecycle hooks had errors", err.Error())
			}
		}

		// Setup and enter
		if err := runWithSpinner("Setting up container", "", func() error {
			if err := setupContainer(containerID, cfg.RemoteUser, ucfg.Dotfiles); err != nil {
				printWarn("Container setup had errors", err.Error())
			}
			return nil
		}); err != nil {
			return err
		}

		printDone("Ready", "")
		printProgress("Entering container", cfg.RemoteUser+"@devc-"+ws.name)
		exitCode, err := containerExecInteractive(ctx, containerID, cfg.RemoteUser, cfg.RemoteWorkspaceFolder, []string{"bash", "-l"})
		if err != nil {
			return fmt.Errorf("interactive exec failed: %w", err)
		}
		os.Exit(exitCode)
		return nil
	}

	// 8. Rebuild: remove existing container + cached image
	if opts.rebuild {
		if containerID, err := findContainerByWorkspace(ws); err == nil {
			printProgress("Removing container", containerID[:12])
			cli, cliErr := getDockerClient()
			if cliErr == nil {
				_ = cli.ContainerRemove(ctx, containerID, container.RemoveOptions{Force: true})
			}
		}
		// Remove cached image
		allFeatures := mergeFeatures(ucfg.Features, cfg.Features)
		baseImage := cfg.Image
		if cfg.Build != nil && cfg.Build.Dockerfile != "" {
			baseImage = fmt.Sprintf("devc-%s-intermediate:latest", ws.name)
		}
		if baseImage != "" {
			tag := computeImageTag(ws.name, baseImage, allFeatures)
			cli, cliErr := getDockerClient()
			if cliErr == nil {
				_, _ = cli.ImageRemove(ctx, tag, image.RemoveOptions{Force: true})
			}
		}
	}

	// 9. Collect and resolve ports
	ports := collectPorts(cfg.Raw, opts.ports)
	resolvedPorts := resolveAllPorts(ports)
	if len(resolvedPorts) > 0 {
		printDone("Ports", strings.Join(resolvedPorts, ", "))
	}

	// 10. Build image (with spinner for feature pull, build output streamed)
	printProgress("Building image", ws.name)
	imageTag, err := buildFeatureImage(ctx, ws, cfg, ucfg.Features)
	if err != nil {
		return fmt.Errorf("image build: %w", err)
	}
	printDone("Image built", imageTag)

	// 11. Create and start container
	containerID, err = createAndStartContainer(ctx, ws, cfg, imageTag, resolvedPorts, buildHostMounts(ucfg))
	if err != nil {
		return fmt.Errorf("container create: %w", err)
	}

	// 12. Lifecycle hooks
	onCreateHooks := parseLifecycleHook(cfg.OnCreateCommand)
	postCreateHooks := parseLifecycleHook(cfg.PostCreateCommand)
	postStartHooks := parseLifecycleHook(cfg.PostStartCommand)
	if err := runLifecycleHooks(ctx, containerID, cfg.RemoteUser, onCreateHooks, postCreateHooks, postStartHooks); err != nil {
		printWarn("Lifecycle hooks had errors", err.Error())
	}

	// 13. Setup container with spinner
	if err := runWithSpinner("Setting up container", "", func() error {
		if err := setupContainer(containerID, cfg.RemoteUser, ucfg.Dotfiles); err != nil {
			printWarn("Container setup had errors", err.Error())
		}
		return nil
	}); err != nil {
		return err
	}

	// 14. Interactive exec into container
	printDone("Ready", "")
	printProgress("Entering container", cfg.RemoteUser+"@devc-"+ws.name)
	exitCode, err := containerExecInteractive(ctx, containerID, cfg.RemoteUser, cfg.RemoteWorkspaceFolder, []string{"bash", "-l"})
	if err != nil {
		return fmt.Errorf("interactive exec failed: %w", err)
	}
	os.Exit(exitCode)
	return nil // unreachable
}

// resolveAllPorts resolves bare port numbers to host:container format.
func resolveAllPorts(ports []string) []string {
	resolved := make([]string, len(ports))
	for i, p := range ports {
		resolved[i] = resolvePort(p)
	}
	return resolved
}
