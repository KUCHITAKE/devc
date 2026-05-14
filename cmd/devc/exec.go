package main

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/closer/devc/internal/config"
	"github.com/closer/devc/internal/docker"
	"github.com/closer/devc/internal/ui"
	"github.com/spf13/cobra"
)

func newExecCmd() *cobra.Command {
	var dir string
	cmd := &cobra.Command{
		Use:   "exec [flags] [workspace-dir] [-- command...]",
		Short: "Run a command in the devcontainer",
		Long: `Run a command inside a running devcontainer.
If no command is given, an interactive shell is opened.

Examples:
  devc exec -- go test ./...
  devc exec -- make build
  devc exec -d ~/project -- npm run dev
  devc exec                  # opens interactive shell`,
		RunE: func(cmd *cobra.Command, args []string) error {
			wsDir := dir
			if wsDir == "" {
				wsDir = "."
			}

			cmdArgs := cmd.Flags().Args()
			// args after -- are in cmd.ArgsLenAtDash
			if dash := cmd.ArgsLenAtDash(); dash >= 0 {
				// args before -- are workspace dir candidates
				if dash > 0 {
					wsDir = args[0]
				}
				cmdArgs = args[dash:]
			} else if len(args) > 0 {
				// No -- used; all args are workspace dir (max 1)
				wsDir = args[0]
				cmdArgs = nil
			}

			return runExec(wsDir, cmdArgs)
		},
	}
	cmd.Flags().StringVarP(&dir, "dir", "d", "", "Workspace directory (default: current directory)")
	return cmd
}

// execArgs builds the command to execute in the container.
// If no command is given, returns an interactive login shell.
// Otherwise wraps the command in bash -lc to pick up the environment.
func execArgs(cmdArgs []string) []string {
	if len(cmdArgs) == 0 {
		return []string{"bash", "-l"}
	}
	return []string{"bash", "-lc", strings.Join(cmdArgs, " ")}
}

func runExec(dir string, cmdArgs []string) error {
	ctx := context.Background()

	ws, err := config.ResolveWorkspace(dir)
	if err != nil {
		return err
	}

	containerID, err := docker.FindContainerByWorkspace(ws)
	if err != nil {
		return fmt.Errorf("no running devcontainer found for %s: %w", ws.Name, err)
	}

	if !docker.IsContainerRunning(containerID) {
		return fmt.Errorf("devcontainer for %s is stopped — run 'devc up' first", ws.Name)
	}

	cfg, err := config.ParseDevcontainerConfig(ws)
	if err != nil {
		return err
	}

	remoteUser := docker.ResolveRemoteUser(ctx, containerID, cfg.RemoteUser)
	shellCmd := execArgs(cmdArgs)

	exitCode, err := docker.ExecInteractive(ctx, containerID, remoteUser, cfg.RemoteWorkspaceFolder, shellCmd)
	if err != nil {
		return fmt.Errorf("exec failed: %w", err)
	}

	if exitCode != 0 {
		ui.PrintError("Command exited", fmt.Sprintf("exit code %d", exitCode))
		os.Exit(exitCode)
	}
	return nil
}
