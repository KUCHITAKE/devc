package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
)

// buildInternalRootCmd returns the command tree for container-internal usage.
func buildInternalRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:   "devc <command>",
		Short: "devc container utilities",
		Long:  "Utilities available inside a devc-managed container.",
		Version:       version,
		SilenceUsage:  true,
		SilenceErrors: true,
	}

	root.AddCommand(newInfoCmd())
	root.AddCommand(newEnvCmd())
	root.AddCommand(newDotfilesCmd())
	root.AddCommand(newPortCmd())
	root.AddCommand(newHostCmd())
	root.AddCommand(newRebuildInternalCmd())

	return root
}

func newInfoCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "info",
		Short: "Show container metadata",
		RunE: func(cmd *cobra.Command, args []string) error {
			meta, err := loadContainerMeta()
			if err != nil {
				return err
			}

			fmt.Printf("Project:     %s\n", meta.Project)
			fmt.Printf("Mode:        %s\n", meta.Mode)
			fmt.Printf("Workspace:   %s\n", meta.WorkspaceMount)
			fmt.Printf("Remote user: %s\n", meta.RemoteUser)
			fmt.Printf("Image:       %s\n", meta.Image)
			fmt.Printf("Arch:        %s\n", meta.Arch)
			fmt.Printf("devc:        %s\n", meta.Version)
			fmt.Printf("Created:     %s\n", meta.CreatedAt)

			if len(meta.Ports) > 0 {
				fmt.Printf("Ports:       %s\n", strings.Join(meta.Ports, ", "))
			}
			if len(meta.Features) > 0 {
				fmt.Printf("Features:\n")
				for _, f := range meta.Features {
					fmt.Printf("  - %s\n", f)
				}
			}
			if len(meta.Dotfiles) > 0 {
				fmt.Printf("Dotfiles:\n")
				for _, d := range meta.Dotfiles {
					fmt.Printf("  - %s\n", d)
				}
			}
			return nil
		},
	}
}

func newEnvCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "env",
		Short: "Show devc-injected environment variables",
		RunE: func(cmd *cobra.Command, args []string) error {
			meta, err := loadContainerMeta()
			if err != nil {
				return err
			}

			// Show DEVC_ env vars from current environment
			fmt.Println("# devc environment variables")
			for _, env := range os.Environ() {
				if strings.HasPrefix(env, "DEVC_") {
					fmt.Println(env)
				}
			}

			// Show containerEnv from meta
			if len(meta.ContainerEnv) > 0 {
				fmt.Println("\n# containerEnv (from devcontainer.json)")
				for k, v := range meta.ContainerEnv {
					fmt.Printf("%s=%s\n", k, v)
				}
			}
			return nil
		},
	}
}

func newDotfilesCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "dotfiles",
		Short: "Manage dotfiles",
	}

	cmd.AddCommand(&cobra.Command{
		Use:   "sync",
		Short: "Re-sync dotfiles symlinks",
		RunE: func(cmd *cobra.Command, args []string) error {
			meta, err := loadContainerMeta()
			if err != nil {
				return err
			}

			if len(meta.Dotfiles) == 0 {
				fmt.Println("No dotfiles configured.")
				return nil
			}

			home := os.Getenv("HOME")
			if home == "" {
				return fmt.Errorf("HOME not set")
			}

			for _, df := range meta.Dotfiles {
				rel := dotfileRelPath(df)
				staging := filepath.Join(dotfilesDir, rel)
				target := filepath.Join(home, rel)

				// Check staging file exists
				if _, err := os.Stat(staging); err != nil {
					printWarn("Skip", fmt.Sprintf("%s (not found in staging)", rel))
					continue
				}

				// Ensure parent directory
				if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
					printWarn("Skip", fmt.Sprintf("%s: %v", rel, err))
					continue
				}

				// Create symlink
				_ = os.Remove(target)
				if err := os.Symlink(staging, target); err != nil {
					printWarn("Failed", fmt.Sprintf("%s: %v", rel, err))
				} else {
					printDone("Linked", fmt.Sprintf("%s → %s", target, staging))
				}
			}
			return nil
		},
	})

	return cmd
}

// sendDaemonRequest sends a request to the host daemon via the Unix socket.
func sendDaemonRequest(req daemonRequest) (*daemonResponse, error) {
	conn, err := net.Dial("unix", devcSockPath)
	if err != nil {
		return nil, fmt.Errorf("connect to devc daemon: %w (is the host devc process running?)", err)
	}
	defer func() { _ = conn.Close() }()

	data, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	if _, err := conn.Write(data); err != nil {
		return nil, fmt.Errorf("send request: %w", err)
	}
	// Signal that we're done writing so the host can read the full request
	if uc, ok := conn.(*net.UnixConn); ok {
		_ = uc.CloseWrite()
	}

	respData, err := io.ReadAll(conn)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	var resp daemonResponse
	if err := json.Unmarshal(respData, &resp); err != nil {
		return nil, fmt.Errorf("parse response: %w", err)
	}
	return &resp, nil
}

func newPortCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "port <port> [host:container]",
		Short: "Forward a port from the container to the host",
		Long: `Forward a container port to the host machine.

Examples:
  devc port 8080          # forward container:8080 → host:8080
  devc port 9090:3000     # forward container:3000 → host:9090`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			resp, err := sendDaemonRequest(daemonRequest{
				Type: "port",
				Port: args[0],
			})
			if err != nil {
				return err
			}
			if !resp.OK {
				return fmt.Errorf("%s", resp.Message)
			}
			printDone("Port forwarded", resp.Message)
			return nil
		},
	}
}

func newHostCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "host <command> [args...]",
		Short: "Execute a command on the host machine",
		Long: `Run a command on the host machine from inside the container.

Examples:
  devc host open http://localhost:3000
  devc host code .
  devc host xdg-open file.pdf`,
		Args:               cobra.MinimumNArgs(1),
		DisableFlagParsing: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			resp, err := sendDaemonRequest(daemonRequest{
				Type:    "host",
				Command: args,
			})
			if err != nil {
				return err
			}
			if resp.Output != "" {
				fmt.Print(resp.Output)
			}
			if !resp.OK {
				return fmt.Errorf("host command failed: %s", resp.Message)
			}
			return nil
		},
	}
}

func newRebuildInternalCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "rebuild",
		Short: "Request a container rebuild",
		Long:  "Signal the host to rebuild the container. Exit the container after running this command.",
		RunE: func(cmd *cobra.Command, args []string) error {
			resp, err := sendDaemonRequest(daemonRequest{
				Type: "rebuild",
			})
			if err != nil {
				return err
			}
			if !resp.OK {
				return fmt.Errorf("%s", resp.Message)
			}
			printDone("Rebuild requested", resp.Message)
			return nil
		},
	}
}
