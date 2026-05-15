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
func InstallFeaturesRuntime(ctx context.Context, containerID, wsDir string, features map[string]map[string]interface{}, rebuild bool) error {
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

	// Read lockfile (ignored on rebuild)
	lockfilePath := build.LockfilePath(wsDir)
	var lockfile *build.Lockfile
	if !rebuild {
		lockfile, _ = build.ReadLockfile(lockfilePath)
	}

	ui.PrintProgress("Installing features", fmt.Sprintf("%d features", len(features)))

	// Ensure staging directory exists in the container
	if err := docker.Exec(ctx, containerID, "root", []string{"mkdir", "-p", "/tmp/build-features"}); err != nil {
		return fmt.Errorf("create feature staging dir: %w", err)
	}

	newLock := &build.Lockfile{Features: make(map[string]build.FeatureLock)}
	for _, ref := range refs {
		opts := features[ref]

		var featureID string
		var result *build.PullResult

		if build.IsLocalFeature(ref) {
			// Local path feature — load from disk
			featureID = strings.TrimPrefix(ref, "./")
			var err error
			result, err = build.LoadLocalFeature(wsDir, ref)
			if err != nil {
				ui.PrintWarn("Feature install failed", featureID)
				fmt.Fprintln(os.Stderr, err)
				continue
			}
			ui.PrintProgress("Installing feature", featureID)
		} else {
			fr, err := build.ParseFeatureRef(ref)
			if err != nil {
				ui.PrintWarn("Skipping feature", fmt.Sprintf("%s: %v", ref, err))
				continue
			}
			featureID = fr.ID

			ui.PrintProgress("Installing feature", featureID)

			// Pull (use locked digest if available)
			var pullErr error
			if lockfile != nil {
				if lock, ok := lockfile.Features[ref]; ok {
					result, pullErr = build.PullFeatureByDigest(ctx, fr, lock.Resolved)
				}
			}
			if result == nil && pullErr == nil {
				result, pullErr = build.PullFeature(ctx, fr)
			}
			if pullErr != nil {
				ui.PrintWarn("Feature install failed", featureID)
				fmt.Fprintln(os.Stderr, pullErr)
				continue
			}

			newLock.Features[ref] = build.FeatureLock{
				Version:  fr.Tag,
				Resolved: result.Digest,
			}
		}

		installErr := func() error {
			// Create tar archive for CopyToContainer
			var buf bytes.Buffer
			tw := tar.NewWriter(&buf)
			if err := build.WriteFeatureFilesToTar(tw, featureID, result.Files.AllFiles); err != nil {
				return fmt.Errorf("tar: %w", err)
			}
			if err := tw.Close(); err != nil {
				return fmt.Errorf("tar: %w", err)
			}

			// Copy into container
			if err := cli.CopyToContainer(ctx, containerID, "/tmp/build-features/", &buf, dockercontainer.CopyToContainerOptions{}); err != nil {
				return fmt.Errorf("copy: %w", err)
			}

			// Build install command with env vars
			featureDir := "/tmp/build-features/" + featureID
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
			ui.PrintWarn("Feature install failed", featureID)
			fmt.Fprintln(os.Stderr, installErr)
		} else {
			ui.PrintDone("Installed feature", featureID)
		}
	}

	// Write lockfile
	if len(newLock.Features) > 0 {
		if err := build.WriteLockfile(lockfilePath, newLock); err != nil {
			ui.PrintWarn("Failed to write lockfile", err.Error())
		} else {
			ui.PrintDone("Lockfile updated", lockfilePath)
		}
	}

	return nil
}

// UpOptions holds options for the compose up flow.
type UpOptions struct {
	Ports   []string
	Rebuild bool
}

// StartComposeServices handles the compose-specific setup: teardown on rebuild,
// port resolution, override YAML generation, docker compose up, and metadata injection.
// It returns the container ID and resolved ports for the caller to finalize.
func StartComposeServices(ctx context.Context, ws config.Workspace, cfg *config.DevcontainerConfig, cc *Config, ucfg *config.UserConfig, opts UpOptions) (string, []string, error) {
	project := Project(ws)

	// 1. Rebuild: tear down existing
	if opts.Rebuild {
		ui.PrintProgress("Removing containers", project)
		_ = Exec(ctx, cc.Files, project, "down", "--remove-orphans")
	}

	// 2. Collect and resolve ports
	ports := config.CollectPorts(cfg.Raw, opts.Ports)
	resolvedPorts := config.ResolveAllPorts(ports)
	if len(resolvedPorts) > 0 {
		ui.PrintDone("Ports", strings.Join(resolvedPorts, ", "))
	}

	// 3. Generate override YAML
	sockDir := daemon.SockDir(ws.ID)
	mounts := config.BuildHostMounts(ucfg, ws.ID, sockDir)
	overridePath, err := WriteOverride(ws, cc, cfg.RemoteWorkspaceFolder, mounts, resolvedPorts, cfg.ContainerEnv)
	if err != nil {
		return "", nil, err
	}
	defer func() { _ = os.Remove(overridePath) }()

	// Build file list: original compose files + override
	allFiles := append(append([]string{}, cc.Files...), overridePath)

	// 4. docker compose up -d
	upArgs := []string{"up", "-d", "--build"}
	if len(cc.RunServices) > 0 {
		upArgs = append(upArgs, cc.RunServices...)
	}

	ui.PrintProgress("Starting services", project)
	if err := ExecStream(ctx, allFiles, project, upArgs...); err != nil {
		return "", nil, fmt.Errorf("compose up: %w", err)
	}

	// 5. Find service container
	containerID, err := FindServiceContainer(ctx, project, cc.Service)
	if err != nil {
		return "", nil, fmt.Errorf("find service container: %w", err)
	}
	ui.PrintDone("Container ready", containerID[:12])

	return containerID, resolvedPorts, nil
}
