package main

import (
	"fmt"
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
