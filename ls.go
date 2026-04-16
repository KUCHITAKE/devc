package main

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	"github.com/spf13/cobra"
)

func newLsCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "ls",
		Aliases: []string{"list", "ps"},
		Short:   "List devc containers",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runLs()
		},
	}
}

func runLs() error {
	ctx := context.Background()
	cli, err := getDockerClient()
	if err != nil {
		return fmt.Errorf("docker client: %w", err)
	}

	containers, err := cli.ContainerList(ctx, container.ListOptions{
		All: true,
		Filters: filters.NewArgs(
			filters.Arg("label", "devcontainer.local_folder"),
		),
	})
	if err != nil {
		return fmt.Errorf("list containers: %w", err)
	}

	if len(containers) == 0 {
		fmt.Println("No devc containers found.")
		return nil
	}

	sort.Slice(containers, func(i, j int) bool {
		return containers[i].Created > containers[j].Created
	})

	w := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
	if _, err := fmt.Fprintln(w, "WORKSPACE\tSTATUS\tPORTS\tUPTIME\tPATH"); err != nil {
		return err
	}

	for _, c := range containers {
		name := containerDisplayName(c)
		status := c.State
		ports := formatPorts(c.Ports)
		uptime := formatUptime(c.State, c.Created)
		path := c.Labels["devcontainer.local_folder"]

		if _, err := fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", name, status, ports, uptime, path); err != nil {
			return err
		}
	}

	return w.Flush()
}

func containerDisplayName(c container.Summary) string {
	for _, name := range c.Names {
		name = strings.TrimPrefix(name, "/")
		if strings.HasPrefix(name, "devc-") {
			return strings.TrimPrefix(name, "devc-")
		}
	}
	// Compose container: use project label if available
	if project, ok := c.Labels["com.docker.compose.project"]; ok {
		svc := c.Labels["com.docker.compose.service"]
		if svc != "" {
			return project + "/" + svc
		}
		return project
	}
	if len(c.Names) > 0 {
		return strings.TrimPrefix(c.Names[0], "/")
	}
	return c.ID[:12]
}

func formatPorts(ports []container.Port) string {
	if len(ports) == 0 {
		return "-"
	}

	seen := make(map[string]bool)
	var parts []string
	for _, p := range ports {
		if p.PublicPort == 0 {
			continue
		}
		var s string
		if p.PublicPort == p.PrivatePort {
			s = fmt.Sprintf("%d", p.PublicPort)
		} else {
			s = fmt.Sprintf("%d→%d", p.PublicPort, p.PrivatePort)
		}
		if !seen[s] {
			seen[s] = true
			parts = append(parts, s)
		}
	}

	if len(parts) == 0 {
		return "-"
	}
	return strings.Join(parts, ", ")
}

func formatUptime(state string, created int64) string {
	if state != "running" {
		return "-"
	}
	d := time.Since(time.Unix(created, 0))
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		h := int(d.Hours())
		m := int(d.Minutes()) % 60
		if m == 0 {
			return fmt.Sprintf("%dh", h)
		}
		return fmt.Sprintf("%dh%dm", h, m)
	default:
		days := int(d.Hours()) / 24
		h := int(d.Hours()) % 24
		if h == 0 {
			return fmt.Sprintf("%dd", days)
		}
		return fmt.Sprintf("%dd%dh", days, h)
	}
}
