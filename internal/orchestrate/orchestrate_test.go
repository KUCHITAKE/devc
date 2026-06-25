package orchestrate

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/closer/devc/internal/config"
	"github.com/closer/devc/internal/container"
)

type callLog struct {
	restarted        bool
	ensuredBinary    bool
	resolvedUser     string
	setupUser        string
	enteredUser      string
	enteredContainer string
	lifecycleCount   int
	preHooksCalled   bool
}

func newMockDeps(log *callLog, running bool) *Deps {
	return &Deps{
		IsRunning: func(containerID string) bool {
			return running
		},
		Restart: func(ctx context.Context) (string, error) {
			log.restarted = true
			return "newcontainer123456", nil
		},
		EnsureBinary: func(sockDir string) error {
			log.ensuredBinary = true
			return nil
		},
		ResolveUser: func(ctx context.Context, containerID, remoteUser string) string {
			log.resolvedUser = remoteUser
			return remoteUser
		},
		Setup: func(containerID, remoteUser string, dotfiles []string) error {
			log.setupUser = remoteUser
			return nil
		},
		RunHooks: func(ctx context.Context, containerID, user string, hooks ...[]container.LifecycleCommand) error {
			log.lifecycleCount += len(hooks)
			return nil
		},
		Enter: func(ctx context.Context, containerID, remoteUser, workspaceFolder string) error {
			log.enteredUser = remoteUser
			log.enteredContainer = containerID
			return nil
		},
	}
}

func TestHandleExistingContainer_RestartError(t *testing.T) {
	log := &callLog{}
	deps := newMockDeps(log, false)
	deps.Restart = func(ctx context.Context) (string, error) {
		return "", fmt.Errorf("compose start failed")
	}

	cfg := &config.DevcontainerConfig{
		RemoteUser:            "vscode",
		RemoteWorkspaceFolder: "/workspaces/test",
	}

	err := HandleExistingContainer(context.Background(), "abcdef123456abcdef", "ws-1", cfg, nil, deps)
	if err == nil {
		t.Fatal("expected error from Restart")
	}
	if !strings.Contains(err.Error(), "compose start failed") {
		t.Errorf("error = %q, want to contain 'compose start failed'", err.Error())
	}
	if log.ensuredBinary {
		t.Error("should not ensure binary after restart failure")
	}
	if log.enteredContainer != "" {
		t.Error("should not enter container after restart failure")
	}
}

func TestHandleExistingContainer_Running(t *testing.T) {
	log := &callLog{}
	deps := newMockDeps(log, true)

	cfg := &config.DevcontainerConfig{
		RemoteUser:            "vscode",
		RemoteWorkspaceFolder: "/workspaces/test",
		PostStartCommand:      json.RawMessage(`"echo hi"`),
	}

	err := HandleExistingContainer(context.Background(), "abcdef123456abcdef", "ws-1", cfg, nil, deps)
	if err != nil {
		t.Fatal(err)
	}

	if log.restarted {
		t.Error("should not restart a running container")
	}
	if !log.ensuredBinary {
		t.Error("should ensure devc binary")
	}
	if log.enteredContainer != "abcdef123456abcdef" {
		t.Errorf("entered container = %q, want %q", log.enteredContainer, "abcdef123456abcdef")
	}
	if log.enteredUser != "vscode" {
		t.Errorf("entered user = %q, want %q", log.enteredUser, "vscode")
	}
}

func TestHandleExistingContainer_Stopped(t *testing.T) {
	log := &callLog{}
	deps := newMockDeps(log, false)

	cfg := &config.DevcontainerConfig{
		RemoteUser:            "dev",
		RemoteWorkspaceFolder: "/workspaces/test",
		PostStartCommand:      json.RawMessage(`"echo start"`),
	}

	err := HandleExistingContainer(context.Background(), "bbcdef456789abcdef", "ws-2", cfg, nil, deps)
	if err != nil {
		t.Fatal(err)
	}

	if !log.restarted {
		t.Error("should restart a stopped container")
	}
	// After restart, container ID changes
	if log.enteredContainer != "newcontainer123456" {
		t.Errorf("entered container = %q, want %q", log.enteredContainer, "newcontainer123456")
	}
	if log.lifecycleCount == 0 {
		t.Error("should run postStartCommand hooks")
	}
}

func TestHandleExistingContainer_ResolvedUserPropagation(t *testing.T) {
	log := &callLog{}
	deps := newMockDeps(log, true)
	deps.ResolveUser = func(ctx context.Context, containerID, remoteUser string) string {
		// Simulate fallback to root
		return "root"
	}

	cfg := &config.DevcontainerConfig{
		RemoteUser:            "nonexistent",
		RemoteWorkspaceFolder: "/workspaces/test",
	}

	err := HandleExistingContainer(context.Background(), "abcdef123456abcdef", "ws-1", cfg, nil, deps)
	if err != nil {
		t.Fatal(err)
	}

	// cfg.RemoteUser should be updated
	if cfg.RemoteUser != "root" {
		t.Errorf("cfg.RemoteUser = %q, want %q", cfg.RemoteUser, "root")
	}
	// Setup should use resolved user
	if log.setupUser != "root" {
		t.Errorf("Setup user = %q, want %q", log.setupUser, "root")
	}
	// Enter should use resolved user
	if log.enteredUser != "root" {
		t.Errorf("Enter user = %q, want %q", log.enteredUser, "root")
	}
}

func TestHandleExistingContainer_DotfilesPropagation(t *testing.T) {
	var setupDotfiles []string
	log := &callLog{}
	deps := newMockDeps(log, true)
	deps.Setup = func(containerID, remoteUser string, dotfiles []string) error {
		setupDotfiles = dotfiles
		return nil
	}

	cfg := &config.DevcontainerConfig{
		RemoteUser:            "vscode",
		RemoteWorkspaceFolder: "/workspaces/test",
	}
	dotfiles := []string{".bashrc", ".gitconfig"}

	err := HandleExistingContainer(context.Background(), "abcdef123456abcdef", "ws-1", cfg, dotfiles, deps)
	if err != nil {
		t.Fatal(err)
	}

	if len(setupDotfiles) != 2 || setupDotfiles[0] != ".bashrc" || setupDotfiles[1] != ".gitconfig" {
		t.Errorf("Setup dotfiles = %v, want [.bashrc .gitconfig]", setupDotfiles)
	}
}

func TestHandleExistingContainer_StoppedRunHooksUsesNewContainerID(t *testing.T) {
	var hookContainerID string
	log := &callLog{}
	deps := newMockDeps(log, false)
	deps.RunHooks = func(ctx context.Context, containerID, user string, hooks ...[]container.LifecycleCommand) error {
		hookContainerID = containerID
		log.lifecycleCount += len(hooks)
		return nil
	}

	cfg := &config.DevcontainerConfig{
		RemoteUser:            "vscode",
		RemoteWorkspaceFolder: "/workspaces/test",
		PostStartCommand:      json.RawMessage(`"echo start"`),
	}

	err := HandleExistingContainer(context.Background(), "oldcontainer12345678", "ws-1", cfg, nil, deps)
	if err != nil {
		t.Fatal(err)
	}

	if hookContainerID != "newcontainer123456" {
		t.Errorf("RunHooks containerID = %q, want %q", hookContainerID, "newcontainer123456")
	}
}

func TestHandleExistingContainer_StoppedHookCount(t *testing.T) {
	log := &callLog{}
	deps := newMockDeps(log, false)

	cfg := &config.DevcontainerConfig{
		RemoteUser:            "vscode",
		RemoteWorkspaceFolder: "/workspaces/test",
		PostStartCommand:      json.RawMessage(`"echo start"`),
	}

	err := HandleExistingContainer(context.Background(), "abcdef123456abcdef", "ws-1", cfg, nil, deps)
	if err != nil {
		t.Fatal(err)
	}

	// Only postStartCommand should be run (1 hook group)
	if log.lifecycleCount != 1 {
		t.Errorf("lifecycle hook groups = %d, want 1 (postStart only)", log.lifecycleCount)
	}
}

func TestFinalizeNewContainer_LifecycleHookCount(t *testing.T) {
	log := &callLog{}
	deps := newMockDeps(log, false)

	cfg := &config.DevcontainerConfig{
		RemoteUser:            "vscode",
		RemoteWorkspaceFolder: "/workspaces/proj",
		OnCreateCommand:       json.RawMessage(`"echo create"`),
		PostCreateCommand:     json.RawMessage(`"echo postcreate"`),
		PostStartCommand:      json.RawMessage(`"echo start"`),
	}

	err := FinalizeNewContainer(context.Background(), "abcdef123456abcdef", "ws-1", cfg, nil, deps)
	if err != nil {
		t.Fatal(err)
	}

	// onCreate + postCreate + postStart = 3 hook groups
	if log.lifecycleCount != 3 {
		t.Errorf("lifecycle hook groups = %d, want 3 (onCreate + postCreate + postStart)", log.lifecycleCount)
	}
}

func TestFinalizeNewContainer_WithPreHooks(t *testing.T) {
	log := &callLog{}
	deps := newMockDeps(log, false)
	deps.PreHooks = func(ctx context.Context, containerID string) error {
		log.preHooksCalled = true
		return nil
	}

	cfg := &config.DevcontainerConfig{
		RemoteUser:            "vscode",
		RemoteWorkspaceFolder: "/workspaces/proj",
		OnCreateCommand:       json.RawMessage(`"echo create"`),
		PostCreateCommand:     json.RawMessage(`"echo postcreate"`),
		PostStartCommand:      json.RawMessage(`"echo start"`),
	}

	err := FinalizeNewContainer(context.Background(), "ccdef789012345678", "ws-3", cfg, nil, deps)
	if err != nil {
		t.Fatal(err)
	}

	if !log.preHooksCalled {
		t.Error("preHooks should be called")
	}
	if log.lifecycleCount == 0 {
		t.Error("should run lifecycle hooks")
	}
	if log.enteredUser != "vscode" {
		t.Errorf("entered user = %q, want %q", log.enteredUser, "vscode")
	}
}

func TestFinalizeNewContainer_WithoutPreHooks(t *testing.T) {
	log := &callLog{}
	deps := newMockDeps(log, false)

	cfg := &config.DevcontainerConfig{
		RemoteUser:            "root",
		RemoteWorkspaceFolder: "/workspaces/proj",
	}

	err := FinalizeNewContainer(context.Background(), "ddabcdef012345678", "ws-4", cfg, nil, deps)
	if err != nil {
		t.Fatal(err)
	}

	if log.preHooksCalled {
		t.Error("preHooks should not be called when nil")
	}
	if log.enteredContainer != "ddabcdef012345678" {
		t.Errorf("entered container = %q, want %q", log.enteredContainer, "ddabcdef012345678")
	}
}
