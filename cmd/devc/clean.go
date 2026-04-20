package main

import (
	"context"
	"fmt"

	"github.com/closer/devc/internal/compose"
	"github.com/closer/devc/internal/config"
	"github.com/closer/devc/internal/docker"
	"github.com/closer/devc/internal/ui"
	dockercontainer "github.com/docker/docker/api/types/container"
	"github.com/spf13/cobra"
)

func newCleanCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "clean [workspace-dir]",
		Short: "Remove containers and volumes (fresh DB, etc.)",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			dir := "."
			if len(args) > 0 {
				dir = args[0]
			}
			return runClean(dir)
		},
	}
}

func runClean(dir string) error {
	ws, err := config.ResolveWorkspace(dir)
	if err != nil {
		return err
	}

	raw, _ := config.ReadDevcontainerJSON(ws)
	var files []string
	if raw != nil {
		files = config.ComposeFiles(ws, raw)
	}

	ctx := context.Background()

	if len(files) > 0 {
		project := compose.Project(ws)
		ui.PrintProgress("Removing containers", project)
		if err := compose.Exec(ctx, files, project, "down", "-v", "--remove-orphans"); err != nil {
			return fmt.Errorf("compose down -v failed: %w", err)
		}
	} else {
		containerID, err := docker.FindContainerByWorkspace(ws)
		if err != nil {
			ui.PrintWarn("No container found", "")
			ui.PrintDone("Clean complete", "nothing to remove")
			return nil
		}
		ui.PrintProgress("Removing container", containerID[:12])
		cli, err := docker.GetClient()
		if err != nil {
			return fmt.Errorf("docker client: %w", err)
		}
		if err := cli.ContainerRemove(ctx, containerID, dockercontainer.RemoveOptions{Force: true}); err != nil {
			return fmt.Errorf("container remove failed: %w", err)
		}
	}

	ui.PrintDone("Clean complete", "")
	return nil
}
