package main

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
)

// composeConfig holds compose-specific fields from devcontainer.json.
type composeConfig struct {
	Files           []string // resolved absolute paths to compose files
	Service         string   // main service name (required)
	RunServices     []string // services to start (nil = all)
	OverrideCommand bool     // inject sleep infinity (default true)
}

// parseComposeConfig extracts compose fields from devcontainerConfig.Raw.
func parseComposeConfig(ws workspace, raw map[string]json.RawMessage) (*composeConfig, error) {
	cc := &composeConfig{
		Files:           composeFiles(ws, raw),
		OverrideCommand: true,
	}

	// service (required)
	if v, ok := raw["service"]; ok {
		if err := json.Unmarshal(v, &cc.Service); err != nil {
			return nil, fmt.Errorf("parse service: %w", err)
		}
	}
	if cc.Service == "" {
		return nil, fmt.Errorf("compose-based devcontainer requires \"service\" field")
	}

	// runServices (optional)
	if v, ok := raw["runServices"]; ok {
		if err := json.Unmarshal(v, &cc.RunServices); err != nil {
			return nil, fmt.Errorf("parse runServices: %w", err)
		}
	}

	// overrideCommand (optional, default true)
	if v, ok := raw["overrideCommand"]; ok {
		if err := json.Unmarshal(v, &cc.OverrideCommand); err != nil {
			return nil, fmt.Errorf("parse overrideCommand: %w", err)
		}
	}

	return cc, nil
}

// writeComposeOverride generates a temporary override YAML for the compose service.
// It injects overrideCommand (sleep infinity), mounts, and ports.
// Returns the path to the generated file; caller must clean up.
func writeComposeOverride(ws workspace, cc *composeConfig, workspaceFolder string, mounts []hostMount, ports []string, env map[string]string) (string, error) {
	var b strings.Builder
	b.WriteString("services:\n")
	b.WriteString("  " + cc.Service + ":\n")

	if cc.OverrideCommand {
		b.WriteString("    command: sleep infinity\n")
	}

	// Set working directory so lifecycle hooks and exec run in the workspace
	if workspaceFolder != "" {
		b.WriteString("    working_dir: " + workspaceFolder + "\n")
	}

	// Environment variables: DEVC_CONTAINER + containerEnv
	{
		allEnv := make(map[string]string)
		for k, v := range env {
			allEnv[k] = v
		}
		allEnv[devcContainerEnv] = "1"

		envKeys := make([]string, 0, len(allEnv))
		for k := range allEnv {
			envKeys = append(envKeys, k)
		}
		sort.Strings(envKeys)
		b.WriteString("    environment:\n")
		for _, k := range envKeys {
			fmt.Fprintf(&b, "      %s: %q\n", k, allEnv[k])
		}
	}

	// Volumes: user mounts only (dotfiles, credentials).
	// The workspace bind mount is defined in the compose file itself.
	var volumes []string
	for _, m := range mounts {
		if _, err := os.Stat(m.source); err == nil {
			volumes = append(volumes, fmt.Sprintf("%s:%s", m.source, m.target))
		}
	}
	if len(volumes) > 0 {
		b.WriteString("    volumes:\n")
		for _, v := range volumes {
			b.WriteString("      - " + v + "\n")
		}
	}

	// Ports
	if len(ports) > 0 {
		b.WriteString("    ports:\n")
		for _, p := range ports {
			fmt.Fprintf(&b, "      - %q\n", p)
		}
	}

	overridePath := filepath.Join(ws.dir, ".devcontainer", ".devc-compose-override.yml")
	if err := os.WriteFile(overridePath, []byte(b.String()), 0o644); err != nil {
		return "", fmt.Errorf("write compose override: %w", err)
	}
	return overridePath, nil
}

// findComposeServiceContainer finds the container for a specific compose service
// using Docker labels.
func findComposeServiceContainer(ctx context.Context, project, service string) (string, error) {
	cli, err := getDockerClient()
	if err != nil {
		return "", fmt.Errorf("docker client: %w", err)
	}

	containers, err := cli.ContainerList(ctx, container.ListOptions{
		All: true,
		Filters: filters.NewArgs(
			filters.Arg("label", "com.docker.compose.project="+project),
			filters.Arg("label", "com.docker.compose.service="+service),
		),
	})
	if err != nil {
		return "", fmt.Errorf("container list: %w", err)
	}
	if len(containers) == 0 {
		return "", fmt.Errorf("no container found for service %q in project %q", service, project)
	}
	return containers[0].ID, nil
}

// composeExec runs `docker compose` with the given args.
// Output is captured; on error the last lines are included in the error message.
func composeExec(ctx context.Context, files []string, project string, args ...string) error {
	cmdArgs := []string{"compose"}
	for _, f := range files {
		cmdArgs = append(cmdArgs, "-f", f)
	}
	cmdArgs = append(cmdArgs, "-p", project)
	cmdArgs = append(cmdArgs, args...)

	cmd := exec.CommandContext(ctx, "docker", cmdArgs...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		// Show last 20 lines for context
		lines := strings.Split(strings.TrimSpace(string(out)), "\n")
		if len(lines) > 20 {
			lines = lines[len(lines)-20:]
		}
		return fmt.Errorf("docker compose %s: %w\n%s", strings.Join(args, " "), err, strings.Join(lines, "\n"))
	}
	return nil
}

// composeExecStream runs `docker compose` with the given args, streaming output to stderr.
// Use this for long-running operations where the user should see progress (e.g. build).
func composeExecStream(ctx context.Context, files []string, project string, args ...string) error {
	cmdArgs := []string{"compose"}
	for _, f := range files {
		cmdArgs = append(cmdArgs, "-f", f)
	}
	cmdArgs = append(cmdArgs, "-p", project)
	cmdArgs = append(cmdArgs, args...)

	cmd := exec.CommandContext(ctx, "docker", cmdArgs...)
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("docker compose %s: %w", strings.Join(args, " "), err)
	}
	return nil
}

// composeProject returns the compose project name for a workspace.
func composeProject(ws workspace) string {
	return ws.name + "_devcontainer"
}

// installFeaturesRuntime installs OCI features inside a running container.
// Unlike the image-based flow (which bakes features into the image at build time),
// this runs install.sh at runtime via exec — used for compose-based devcontainers.
func installFeaturesRuntime(ctx context.Context, containerID string, features map[string]map[string]interface{}) error {
	if len(features) == 0 {
		return nil
	}

	cli, err := getDockerClient()
	if err != nil {
		return fmt.Errorf("docker client: %w", err)
	}

	// Sort feature refs for deterministic order
	refs := make([]string, 0, len(features))
	for ref := range features {
		refs = append(refs, ref)
	}
	sort.Strings(refs)

	printProgress("Installing features", fmt.Sprintf("%d features", len(features)))

	// Ensure staging directory exists in the container
	if err := containerExec(ctx, containerID, "root", []string{"mkdir", "-p", "/tmp/build-features"}); err != nil {
		return fmt.Errorf("create feature staging dir: %w", err)
	}

	for _, ref := range refs {
		opts := features[ref]
		fr, err := parseFeatureRef(ref)
		if err != nil {
			printWarn("Skipping feature", fmt.Sprintf("%s: %v", ref, err))
			continue
		}

		printProgress("Installing feature", fr.ID)

		installErr := func() error {
			// Pull
			files, pullErr := pullFeature(ctx, fr)
			if pullErr != nil {
				return fmt.Errorf("pull: %w", pullErr)
			}

			// Create tar archive for CopyToContainer
			var buf bytes.Buffer
			tw := tar.NewWriter(&buf)
			for name, data := range files.AllFiles {
				if err := tw.WriteHeader(&tar.Header{
					Name: fr.ID + "/" + name,
					Mode: 0o755,
					Size: int64(len(data)),
				}); err != nil {
					return fmt.Errorf("tar: %w", err)
				}
				if _, err := tw.Write(data); err != nil {
					return fmt.Errorf("tar: %w", err)
				}
			}
			if err := tw.Close(); err != nil {
				return fmt.Errorf("tar: %w", err)
			}

			// Copy into container
			if err := cli.CopyToContainer(ctx, containerID, "/tmp/build-features/", &buf, container.CopyToContainerOptions{}); err != nil {
				return fmt.Errorf("copy: %w", err)
			}

			// Build install command with env vars
			featureDir := "/tmp/build-features/" + fr.ID
			envs := featureEnvVars(opts)
			var cmdParts []string
			cmdParts = append(cmdParts, "cd "+featureDir)
			envKeys := make([]string, 0, len(envs))
			for k := range envs {
				envKeys = append(envKeys, k)
			}
			sort.Strings(envKeys)
			for _, k := range envKeys {
				cmdParts = append(cmdParts, fmt.Sprintf("export %s='%s'", k, envs[k]))
			}
			cmdParts = append(cmdParts, "chmod +x install.sh && ./install.sh")
			cmdParts = append(cmdParts, "rm -rf "+featureDir)

			installCmd := strings.Join(cmdParts, " && ")
			return containerExecTail(ctx, containerID, "root", []string{"sh", "-c", installCmd})
		}()

		if installErr != nil {
			printWarn("Feature install failed", fmt.Sprintf("%s: %v", fr.ID, installErr))
		} else {
			printDone("Installed feature", fr.ID)
		}
	}

	return nil
}

// runUpCompose is the compose-based equivalent of the image-based flow in runUp.
func runUpCompose(ctx context.Context, ws workspace, cfg *devcontainerConfig, cc *composeConfig, ucfg *userConfig, opts upOptions) error {
	project := composeProject(ws)

	// 1. Check existing service container
	containerID, findErr := findComposeServiceContainer(ctx, project, cc.Service)

	if findErr == nil && !opts.rebuild {
		if isContainerRunning(containerID) {
			// Already running — attach directly
			printDone("Attaching to container", containerID[:12])
		} else {
			// Stopped — restart
			printProgress("Restarting services", project)
			if err := composeExec(ctx, cc.Files, project, "start"); err != nil {
				return fmt.Errorf("compose start: %w", err)
			}
			// Re-find container after start
			containerID, findErr = findComposeServiceContainer(ctx, project, cc.Service)
			if findErr != nil {
				return fmt.Errorf("find container after start: %w", findErr)
			}
			// Run postStartCommand only
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
		printProgress("Entering container", cfg.RemoteUser+"@"+cc.Service)
		exitCode, err := containerExecInteractive(ctx, containerID, cfg.RemoteUser, cfg.RemoteWorkspaceFolder, []string{"bash", "-l"})
		if err != nil {
			return fmt.Errorf("interactive exec failed: %w", err)
		}
		os.Exit(exitCode)
		return nil
	}

	// 2. Rebuild: tear down existing
	if opts.rebuild {
		printProgress("Removing containers", project)
		_ = composeExec(ctx, cc.Files, project, "down", "--remove-orphans")
	}

	// 3. Collect and resolve ports
	ports := collectPorts(cfg.Raw, opts.ports)
	resolvedPorts := resolveAllPorts(ports)
	if len(resolvedPorts) > 0 {
		printDone("Ports", strings.Join(resolvedPorts, ", "))
	}

	// 4. Generate override YAML
	mounts := buildHostMounts(ucfg)
	overridePath, err := writeComposeOverride(ws, cc, cfg.RemoteWorkspaceFolder, mounts, resolvedPorts, cfg.ContainerEnv)
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(overridePath) }()

	// Build file list: original compose files + override
	allFiles := append(append([]string{}, cc.Files...), overridePath)

	// 5. docker compose up -d
	upArgs := []string{"up", "-d", "--build"}
	if len(cc.RunServices) > 0 {
		upArgs = append(upArgs, cc.RunServices...)
	}

	printProgress("Starting services", project)
	if err := composeExecStream(ctx, allFiles, project, upArgs...); err != nil {
		return fmt.Errorf("compose up: %w", err)
	}

	// 6. Find service container
	containerID, err = findComposeServiceContainer(ctx, project, cc.Service)
	if err != nil {
		return fmt.Errorf("find service container: %w", err)
	}
	printDone("Container ready", containerID[:12])

	// 7. Install features at runtime (compose can't bake them into the image)
	allFeatures := mergeFeatures(ucfg.Features, cfg.Features)
	if err := installFeaturesRuntime(ctx, containerID, allFeatures); err != nil {
		printWarn("Feature installation had errors", err.Error())
	}

	// 8. Inject devc binary and metadata
	meta := buildContainerMeta(ws, cfg, resolvedPorts, allFeatures, ucfg.Dotfiles, "compose", "")
	if err := injectDevcIntoContainer(ctx, containerID, meta); err != nil {
		printWarn("devc injection failed", err.Error())
	}

	// 9. Lifecycle hooks
	onCreateHooks := parseLifecycleHook(cfg.OnCreateCommand)
	postCreateHooks := parseLifecycleHook(cfg.PostCreateCommand)
	postStartHooks := parseLifecycleHook(cfg.PostStartCommand)
	if err := runLifecycleHooks(ctx, containerID, cfg.RemoteUser, onCreateHooks, postCreateHooks, postStartHooks); err != nil {
		printWarn("Lifecycle hooks had errors", err.Error())
	}

	// 10. Setup container
	if err := runWithSpinner("Setting up container", "", func() error {
		if err := setupContainer(containerID, cfg.RemoteUser, ucfg.Dotfiles); err != nil {
			printWarn("Container setup had errors", err.Error())
		}
		return nil
	}); err != nil {
		return err
	}

	// 11. Interactive exec
	printDone("Ready", "")
	printProgress("Entering container", cfg.RemoteUser+"@"+cc.Service)
	exitCode, err := containerExecInteractive(ctx, containerID, cfg.RemoteUser, cfg.RemoteWorkspaceFolder, []string{"bash", "-l"})
	if err != nil {
		return fmt.Errorf("interactive exec failed: %w", err)
	}
	os.Exit(exitCode)
	return nil
}
