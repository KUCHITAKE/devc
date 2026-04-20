package compose

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

	"github.com/closer/devc/internal/build"
	"github.com/closer/devc/internal/config"
	"github.com/closer/devc/internal/container"
	"github.com/closer/devc/internal/daemon"
	"github.com/closer/devc/internal/docker"
	"github.com/closer/devc/internal/meta"
	"github.com/closer/devc/internal/ui"
	dockercontainer "github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
)

// Config holds compose-specific fields from devcontainer.json.
type Config struct {
	Files           []string // resolved absolute paths to compose files
	Service         string   // main service name (required)
	RunServices     []string // services to start (nil = all)
	OverrideCommand bool     // inject sleep infinity (default true)
}

// ParseConfig extracts compose fields from devcontainerConfig.Raw.
func ParseConfig(ws config.Workspace, raw map[string]json.RawMessage) (*Config, error) {
	cc := &Config{
		Files:           config.ComposeFiles(ws, raw),
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

// WriteOverride generates a temporary override YAML for the compose service.
// It injects overrideCommand (sleep infinity), mounts, and ports.
// Returns the path to the generated file; caller must clean up.
func WriteOverride(ws config.Workspace, cc *Config, workspaceFolder string, mounts []config.HostMount, ports []string, env map[string]string) (string, error) {
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
		allEnv[meta.DevcContainerEnv] = "1"

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
		if _, err := os.Stat(m.Source); err == nil {
			volumes = append(volumes, fmt.Sprintf("%s:%s", m.Source, m.Target))
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

	overridePath := filepath.Join(ws.Dir, ".devcontainer", ".devc-compose-override.yml")
	if err := os.WriteFile(overridePath, []byte(b.String()), 0o644); err != nil {
		return "", fmt.Errorf("write compose override: %w", err)
	}
	return overridePath, nil
}

// FindServiceContainer finds the container for a specific compose service
// using Docker labels.
func FindServiceContainer(ctx context.Context, project, service string) (string, error) {
	cli, err := docker.GetClient()
	if err != nil {
		return "", fmt.Errorf("docker client: %w", err)
	}

	containers, err := cli.ContainerList(ctx, dockercontainer.ListOptions{
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

// Exec runs `docker compose` with the given args.
// Output is captured; on error the last lines are included in the error message.
func Exec(ctx context.Context, files []string, project string, args ...string) error {
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

// ExecStream runs `docker compose` with the given args, streaming output to stderr.
// Use this for long-running operations where the user should see progress (e.g. build).
func ExecStream(ctx context.Context, files []string, project string, args ...string) error {
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

// Project returns the compose project name for a workspace.
func Project(ws config.Workspace) string {
	return ws.ID + "_devcontainer"
}

// InstallFeaturesRuntime installs OCI features inside a running container.
// Unlike the image-based flow (which bakes features into the image at build time),
// this runs install.sh at runtime via exec — used for compose-based devcontainers.
func InstallFeaturesRuntime(ctx context.Context, containerID string, features map[string]map[string]interface{}) error {
	if len(features) == 0 {
		return nil
	}

	cli, err := docker.GetClient()
	if err != nil {
		return fmt.Errorf("docker client: %w", err)
	}

	// Sort feature refs for deterministic order
	refs := make([]string, 0, len(features))
	for ref := range features {
		refs = append(refs, ref)
	}
	sort.Strings(refs)

	ui.PrintProgress("Installing features", fmt.Sprintf("%d features", len(features)))

	// Ensure staging directory exists in the container
	if err := docker.Exec(ctx, containerID, "root", []string{"mkdir", "-p", "/tmp/build-features"}); err != nil {
		return fmt.Errorf("create feature staging dir: %w", err)
	}

	for _, ref := range refs {
		opts := features[ref]
		fr, err := build.ParseFeatureRef(ref)
		if err != nil {
			ui.PrintWarn("Skipping feature", fmt.Sprintf("%s: %v", ref, err))
			continue
		}

		ui.PrintProgress("Installing feature", fr.ID)

		installErr := func() error {
			// Pull
			files, pullErr := build.PullFeature(ctx, fr)
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
			if err := cli.CopyToContainer(ctx, containerID, "/tmp/build-features/", &buf, dockercontainer.CopyToContainerOptions{}); err != nil {
				return fmt.Errorf("copy: %w", err)
			}

			// Build install command with env vars
			featureDir := "/tmp/build-features/" + fr.ID
			envs := build.FeatureEnvVars(opts)
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
			return docker.ExecTail(ctx, containerID, "root", []string{"sh", "-c", installCmd})
		}()

		if installErr != nil {
			ui.PrintWarn("Feature install failed", fr.ID)
			fmt.Fprintln(os.Stderr, installErr)
		} else {
			ui.PrintDone("Installed feature", fr.ID)
		}
	}

	return nil
}

// UpOptions holds options for the compose up flow.
type UpOptions struct {
	Ports   []string
	Rebuild bool
}

// RunUpCompose is the compose-based equivalent of the image-based flow in RunUp.
func RunUpCompose(ctx context.Context, ws config.Workspace, cfg *config.DevcontainerConfig, cc *Config, ucfg *config.UserConfig, opts UpOptions, version string, enterContainer func(ctx context.Context, ws config.Workspace, containerID, remoteUser, workspaceFolder string, staticPorts []string) error) error {
	project := Project(ws)

	// 1. Check existing service container
	containerID, findErr := FindServiceContainer(ctx, project, cc.Service)

	if findErr == nil && !opts.Rebuild {
		if docker.IsContainerRunning(containerID) {
			// Already running — attach directly
			ui.PrintDone("Attaching to container", containerID[:12])
		} else {
			// Stopped — restart
			ui.PrintProgress("Restarting services", project)

			// Ensure daemon socket directory exists (may be lost after host reboot)
			sockDir := daemon.SockDir(ws.ID)
			if err := os.MkdirAll(sockDir, 0o755); err != nil {
				return fmt.Errorf("create daemon socket dir: %w", err)
			}

			if err := Exec(ctx, cc.Files, project, "start"); err != nil {
				return fmt.Errorf("compose start: %w", err)
			}
			// Re-find container after start
			containerID, findErr = FindServiceContainer(ctx, project, cc.Service)
			if findErr != nil {
				return fmt.Errorf("find container after start: %w", findErr)
			}
			// Run postStartCommand only
			postStartHooks := container.ParseLifecycleHook(cfg.PostStartCommand)
			if err := container.RunLifecycleHooks(ctx, containerID, cfg.RemoteUser, postStartHooks); err != nil {
				ui.PrintWarn("Lifecycle hooks had errors", err.Error())
			}
		}

		// Ensure devc binary is present (may be lost if /tmp was cleaned)
		if err := meta.EnsureDevcBinary(daemon.SockDir(ws.ID)); err != nil {
			ui.PrintWarn("devc binary restore failed", err.Error())
		}

		// Resolve remote user (fall back to root if user doesn't exist)
		cfg.RemoteUser = docker.ResolveRemoteUser(ctx, containerID, cfg.RemoteUser)

		// Setup and enter
		if err := ui.RunWithSpinner("Setting up container", "", func() error {
			if err := docker.SetupContainer(containerID, cfg.RemoteUser, ucfg.Dotfiles); err != nil {
				ui.PrintWarn("Container setup had errors", err.Error())
			}
			return nil
		}); err != nil {
			return err
		}

		return enterContainer(ctx, ws, containerID, cfg.RemoteUser, cfg.RemoteWorkspaceFolder, nil)
	}

	// 2. Rebuild: tear down existing
	if opts.Rebuild {
		ui.PrintProgress("Removing containers", project)
		_ = Exec(ctx, cc.Files, project, "down", "--remove-orphans")
	}

	// 3. Collect and resolve ports
	ports := config.CollectPorts(cfg.Raw, opts.Ports)
	resolvedPorts := config.ResolveAllPorts(ports)
	if len(resolvedPorts) > 0 {
		ui.PrintDone("Ports", strings.Join(resolvedPorts, ", "))
	}

	// 4. Generate override YAML
	sockDir := daemon.SockDir(ws.ID)
	mounts := config.BuildHostMounts(ucfg, ws.ID, sockDir)
	overridePath, err := WriteOverride(ws, cc, cfg.RemoteWorkspaceFolder, mounts, resolvedPorts, cfg.ContainerEnv)
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

	ui.PrintProgress("Starting services", project)
	if err := ExecStream(ctx, allFiles, project, upArgs...); err != nil {
		return fmt.Errorf("compose up: %w", err)
	}

	// 6. Find service container
	containerID, err = FindServiceContainer(ctx, project, cc.Service)
	if err != nil {
		return fmt.Errorf("find service container: %w", err)
	}
	ui.PrintDone("Container ready", containerID[:12])

	// 7. Resolve remote user (fall back to root if user doesn't exist)
	cfg.RemoteUser = docker.ResolveRemoteUser(ctx, containerID, cfg.RemoteUser)

	// 8. Install features at runtime (compose can't bake them into the image)
	allFeatures := build.MergeFeatures(ucfg.Features, cfg.Features)
	if err := InstallFeaturesRuntime(ctx, containerID, allFeatures); err != nil {
		ui.PrintWarn("Feature installation had errors", err.Error())
	}

	// 9. Inject devc binary and metadata
	m := meta.BuildContainerMeta(ws, cfg, resolvedPorts, allFeatures, ucfg.Dotfiles, "compose", "", version)
	if err := meta.InjectIntoContainer(ctx, containerID, ws.ID, m, sockDir); err != nil {
		ui.PrintWarn("devc injection failed", err.Error())
	}

	// 10. Lifecycle hooks
	onCreateHooks := container.ParseLifecycleHook(cfg.OnCreateCommand)
	postCreateHooks := container.ParseLifecycleHook(cfg.PostCreateCommand)
	postStartHooks := container.ParseLifecycleHook(cfg.PostStartCommand)
	if err := container.RunLifecycleHooks(ctx, containerID, cfg.RemoteUser, onCreateHooks, postCreateHooks, postStartHooks); err != nil {
		ui.PrintWarn("Lifecycle hooks had errors", err.Error())
	}

	// 11. Setup container
	if err := ui.RunWithSpinner("Setting up container", "", func() error {
		if err := docker.SetupContainer(containerID, cfg.RemoteUser, ucfg.Dotfiles); err != nil {
			ui.PrintWarn("Container setup had errors", err.Error())
		}
		return nil
	}); err != nil {
		return err
	}

	// 12. Enter container
	return enterContainer(ctx, ws, containerID, cfg.RemoteUser, cfg.RemoteWorkspaceFolder, resolvedPorts)
}
