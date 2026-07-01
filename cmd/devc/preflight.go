package main

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/closer/devc/internal/compose"
	"github.com/closer/devc/internal/config"
	"github.com/closer/devc/internal/docker"
	"github.com/closer/devc/internal/preflight"
	"github.com/closer/devc/internal/ui"
	dockercontainer "github.com/docker/docker/api/types/container"
)

// preflightCheck warns about conflicts between the workspace about to start and
// other devc workspaces already running on the host. Explicit host port
// conflicts abort the launch unless opts.force is set; bare ports auto-remap
// and are only advisory. It never fails the launch on its own errors (e.g. the
// Docker daemon being unreachable) — those surface later in the real flow.
func preflightCheck(ctx context.Context, ws config.Workspace, cfg *config.DevcontainerConfig, opts upOptions) error {
	desired := config.CollectPorts(cfg.Raw, opts.ports)

	// Compose services publish their own ports (declared in the compose files,
	// not devcontainer.json). Resolve them so the check sees ports like an
	// envoy service's 8765 that would otherwise slip through.
	selfProject := ""
	if config.ComposeFiles(ws, cfg.Raw) != nil {
		if cc, err := compose.ParseConfig(ws, cfg.Raw); err == nil {
			selfProject = compose.Project(ws)
			if cports, err := compose.PublishedHostPorts(ctx, cc.Files, selfProject); err == nil {
				desired = append(desired, cports...)
			}
		}
	}

	containers, err := docker.ListRunningContainers(ctx)
	if err != nil {
		return nil
	}

	running := toRunningWorkspaces(containers, ws.Dir, selfProject)
	report := preflight.Analyze(ws.Name, ws.Dir, desired, running)

	for _, other := range report.SameName {
		ui.PrintWarn("Same repo already running", fmt.Sprintf("workspace %q at %s", other.Name, other.Path))
	}

	for _, c := range report.PortConflicts {
		holder := c.Holder.Name
		if c.Holder.Path != "" {
			holder = fmt.Sprintf("%s (%s)", c.Holder.Name, c.Holder.Path)
		}
		detail := fmt.Sprintf("port %d is used by %s", c.HostPort, holder)
		if c.Explicit {
			ui.PrintError("Port conflict", detail)
		} else {
			ui.PrintWarn("Port conflict", detail+" — will remap to a free port")
		}
	}

	if report.HasBlocking() && !opts.force {
		return fmt.Errorf("host port conflict with a running workspace; re-run with --force to launch anyway")
	}

	return nil
}

// toRunningWorkspaces maps running Docker containers to devc workspaces for the
// port pre-flight. It considers only devc-managed containers — those with a
// devcontainer.local_folder label (image-based) or a compose project ending in
// "_devcontainer" — and excludes the current workspace (by path or compose
// project) so a restart never reports a conflict with itself. Containers of the
// same workspace are aggregated so their published ports collapse into one
// entry.
func toRunningWorkspaces(containers []dockercontainer.Summary, selfPath, selfProject string) []preflight.RunningWS {
	type group struct {
		name  string
		path  string
		ports map[int]bool
	}
	groups := make(map[string]*group)

	for _, c := range containers {
		name, path, ok := devcContainerIdentity(c.Labels)
		if !ok {
			continue
		}
		// Skip the current workspace's own containers (by path or, when the
		// path could not be recovered, by compose project).
		if path != "" && path == selfPath {
			continue
		}
		if project := c.Labels["com.docker.compose.project"]; project != "" && project == selfProject {
			continue
		}

		key := devcWorkspaceKey(name, path, c.Labels)
		g := groups[key]
		if g == nil {
			g = &group{name: name, path: path, ports: make(map[int]bool)}
			groups[key] = g
		}
		for _, p := range c.Ports {
			if p.PublicPort != 0 {
				g.ports[int(p.PublicPort)] = true
			}
		}
	}

	keys := make([]string, 0, len(groups))
	for k := range groups {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	out := make([]preflight.RunningWS, 0, len(groups))
	for _, k := range keys {
		g := groups[k]
		ports := make([]int, 0, len(g.ports))
		for p := range g.ports {
			ports = append(ports, p)
		}
		sort.Ints(ports)
		out = append(out, preflight.RunningWS{Name: g.name, Path: g.path, HostPorts: ports})
	}
	return out
}

// devcContainerIdentity classifies a container as a devc-managed workspace and
// returns its display name and workspace path. It recognises two kinds:
//
//   - image-based containers, tagged with the devcontainer.local_folder label
//     holding the absolute workspace path;
//   - compose service containers, whose project label ends in "_devcontainer".
//     These carry no local_folder label, but devc always runs compose from the
//     workspace's .devcontainer directory, so the compose working_dir label
//     recovers the path (and hence the basename).
//
// ok is false for containers devc does not manage.
func devcContainerIdentity(labels map[string]string) (name, path string, ok bool) {
	if folder := labels["devcontainer.local_folder"]; folder != "" {
		return filepath.Base(folder), folder, true
	}
	project := labels["com.docker.compose.project"]
	if strings.HasSuffix(project, "_devcontainer") {
		if wd := labels["com.docker.compose.project.working_dir"]; filepath.Base(wd) == ".devcontainer" {
			p := filepath.Dir(wd)
			return filepath.Base(p), p, true
		}
		return strings.TrimSuffix(project, "_devcontainer"), "", true
	}
	return "", "", false
}

// devcWorkspaceKey returns a stable grouping key that collapses every container
// of one workspace (e.g. all services of a compose project) into a single row.
func devcWorkspaceKey(name, path string, labels map[string]string) string {
	if path != "" {
		return "path:" + path
	}
	if project := labels["com.docker.compose.project"]; project != "" {
		return "project:" + project
	}
	return "name:" + name
}
