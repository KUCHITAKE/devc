package docker

import (
	"reflect"
	"testing"
)

func TestDotfileLinkCommands(t *testing.T) {
	got := dotfileLinkCommands("/opt/devc-dotfiles/.claude", "/home/vscode/.claude")
	want := [][]string{
		{"mkdir", "-p", "/home/vscode"},
		{"rm", "-rf", "/home/vscode/.claude"},
		{"ln", "-sfn", "/opt/devc-dotfiles/.claude", "/home/vscode/.claude"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("dotfileLinkCommands() = %v, want %v", got, want)
	}
}

func TestDotfileLinkCommandsRemovesBeforeLinking(t *testing.T) {
	// A devcontainer feature may pre-create the target as a populated
	// directory (e.g. claude-code creates ~/.claude). `ln -sfn` cannot
	// replace a directory, so the target must be removed first, otherwise
	// the link lands inside the directory and host credentials never apply.
	cmds := dotfileLinkCommands("/opt/devc-dotfiles/.claude", "/home/vscode/.claude")
	rmIdx, lnIdx := -1, -1
	for i, c := range cmds {
		switch c[0] {
		case "rm":
			rmIdx = i
		case "ln":
			lnIdx = i
		}
	}
	if rmIdx == -1 {
		t.Fatal("expected an rm command to clear the existing target")
	}
	if lnIdx == -1 {
		t.Fatal("expected an ln command to create the symlink")
	}
	if rmIdx > lnIdx {
		t.Fatalf("rm (index %d) must run before ln (index %d)", rmIdx, lnIdx)
	}
}

func TestRemoteUserFromMetadata(t *testing.T) {
	tests := []struct {
		name     string
		metadata string
		want     string
	}{
		{
			name:     "standard devcontainer base image metadata",
			metadata: `[{"id":"ghcr.io/devcontainers/features/common-utils:2"},{"id":"ghcr.io/devcontainers/features/git:1"},{"remoteUser":"vscode"}]`,
			want:     "vscode",
		},
		{
			name:     "empty metadata array",
			metadata: `[]`,
			want:     "",
		},
		{
			name:     "no remoteUser in any entry",
			metadata: `[{"id":"ghcr.io/devcontainers/features/common-utils:2"}]`,
			want:     "",
		},
		{
			name:     "last remoteUser wins",
			metadata: `[{"remoteUser":"node"},{"remoteUser":"vscode"}]`,
			want:     "vscode",
		},
		{
			name:     "invalid JSON",
			metadata: `not json`,
			want:     "",
		},
		{
			name:     "empty remoteUser is ignored",
			metadata: `[{"remoteUser":"vscode"},{"remoteUser":""}]`,
			want:     "vscode",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := RemoteUserFromMetadata(tt.metadata)
			if got != tt.want {
				t.Errorf("RemoteUserFromMetadata() = %q, want %q", got, tt.want)
			}
		})
	}
}
