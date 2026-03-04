package main

import (
	"context"
	"fmt"

	"github.com/docker/docker/api/types/container"
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
	ws, err := resolveWorkspace(dir)
	if err != nil {
		return err
	}

	raw, _ := readDevcontainerJSON(ws)
	var files []string
	if raw != nil {
		files = composeFiles(ws, raw)
	}

	ctx := context.Background()

	if len(files) > 0 {
		project := composeProject(ws)
		printProgress("Stopping containers", project)
		if err := composeExec(ctx, files, project, "down"); err != nil {
			return fmt.Errorf("compose down failed: %w", err)
		}
	} else {
		containerID, err := findContainerByWorkspace(ws)
		if err != nil {
			return err
		}
		printProgress("Stopping container", containerID[:12])
		cli, err := getDockerClient()
		if err != nil {
			return fmt.Errorf("docker client: %w", err)
		}
		if err := cli.ContainerStop(ctx, containerID, container.StopOptions{}); err != nil {
			return fmt.Errorf("container stop failed: %w", err)
		}
	}

	printDone("Down complete", "")
	return nil
}
