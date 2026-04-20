package daemon

import (
	"context"
	"strconv"
	"strings"
	"time"

	"github.com/closer/devc/internal/docker"
	"github.com/closer/devc/internal/ui"
)

const (
	// Ports below this are system services (sshd, etc.) — skip auto-forward.
	minAutoForwardPort = 1024
	// Ports at or above this are in the ephemeral range — skip auto-forward.
	maxAutoForwardPort = 32768
)

// StartAutoPortDetection polls for new LISTEN ports inside the container and
// auto-forwards them to the host. It runs until ctx is cancelled.
func (d *Daemon) StartAutoPortDetection(ctx context.Context) {
	go func() {
		// Wait for lifecycle hooks and services to settle before scanning.
		select {
		case <-time.After(3 * time.Second):
		case <-ctx.Done():
			return
		}

		ticker := time.NewTicker(2 * time.Second)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				d.detectAndForward(ctx)
			}
		}
	}()
}

func (d *Daemon) detectAndForward(ctx context.Context) {
	ports, err := d.scanContainerPorts(ctx)
	if err != nil {
		return
	}

	d.mu.Lock()
	defer d.mu.Unlock()

	for port := range ports {
		if d.isPortForwardedLocked(port) {
			continue
		}
		d.autoForwards[port] = true
		// Release lock during forwarding (network I/O).
		d.mu.Unlock()
		resp := d.HandlePort(ctx, Request{Type: "port", Port: port})
		if resp.OK {
			ui.PrintDone("Auto-forwarded port", resp.Message)
		}
		d.mu.Lock()
	}
}

// scanContainerPorts reads /proc/net/tcp and /proc/net/tcp6 inside the
// container and returns the set of LISTEN ports in the auto-forward range.
func (d *Daemon) scanContainerPorts(ctx context.Context) (map[string]bool, error) {
	out, err := docker.ExecOutput(ctx, d.containerID, "root",
		[]string{"cat", "/proc/net/tcp", "/proc/net/tcp6"})
	if err != nil {
		return nil, err
	}
	ports := ParseProcNetTCP(out)
	result := make(map[string]bool, len(ports))
	for _, p := range ports {
		if p >= minAutoForwardPort && p < maxAutoForwardPort {
			result[strconv.Itoa(p)] = true
		}
	}
	return result, nil
}

// ParseProcNetTCP parses /proc/net/tcp output and returns ports in LISTEN state.
func ParseProcNetTCP(content string) []int {
	seen := make(map[int]bool)
	var ports []int

	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		fields := strings.Fields(line)
		if len(fields) < 4 {
			continue
		}

		// fields[3] is the TCP state — "0A" means LISTEN.
		if fields[3] != "0A" {
			continue
		}

		// fields[1] is local_address in hex_ip:hex_port format.
		addrParts := strings.SplitN(fields[1], ":", 2)
		if len(addrParts) != 2 {
			continue
		}
		port, err := strconv.ParseInt(addrParts[1], 16, 32)
		if err != nil || port <= 0 {
			continue
		}

		p := int(port)
		if !seen[p] {
			seen[p] = true
			ports = append(ports, p)
		}
	}
	return ports
}

// isPortForwardedLocked returns true if the port already has a forward.
// Must be called with d.mu held.
func (d *Daemon) isPortForwardedLocked(containerPort string) bool {
	for _, fwd := range d.forwards {
		if fwd.containerPort == containerPort {
			return true
		}
	}
	return d.autoForwards[containerPort]
}

// StaticPortSet returns a set of container ports from resolved port strings
// ("host:container" format) for checking duplicates.
func StaticPortSet(resolvedPorts []string) map[string]bool {
	m := make(map[string]bool, len(resolvedPorts))
	for _, p := range resolvedPorts {
		parts := strings.SplitN(p, ":", 2)
		if len(parts) == 2 {
			m[parts[1]] = true
		}
	}
	return m
}

// SetStaticPorts records ports that were forwarded at container creation time
// (via Docker port bindings) so auto-detection skips them.
func (d *Daemon) SetStaticPorts(resolvedPorts []string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	for port := range StaticPortSet(resolvedPorts) {
		d.autoForwards[port] = true // mark as already handled
	}
}
