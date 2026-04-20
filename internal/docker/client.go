package docker

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"

	"github.com/closer/devc/internal/ui"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/client"
	"github.com/docker/docker/pkg/stdcopy"
	"golang.org/x/term"
)

var (
	dockerOnce   sync.Once
	dockerClient *client.Client
	dockerErr    error
)

// GetClient returns a lazy-initialized Docker client singleton.
func GetClient() (*client.Client, error) {
	dockerOnce.Do(func() {
		dockerClient, dockerErr = client.NewClientWithOpts(
			client.FromEnv,
			client.WithAPIVersionNegotiation(),
		)
	})
	return dockerClient, dockerErr
}

// Exec runs a command in a container (non-TTY) with stdout/stderr piped to os.Stdout/os.Stderr.
func Exec(ctx context.Context, containerID, user string, cmd []string) error {
	cli, err := GetClient()
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

// ExecOutput runs a command in a container (non-TTY) and returns stdout as a string.
func ExecOutput(ctx context.Context, containerID, user string, cmd []string) (string, error) {
	cli, err := GetClient()
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

// ExecTail runs a command in a container, displaying the last 3 lines
// of output in dim style below the current cursor position.
// On error, the last 20 lines are included in the error message.
func ExecTail(ctx context.Context, containerID, user string, cmd []string) error {
	cli, err := GetClient()
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

	tail := ui.NewTailRenderer(3)
	var combined strings.Builder
	multi := io.MultiWriter(tail, &combined)

	if _, err := stdcopy.StdCopy(multi, multi, attach.Reader); err != nil {
		tail.Clear()
		return fmt.Errorf("exec copy: %w", err)
	}

	tail.Clear()

	inspect, err := cli.ContainerExecInspect(ctx, resp.ID)
	if err != nil {
		return fmt.Errorf("exec inspect: %w", err)
	}
	if inspect.ExitCode != 0 {
		lines := strings.Split(strings.TrimSpace(combined.String()), "\n")
		if len(lines) > 20 {
			lines = lines[len(lines)-20:]
		}
		return fmt.Errorf("exec exited with code %d\n%s", inspect.ExitCode, strings.Join(lines, "\n"))
	}
	return nil
}

// ExecInteractive runs a command in a container with a TTY attached to the current terminal.
// It returns the exit code of the command.
func ExecInteractive(ctx context.Context, containerID, user, workdir string, cmd []string) (int, error) {
	cli, err := GetClient()
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
