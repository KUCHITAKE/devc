package main

import (
	"context"
	"fmt"

	"github.com/docker/docker/api/types/container"
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
		printProgress("Removing containers", project)
		if err := composeExec(ctx, files, project, "down", "-v", "--remove-orphans"); err != nil {
			return fmt.Errorf("compose down -v failed: %w", err)
		}
	} else {
		containerID, err := findContainerByWorkspace(ws)
		if err != nil {
			printWarn("No container found", "")
			printDone("Clean complete", "nothing to remove")
			return nil
		}
		printProgress("Removing container", containerID[:12])
		cli, err := getDockerClient()
		if err != nil {
			return fmt.Errorf("docker client: %w", err)
		}
		if err := cli.ContainerRemove(ctx, containerID, container.RemoveOptions{Force: true}); err != nil {
			return fmt.Errorf("container remove failed: %w", err)
		}
	}

	printDone("Clean complete", "")
	return nil
}
