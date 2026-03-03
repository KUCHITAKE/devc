package main

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"syscall"

	"github.com/charmbracelet/huh/spinner"
	"github.com/charmbracelet/log"
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
	// 1. Resolve workspace
	ws, err := resolveWorkspace(dir)
	if err != nil {
		return err
	}

	// 2. Extract credentials
	if err := extractCredentials(); err != nil {
		return err
	}

	// 3. Ensure devcontainer.json exists
	if err := ensureDevcontainerJSON(ws); err != nil {
		return err
	}

	// 4. Read devcontainer.json and collect ports
	raw, err := readDevcontainerJSON(ws)
	if err != nil {
		return err
	}

	existing := isContainerRunning(ws)

	// 5. Build merged config with port args (skip port resolution for existing containers)
	var configPath string
	if !existing {
		ports := collectPorts(raw, opts.ports)
		configPath, err = mergedConfigPath(ws, raw, ports)
		if err != nil {
			return err
		}
	}
	if configPath != "" {
		defer func() { _ = os.Remove(configPath) }()
	}

	// 6. Build mount args
	mountArgs := buildMountArgs(hostMounts())

	// 7. Run devcontainer up
	if existing {
		log.Info("Attaching to existing container", "project", ws.name)
	} else {
		log.Info("Starting devcontainer", "project", ws.name)
	}
	cmdArgs := []string{"up",
		"--workspace-folder", ws.dir,
		"--additional-features", additionalFeatures,
	}
	cmdArgs = append(cmdArgs, mountArgs...)
	if opts.rebuild {
		cmdArgs = append(cmdArgs, "--remove-existing-container")
	}
	if configPath != "" {
		cmdArgs = append(cmdArgs, "--config", configPath)
	}

	var stdout bytes.Buffer
	cmd := exec.Command("devcontainer", cmdArgs...)
	cmd.Stdout = &stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("devcontainer up failed: %w\n%s", err, stdout.String())
	}

	// 8. Parse output
	result, err := parseUpOutput(stdout.Bytes())
	if err != nil {
		return fmt.Errorf("%w\nOutput:\n%s", err, stdout.String())
	}

	if result.RemoteUser == "" {
		result.RemoteUser = "vscode"
	}
	if result.RemoteWorkspaceFolder == "" {
		result.RemoteWorkspaceFolder = "/workspaces/" + ws.name
	}

	log.Info("Entering container", "id", result.ContainerID[:12], "user", result.RemoteUser)

	// 9. Setup container with spinner
	if err := spinner.New().
		Title("Setting up container...").
		Action(func() {
			if err := setupContainer(result.ContainerID, result.RemoteUser); err != nil {
				log.Warn("Container setup had errors", "err", err)
			}
		}).
		Run(); err != nil {
		return err
	}

	// 10. Clean up temp config before exec (defer won't run after syscall.Exec)
	if configPath != "" {
		_ = os.Remove(configPath)
	}

	// Process replacement with docker exec
	dockerBin, err := exec.LookPath("docker")
	if err != nil {
		return fmt.Errorf("docker not found: %w", err)
	}
	execArgs := []string{"docker", "exec", "-it",
		"-u", result.RemoteUser,
		"-w", result.RemoteWorkspaceFolder,
		result.ContainerID, "bash", "-l",
	}
	return syscall.Exec(dockerBin, execArgs, os.Environ())
}
