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

func newDownCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "down [workspace-dir]",
		Short: "Stop the devcontainer (volumes are preserved)",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			dir := "."
			if len(args) > 0 {
				dir = args[0]
			}
			return runDown(dir)
		},
	}
}

func runDown(dir string) error {
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
		ui.PrintProgress("Stopping containers", project)
		if err := compose.Exec(ctx, files, project, "stop"); err != nil {
			return fmt.Errorf("compose stop failed: %w", err)
		}
	} else {
		containerID, err := docker.FindContainerByWorkspace(ws)
		if err != nil {
			return err
		}
		ui.PrintProgress("Stopping container", containerID[:12])
		cli, err := docker.GetClient()
		if err != nil {
			return fmt.Errorf("docker client: %w", err)
		}
		if err := cli.ContainerStop(ctx, containerID, dockercontainer.StopOptions{}); err != nil {
			return fmt.Errorf("container stop failed: %w", err)
		}
	}

	ui.PrintDone("Down complete", "")
	return nil
}
