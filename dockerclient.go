package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/api/types/volume"
	"github.com/docker/docker/client"
	"github.com/docker/docker/pkg/stdcopy"
	"golang.org/x/term"
)

var (
	dockerOnce   sync.Once
	dockerClient *client.Client
	dockerErr    error
)

// getDockerClient returns a lazy-initialized Docker client singleton.
func getDockerClient() (*client.Client, error) {
	dockerOnce.Do(func() {
		dockerClient, dockerErr = client.NewClientWithOpts(
			client.FromEnv,
			client.WithAPIVersionNegotiation(),
		)
	})
	return dockerClient, dockerErr
}

// containerExec runs a command in a container (non-TTY) with stdout/stderr piped to os.Stdout/os.Stderr.
func containerExec(ctx context.Context, containerID, user string, cmd []string) error {
	cli, err := getDockerClient()
	if err != nil {
		return fmt.Errorf("docker client: %w", err)
	}

	execCfg := container.ExecOptions{
		User:         user,
		AttachStdout: true,
		AttachStderr: true,
		Cmd:          cmd,
	}
	resp, err := cli.ContainerExecCreate(ctx, containerID, execCfg)
	if err != nil {
		return fmt.Errorf("exec create: %w", err)
	}

	attach, err := cli.ContainerExecAttach(ctx, resp.ID, container.ExecAttachOptions{})
	if err != nil {
		return fmt.Errorf("exec attach: %w", err)
	}
	defer attach.Close()

	// Demultiplex stdout/stderr from the multiplexed stream
	if _, err := stdcopy.StdCopy(os.Stdout, os.Stderr, attach.Reader); err != nil {
		return fmt.Errorf("exec copy: %w", err)
	}

	inspect, err := cli.ContainerExecInspect(ctx, resp.ID)
	if err != nil {
		return fmt.Errorf("exec inspect: %w", err)
	}
	if inspect.ExitCode != 0 {
		return fmt.Errorf("exec exited with code %d", inspect.ExitCode)
	}
	return nil
}

// containerExecOutput runs a command in a container (non-TTY) and returns stdout as a string.
func containerExecOutput(ctx context.Context, containerID, user string, cmd []string) (string, error) {
	cli, err := getDockerClient()
	if err != nil {
		return "", fmt.Errorf("docker client: %w", err)
	}

	execCfg := container.ExecOptions{
		User:         user,
		AttachStdout: true,
		AttachStderr: true,
		Cmd:          cmd,
	}
	resp, err := cli.ContainerExecCreate(ctx, containerID, execCfg)
	if err != nil {
		return "", fmt.Errorf("exec create: %w", err)
	}

	attach, err := cli.ContainerExecAttach(ctx, resp.ID, container.ExecAttachOptions{})
	if err != nil {
		return "", fmt.Errorf("exec attach: %w", err)
	}
	defer attach.Close()

	var stdout strings.Builder
	if _, err := stdcopy.StdCopy(&stdout, io.Discard, attach.Reader); err != nil {
		return "", fmt.Errorf("exec copy: %w", err)
	}

	inspect, err := cli.ContainerExecInspect(ctx, resp.ID)
	if err != nil {
		return "", fmt.Errorf("exec inspect: %w", err)
	}
	if inspect.ExitCode != 0 {
		return "", fmt.Errorf("exec exited with code %d", inspect.ExitCode)
	}
	return strings.TrimSpace(stdout.String()), nil
}

// containerExecInteractive runs a command in a container with a TTY attached to the current terminal.
// It returns the exit code of the command.
func containerExecInteractive(ctx context.Context, containerID, user, workdir string, cmd []string) (int, error) {
	cli, err := getDockerClient()
	if err != nil {
		return 1, fmt.Errorf("docker client: %w", err)
	}

	execCfg := container.ExecOptions{
		User:         user,
		Tty:          true,
		AttachStdin:  true,
		AttachStdout: true,
		AttachStderr: true,
		WorkingDir:   workdir,
		Cmd:          cmd,
	}
	resp, err := cli.ContainerExecCreate(ctx, containerID, execCfg)
	if err != nil {
		return 1, fmt.Errorf("exec create: %w", err)
	}

	attach, err := cli.ContainerExecAttach(ctx, resp.ID, container.ExecAttachOptions{Tty: true})
	if err != nil {
		return 1, fmt.Errorf("exec attach: %w", err)
	}
	defer attach.Close()

	// Put terminal in raw mode
	fd := int(os.Stdin.Fd())
	oldState, err := term.MakeRaw(fd)
	if err != nil {
		return 1, fmt.Errorf("make raw: %w", err)
	}
	defer func() { _ = term.Restore(fd, oldState) }()

	// Resize exec to current terminal size
	resizeExec := func() {
		w, h, err := term.GetSize(fd)
		if err != nil {
			return
		}
		_ = cli.ContainerExecResize(ctx, resp.ID, container.ResizeOptions{
			Width:  uint(w),
			Height: uint(h),
		})
	}
	resizeExec()

	// Handle SIGWINCH for terminal resize
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGWINCH)
	defer signal.Stop(sigCh)
	go func() {
		for range sigCh {
			resizeExec()
		}
	}()

	// Proxy I/O: stdin → conn, conn → stdout
	// For TTY mode, the stream is not multiplexed, so we use io.Copy directly.
	outputDone := make(chan error, 1)
	go func() {
		_, err := io.Copy(os.Stdout, attach.Reader)
		outputDone <- err
	}()

	inputDone := make(chan error, 1)
	go func() {
		_, err := io.Copy(attach.Conn, os.Stdin)
		_ = attach.CloseWrite()
		inputDone <- err
	}()

	// Wait for output to finish (indicates the exec process exited)
	select {
	case <-outputDone:
	case <-ctx.Done():
		return 1, ctx.Err()
	}

	// Get exit code
	inspect, err := cli.ContainerExecInspect(ctx, resp.ID)
	if err != nil {
		return 1, fmt.Errorf("exec inspect: %w", err)
	}
	return inspect.ExitCode, nil
}

// composeDown stops and removes containers, networks, and optionally volumes
// for a Docker Compose project identified by its project label.
func composeDown(ctx context.Context, project string, removeVolumes bool) error {
	cli, err := getDockerClient()
	if err != nil {
		return fmt.Errorf("docker client: %w", err)
	}

	projectFilter := filters.NewArgs(filters.Arg("label", "com.docker.compose.project="+project))

	// 1. Stop and remove containers
	containers, err := cli.ContainerList(ctx, container.ListOptions{
		All:     true,
		Filters: projectFilter,
	})
	if err != nil {
		return fmt.Errorf("list containers: %w", err)
	}
	for _, c := range containers {
		_ = cli.ContainerStop(ctx, c.ID, container.StopOptions{})
		if err := cli.ContainerRemove(ctx, c.ID, container.RemoveOptions{Force: true}); err != nil {
			return fmt.Errorf("remove container %s: %w", c.ID[:12], err)
		}
	}

	// 2. Remove networks
	networks, err := cli.NetworkList(ctx, network.ListOptions{
		Filters: projectFilter,
	})
	if err != nil {
		return fmt.Errorf("list networks: %w", err)
	}
	for _, n := range networks {
		if err := cli.NetworkRemove(ctx, n.ID); err != nil {
			return fmt.Errorf("remove network %s: %w", n.Name, err)
		}
	}

	// 3. Remove volumes (if requested)
	if removeVolumes {
		vols, err := cli.VolumeList(ctx, volume.ListOptions{
			Filters: projectFilter,
		})
		if err != nil {
			return fmt.Errorf("list volumes: %w", err)
		}
		for _, v := range vols.Volumes {
			if err := cli.VolumeRemove(ctx, v.Name, true); err != nil {
				return fmt.Errorf("remove volume %s: %w", v.Name, err)
			}
		}
	}

	return nil
}
