package main

import (
	"fmt"
	"os"
	"os/exec"

	"github.com/charmbracelet/log"
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

	if len(files) > 0 {
		project := ws.name + "_devcontainer"
		log.Info("Removing containers and volumes", "project", project)
		args := []string{"compose", "-p", project}
		for _, f := range files {
			args = append(args, "-f", f)
		}
		args = append(args, "down", "-v")
		cmd := exec.Command("docker", args...)
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("docker compose down -v failed: %w", err)
		}
	} else {
		containerID, err := findContainerByWorkspace(ws)
		if err != nil {
			log.Warn("No container found", "err", err)
			log.Info("Clean complete (nothing to remove)")
			return nil
		}
		log.Info("Removing container", "id", containerID[:12])
		cmd := exec.Command("docker", "rm", "-f", containerID)
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("docker rm failed: %w", err)
		}
	}

	log.Info("Clean complete")
	return nil
}
