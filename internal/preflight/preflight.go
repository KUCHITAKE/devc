// Package preflight detects conflicts between a devcontainer that is about to
// start and other devc workspaces already running on the host.
//
// The only resource that genuinely collides across workspaces is the set of
// published host ports: container names, compose project names, image tags and
// socket directories are all keyed on the workspace ID (which embeds a hash of
// the absolute path), so the same repository checked out into two directories
// never clashes on those. Host ports, however, are a shared global namespace.
package preflight

import (
	"strconv"
	"strings"
)

// RunningWS describes a devc workspace that is currently running.
type RunningWS struct {
	Name      string // directory basename (typically the repo name)
	Path      string // absolute local folder
	HostPorts []int  // published host ports
}

// PortConflict is a desired host port that a running workspace already holds.
type PortConflict struct {
	HostPort int
	Explicit bool // the desired spec pinned this host port (host:container)
	Holder   RunningWS
}

// Report is the outcome of a pre-flight analysis.
type Report struct {
	PortConflicts []PortConflict
	SameName      []RunningWS // running workspaces sharing the current basename
}

// HasBlocking reports whether any conflict pins an explicit host port. Bare
// ports auto-remap to a free port and are therefore only advisory.
func (r Report) HasBlocking() bool {
	for _, c := range r.PortConflicts {
		if c.Explicit {
			return true
		}
	}
	return false
}

// Analyze computes the conflicts between the workspace about to start
// (curName/curPath) publishing desiredPorts and the workspaces already running.
//
// desiredPorts are raw specs as collected from devcontainer.json and -p flags:
// a bare number ("3000"), host:container ("3000:3000", "8080:80") or
// ip:host:container ("127.0.0.1:3000:3000"). The running workspace occupying
// the same absolute path as curPath is treated as the current workspace itself
// (a restart) and never reported as a conflict.
func Analyze(curName, curPath string, desiredPorts []string, running []RunningWS) Report {
	var others []RunningWS
	for _, ws := range running {
		if ws.Path == curPath {
			continue
		}
		others = append(others, ws)
	}

	holderOf := make(map[int]RunningWS)
	for _, ws := range others {
		for _, p := range ws.HostPorts {
			if _, seen := holderOf[p]; !seen {
				holderOf[p] = ws
			}
		}
	}

	var report Report
	reported := make(map[int]bool)
	for _, spec := range desiredPorts {
		host, explicit, ok := hostPort(spec)
		if !ok || reported[host] {
			continue
		}
		if holder, taken := holderOf[host]; taken {
			reported[host] = true
			report.PortConflicts = append(report.PortConflicts, PortConflict{
				HostPort: host,
				Explicit: explicit,
				Holder:   holder,
			})
		}
	}

	for _, ws := range others {
		if ws.Name == curName {
			report.SameName = append(report.SameName, ws)
		}
	}

	return report
}

// hostPort extracts the host port from a port spec and whether the spec pinned
// it explicitly (i.e. was in host:container form rather than a bare number).
func hostPort(spec string) (port int, explicit bool, ok bool) {
	// Strip any protocol suffix (e.g. "3000/tcp").
	if i := strings.IndexByte(spec, '/'); i >= 0 {
		spec = spec[:i]
	}
	parts := strings.Split(spec, ":")
	switch len(parts) {
	case 1:
		// Bare container port; host port defaults to the same number but is
		// free to be remapped.
		n, err := strconv.Atoi(parts[0])
		if err != nil {
			return 0, false, false
		}
		return n, false, true
	case 2:
		// host:container
		n, err := strconv.Atoi(parts[0])
		if err != nil {
			return 0, false, false
		}
		return n, true, true
	case 3:
		// ip:host:container
		n, err := strconv.Atoi(parts[1])
		if err != nil {
			return 0, false, false
		}
		return n, true, true
	default:
		return 0, false, false
	}
}
