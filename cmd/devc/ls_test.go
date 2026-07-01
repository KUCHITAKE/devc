package main

import (
	"testing"
	"time"

	dockercontainer "github.com/docker/docker/api/types/container"
)

func TestFormatUptime(t *testing.T) {
	tests := []struct {
		state   string
		created int64
		want    string
	}{
		{"exited", time.Now().Unix(), "-"},
		{"running", time.Now().Add(-30 * time.Second).Unix(), "30s"},
		{"running", time.Now().Add(-5 * time.Minute).Unix(), "5m"},
		{"running", time.Now().Add(-90 * time.Minute).Unix(), "1h30m"},
		{"running", time.Now().Add(-2 * time.Hour).Unix(), "2h"},
		{"running", time.Now().Add(-25 * time.Hour).Unix(), "1d1h"},
		{"running", time.Now().Add(-48 * time.Hour).Unix(), "2d"},
	}

	for _, tt := range tests {
		got := formatUptime(tt.state, tt.created)
		if got != tt.want {
			t.Errorf("formatUptime(%q, %d) = %q, want %q", tt.state, tt.created, got, tt.want)
		}
	}
}

func TestFormatPorts(t *testing.T) {
	tests := []struct {
		name  string
		ports []dockercontainer.Port
		want  string
	}{
		{"nil", nil, "-"},
		{"empty slice", []dockercontainer.Port{}, "-"},
		{"only private port (no public)", []dockercontainer.Port{
			{PrivatePort: 3000, PublicPort: 0},
		}, "-"},
		{"same public and private", []dockercontainer.Port{
			{PublicPort: 3000, PrivatePort: 3000},
		}, "3000"},
		{"different public and private", []dockercontainer.Port{
			{PublicPort: 8080, PrivatePort: 3000},
		}, "8080→3000"},
		{"multiple ports", []dockercontainer.Port{
			{PublicPort: 3000, PrivatePort: 3000},
			{PublicPort: 5432, PrivatePort: 5432},
		}, "3000, 5432"},
		{"deduplication", []dockercontainer.Port{
			{PublicPort: 3000, PrivatePort: 3000},
			{PublicPort: 3000, PrivatePort: 3000},
		}, "3000"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := formatPorts(tt.ports)
			if got != tt.want {
				t.Errorf("formatPorts() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestBuildWorkspaceRows(t *testing.T) {
	containers := []dockercontainer.Summary{
		// Image-based workspace: single container.
		{
			Labels:  map[string]string{"devcontainer.local_folder": "/home/user/app"},
			State:   "running",
			Created: 100,
			Ports:   []dockercontainer.Port{{PublicPort: 3000, PrivatePort: 3000}},
		},
		// Compose workspace: two services, one running, one exited. They must
		// collapse into a single row with the union of ports and "running".
		{
			Labels: map[string]string{
				"com.docker.compose.project":             "web-abc123_devcontainer",
				"com.docker.compose.project.working_dir": "/home/user/web/.devcontainer",
			},
			State:   "running",
			Created: 200,
			Ports:   []dockercontainer.Port{{PublicPort: 8080, PrivatePort: 80}},
		},
		{
			Labels: map[string]string{
				"com.docker.compose.project":             "web-abc123_devcontainer",
				"com.docker.compose.project.working_dir": "/home/user/web/.devcontainer",
			},
			State:   "exited",
			Created: 150,
			Ports:   []dockercontainer.Port{{PublicPort: 5432, PrivatePort: 5432}},
		},
		// Not devc-managed: ignored.
		{
			Labels:  map[string]string{"com.docker.compose.project": "some-other-stack"},
			State:   "running",
			Created: 300,
			Ports:   []dockercontainer.Port{{PublicPort: 9999, PrivatePort: 9999}},
		},
	}

	rows := buildWorkspaceRows(containers)

	if len(rows) != 2 {
		t.Fatalf("expected 2 workspace rows, got %d: %+v", len(rows), rows)
	}
	// Sorted most-recent first: compose (created 200) before image (100).
	if rows[0].name != "web" || rows[0].status != "running" || rows[0].path != "/home/user/web" {
		t.Fatalf("unexpected compose row: %+v", rows[0])
	}
	if got := formatPorts(rows[0].ports); got != "8080→80, 5432" {
		t.Fatalf("compose ports = %q, want %q", got, "8080→80, 5432")
	}
	if rows[1].name != "app" || rows[1].status != "running" || rows[1].path != "/home/user/app" {
		t.Fatalf("unexpected image row: %+v", rows[1])
	}
}

func TestBuildWorkspaceRows_ComposeAllStopped(t *testing.T) {
	containers := []dockercontainer.Summary{
		{
			Labels: map[string]string{
				"com.docker.compose.project":             "web-abc123_devcontainer",
				"com.docker.compose.project.working_dir": "/home/user/web/.devcontainer",
			},
			State:   "exited",
			Created: 150,
		},
		{
			Labels: map[string]string{
				"com.docker.compose.project":             "web-abc123_devcontainer",
				"com.docker.compose.project.working_dir": "/home/user/web/.devcontainer",
			},
			State:   "exited",
			Created: 120,
		},
	}
	rows := buildWorkspaceRows(containers)
	if len(rows) != 1 || rows[0].status != "exited" {
		t.Fatalf("expected single exited row, got %+v", rows)
	}
}
