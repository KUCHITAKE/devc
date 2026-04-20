package docker

import "testing"

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
