package main

import (
	"fmt"
	"os"
	"os/exec"

	"github.com/charmbracelet/log"
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

	if len(files) > 0 {
		project := ws.name + "_devcontainer"
		log.Info("Stopping containers", "project", project)
		args := []string{"compose", "-p", project}
		for _, f := range files {
			args = append(args, "-f", f)
		}
		args = append(args, "down")
		cmd := exec.Command("docker", args...)
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("docker compose down failed: %w", err)
		}
	} else {
		containerID, err := findContainerByWorkspace(ws)
		if err != nil {
			return err
		}
		log.Info("Stopping container", "id", containerID[:12])
		cmd := exec.Command("docker", "stop", containerID)
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("docker stop failed: %w", err)
		}
	}

	log.Info("Down complete")
	return nil
}
