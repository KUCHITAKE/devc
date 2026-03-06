package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
)

const devcSockPath = "/opt/devc/devc.sock"

// daemonRequest is the JSON request sent from the in-container devc to the host daemon.
type daemonRequest struct {
	Type    string   `json:"type"`              // "port", "host", "rebuild"
	Port    string   `json:"port,omitempty"`     // for "port": e.g. "8080" or "8080:3000"
	Command []string `json:"command,omitempty"`  // for "host": command to execute
}

// daemonResponse is the JSON response sent back to the in-container devc.
type daemonResponse struct {
	OK      bool   `json:"ok"`
	Message string `json:"message,omitempty"`
	Output  string `json:"output,omitempty"`
}

// daemon manages the Unix socket listener and active port forwards.
type daemon struct {
	listener    net.Listener
	sockPath    string
	containerID string
	mu          sync.Mutex
	forwards    []portForward
	rebuildReq  bool
}

type portForward struct {
	listener      net.Listener
	hostPort      string
	containerPort string
}

// daemonSockDir returns the host directory for the daemon socket.
// This directory is mounted into the container at /opt/devc/.
func daemonSockDir(wsName string) string {
	return fmt.Sprintf("/tmp/devc-daemon-%s", wsName)
}

// startDaemon creates a Unix socket in the daemon directory and starts listening.
// The daemon directory must already exist and be mounted into the container.
func startDaemon(ctx context.Context, containerID string, sockDir string) (*daemon, error) {
	sockPath := filepath.Join(sockDir, "devc.sock")

	// Clean up stale socket
	_ = os.Remove(sockPath)

	listener, err := net.Listen("unix", sockPath)
	if err != nil {
		return nil, fmt.Errorf("listen unix socket: %w", err)
	}

	// Make socket accessible to all users in the container
	_ = os.Chmod(sockPath, 0o777)

	d := &daemon{
		listener:    listener,
		sockPath:    sockPath,
		containerID: containerID,
	}

	go d.serve(ctx)

	return d, nil
}

func (d *daemon) serve(ctx context.Context) {
	for {
		conn, err := d.listener.Accept()
		if err != nil {
			select {
			case <-ctx.Done():
				return
			default:
				continue
			}
		}
		go d.handleConn(ctx, conn)
	}
}

func (d *daemon) handleConn(ctx context.Context, conn net.Conn) {
	defer func() { _ = conn.Close() }()

	data, err := io.ReadAll(conn)
	if err != nil {
		return
	}

	var req daemonRequest
	if err := json.Unmarshal(data, &req); err != nil {
		writeResponse(conn, daemonResponse{OK: false, Message: "invalid request"})
		return
	}

	var resp daemonResponse
	switch req.Type {
	case "port":
		resp = d.handlePort(ctx, req)
	case "host":
		resp = d.handleHost(req)
	case "rebuild":
		resp = d.handleRebuild()
	default:
		resp = daemonResponse{OK: false, Message: fmt.Sprintf("unknown request type: %s", req.Type)}
	}

	writeResponse(conn, resp)
}

func (d *daemon) handlePort(ctx context.Context, req daemonRequest) daemonResponse {
	parts := strings.SplitN(req.Port, ":", 2)
	var hostPort, containerPort string
	if len(parts) == 2 {
		hostPort = parts[0]
		containerPort = parts[1]
	} else {
		hostPort = req.Port
		containerPort = req.Port
	}

	// Find available host port
	resolved := resolvePort(hostPort + ":" + containerPort)
	resolvedParts := strings.SplitN(resolved, ":", 2)
	hostPort = resolvedParts[0]

	// Get container IP
	containerIP, err := getContainerIP(ctx, d.containerID)
	if err != nil {
		return daemonResponse{OK: false, Message: fmt.Sprintf("get container IP: %v", err)}
	}

	// Start TCP proxy
	ln, err := net.Listen("tcp", ":"+hostPort)
	if err != nil {
		return daemonResponse{OK: false, Message: fmt.Sprintf("listen on port %s: %v", hostPort, err)}
	}

	fwd := portForward{
		listener:      ln,
		hostPort:      hostPort,
		containerPort: containerPort,
	}

	d.mu.Lock()
	d.forwards = append(d.forwards, fwd)
	d.mu.Unlock()

	go proxyTCP(ctx, ln, containerIP, containerPort)

	msg := fmt.Sprintf("forwarding localhost:%s → container:%s", hostPort, containerPort)
	return daemonResponse{OK: true, Message: msg}
}

func (d *daemon) handleHost(req daemonRequest) daemonResponse {
	if len(req.Command) == 0 {
		return daemonResponse{OK: false, Message: "no command specified"}
	}

	cmd := exec.Command(req.Command[0], req.Command[1:]...) //nolint:gosec // intentional host command execution via authenticated socket
	out, err := cmd.CombinedOutput()
	if err != nil {
		return daemonResponse{OK: false, Message: err.Error(), Output: string(out)}
	}
	return daemonResponse{OK: true, Output: string(out)}
}

func (d *daemon) handleRebuild() daemonResponse {
	d.mu.Lock()
	d.rebuildReq = true
	d.mu.Unlock()
	return daemonResponse{OK: true, Message: "rebuild requested — exit the container to trigger rebuild"}
}

// RebuildRequested returns true if a rebuild was requested from inside the container.
func (d *daemon) RebuildRequested() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.rebuildReq
}

// Close shuts down the daemon and cleans up resources.
func (d *daemon) Close() {
	_ = d.listener.Close()
	_ = os.Remove(d.sockPath)

	d.mu.Lock()
	for _, fwd := range d.forwards {
		_ = fwd.listener.Close()
	}
	d.mu.Unlock()
}

// SockPath returns the host-side path to the Unix socket.
func (d *daemon) SockPath() string {
	return d.sockPath
}

func writeResponse(conn net.Conn, resp daemonResponse) {
	data, _ := json.Marshal(resp)
	_, _ = conn.Write(data)
}

// getContainerIP returns the IP address of a container on the default bridge network.
func getContainerIP(ctx context.Context, containerID string) (string, error) {
	cli, err := getDockerClient()
	if err != nil {
		return "", err
	}
	info, err := cli.ContainerInspect(ctx, containerID)
	if err != nil {
		return "", err
	}
	if info.NetworkSettings == nil {
		return "", fmt.Errorf("no network settings")
	}
	for _, nw := range info.NetworkSettings.Networks {
		if nw.IPAddress != "" {
			return nw.IPAddress, nil
		}
	}
	return "", fmt.Errorf("no IP address found for container %s", containerID[:12])
}

// proxyTCP accepts connections on ln and proxies them to target host:port.
func proxyTCP(ctx context.Context, ln net.Listener, targetHost, targetPort string) {
	target := net.JoinHostPort(targetHost, targetPort)
	for {
		conn, err := ln.Accept()
		if err != nil {
			select {
			case <-ctx.Done():
				return
			default:
				return
			}
		}
		go func(c net.Conn) {
			defer func() { _ = c.Close() }()
			remote, err := net.Dial("tcp", target)
			if err != nil {
				return
			}
			defer func() { _ = remote.Close() }()

			done := make(chan struct{}, 2)
			go func() { _, _ = io.Copy(remote, c); done <- struct{}{} }()
			go func() { _, _ = io.Copy(c, remote); done <- struct{}{} }()
			<-done
		}(conn)
	}
}
