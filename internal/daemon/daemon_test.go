package daemon

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"os"
	"path/filepath"
	"testing"
)

func TestDaemonHostCommand(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sockDir := t.TempDir()
	sockPath := filepath.Join(sockDir, "devc.sock")

	// Create a daemon directly (skip Start which needs a container ID)
	ln, err := net.Listen("unix", sockPath)
	if err != nil {
		t.Fatal(err)
	}

	d := &Daemon{
		listener:    ln,
		sockPath:    sockPath,
		containerID: "abcdef123456",
	}
	go d.serve(ctx)
	defer d.Close()

	// Send a host command
	conn, err := net.Dial("unix", sockPath)
	if err != nil {
		t.Fatal(err)
	}

	req := Request{
		Type:    "host",
		Command: []string{"echo", "hello from host"},
	}
	data, _ := json.Marshal(req)
	_, err = conn.Write(data)
	if err != nil {
		t.Fatal(err)
	}
	if uc, ok := conn.(*net.UnixConn); ok {
		_ = uc.CloseWrite()
	}

	respData, err := io.ReadAll(conn)
	_ = conn.Close()
	if err != nil {
		t.Fatal(err)
	}

	var resp Response
	if err := json.Unmarshal(respData, &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if !resp.OK {
		t.Fatalf("expected OK, got: %s", resp.Message)
	}
	if resp.Output != "hello from host\n" {
		t.Fatalf("output = %q, want %q", resp.Output, "hello from host\n")
	}
}

func TestDaemonRebuild(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sockDir := t.TempDir()
	sockPath := filepath.Join(sockDir, "devc.sock")

	ln, err := net.Listen("unix", sockPath)
	if err != nil {
		t.Fatal(err)
	}

	d := &Daemon{
		listener:    ln,
		sockPath:    sockPath,
		containerID: "abcdef123456",
	}
	go d.serve(ctx)
	defer d.Close()

	if d.RebuildRequested() {
		t.Fatal("rebuild should not be requested initially")
	}

	// Send rebuild request
	conn, err := net.Dial("unix", sockPath)
	if err != nil {
		t.Fatal(err)
	}

	req := Request{Type: "rebuild"}
	data, _ := json.Marshal(req)
	_, _ = conn.Write(data)
	if uc, ok := conn.(*net.UnixConn); ok {
		_ = uc.CloseWrite()
	}

	respData, _ := io.ReadAll(conn)
	_ = conn.Close()

	var resp Response
	if err := json.Unmarshal(respData, &resp); err != nil {
		t.Fatal(err)
	}
	if !resp.OK {
		t.Fatalf("expected OK, got: %s", resp.Message)
	}

	if !d.RebuildRequested() {
		t.Fatal("rebuild should be requested after rebuild command")
	}
}

func TestDaemonSockDir(t *testing.T) {
	dir := SockDir("myproject")
	if dir != "/tmp/devc-daemon-myproject" {
		t.Fatalf("SockDir = %q, want %q", dir, "/tmp/devc-daemon-myproject")
	}
}

func TestDaemonClose(t *testing.T) {
	sockDir := t.TempDir()
	sockPath := filepath.Join(sockDir, "devc.sock")

	ln, err := net.Listen("unix", sockPath)
	if err != nil {
		t.Fatal(err)
	}

	d := &Daemon{
		listener: ln,
		sockPath: sockPath,
	}

	d.Close()

	// Socket file should be removed
	if _, err := os.Stat(sockPath); !os.IsNotExist(err) {
		t.Fatal("socket file should be removed after Close")
	}
}
