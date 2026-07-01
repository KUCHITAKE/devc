package main

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/closer/devc/internal/docker"
	dockercontainer "github.com/docker/docker/api/types/container"
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

	// devc manages two container shapes: image-based (one container, tagged
	// with devcontainer.local_folder) and compose-based (several service
	// containers under a "*_devcontainer" project). Gather both.
	imageContainers, err := docker.ListDevcContainers(ctx, true)
	if err != nil {
		return fmt.Errorf("list containers: %w", err)
	}
	composeContainers, err := docker.ListComposeContainers(ctx, true)
	if err != nil {
		return fmt.Errorf("list compose containers: %w", err)
	}

	rows := buildWorkspaceRows(append(imageContainers, composeContainers...))
	if len(rows) == 0 {
		fmt.Println("No devc containers found.")
		return nil
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
	if _, err := fmt.Fprintln(w, "WORKSPACE\tSTATUS\tPORTS\tUPTIME\tPATH"); err != nil {
		return err
	}

	for _, r := range rows {
		if _, err := fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n",
			r.name, r.status, formatPorts(r.ports), formatUptime(r.status, r.created), r.path); err != nil {
			return err
		}
	}

	return w.Flush()
}

// workspaceRow is one line of `devc ls`: a whole workspace, which for compose
// aggregates all of its service containers.
type workspaceRow struct {
	name    string
	status  string
	ports   []dockercontainer.Port
	created int64
	path    string
}

// buildWorkspaceRows groups containers into one row per devc workspace and
// sorts the rows most-recent first. Compose service containers of the same
// project collapse into a single row whose ports are the union across services;
// its status is "running" when any service is running, otherwise the state of
// the most recently created container. Non-devc containers are ignored.
func buildWorkspaceRows(containers []dockercontainer.Summary) []workspaceRow {
	type agg struct {
		name       string
		path       string
		ports      []dockercontainer.Port
		created    int64  // most recent container in the group
		newestSt   string // state of that most recent container
		anyRunning bool
	}
	groups := make(map[string]*agg)
	var order []string

	for _, c := range containers {
		name, path, ok := devcContainerIdentity(c.Labels)
		if !ok {
			continue
		}
		key := devcWorkspaceKey(name, path, c.Labels)
		g := groups[key]
		if g == nil {
			g = &agg{name: name, path: path}
			groups[key] = g
			order = append(order, key)
		}
		g.ports = append(g.ports, c.Ports...)
		if c.State == "running" {
			g.anyRunning = true
		}
		if c.Created >= g.created {
			g.created = c.Created
			g.newestSt = c.State
		}
	}

	rows := make([]workspaceRow, 0, len(order))
	for _, key := range order {
		g := groups[key]
		status := g.newestSt
		if g.anyRunning {
			status = "running"
		}
		rows = append(rows, workspaceRow{
			name:    g.name,
			status:  status,
			ports:   g.ports,
			created: g.created,
			path:    g.path,
		})
	}

	sort.Slice(rows, func(i, j int) bool {
		return rows[i].created > rows[j].created
	})
	return rows
}

func formatPorts(ports []dockercontainer.Port) string {
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
