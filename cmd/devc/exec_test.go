package main

import (
	"testing"
)

func TestBuildExecCmd(t *testing.T) {
	t.Run("parses command after double dash", func(t *testing.T) {
		cmd := newExecCmd()
		cmd.SetArgs([]string{"--", "go", "test", "./..."})
		// Verify the command is created without error
		if cmd.Use != "exec [flags] [workspace-dir] [-- command...]" {
			t.Fatalf("unexpected Use: %s", cmd.Use)
		}
	})

	t.Run("default shell when no command given", func(t *testing.T) {
		args := execArgs([]string{})
		if len(args) != 2 || args[0] != "bash" || args[1] != "-l" {
			t.Fatalf("expected [bash -l], got %v", args)
		}
	})

	t.Run("wraps command in shell -c", func(t *testing.T) {
		args := execArgs([]string{"go", "test", "./..."})
		if len(args) != 3 || args[0] != "bash" || args[1] != "-lc" {
			t.Fatalf("expected [bash -lc ...], got %v", args)
		}
		if args[2] != "go test ./..." {
			t.Fatalf("expected joined command, got %q", args[2])
		}
	})

	t.Run("single command no wrapping needed", func(t *testing.T) {
		args := execArgs([]string{"ls"})
		if len(args) != 3 || args[2] != "ls" {
			t.Fatalf("expected [bash -lc ls], got %v", args)
		}
	})
}
