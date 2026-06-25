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

func TestContainerDisplayName(t *testing.T) {
	tests := []struct {
		name string
		c    dockercontainer.Summary
		want string
	}{
		{
			"devc container",
			dockercontainer.Summary{Names: []string{"/devc-myproject"}},
			"myproject",
		},
		{
			"compose container",
			dockercontainer.Summary{
				Names:  []string{"/myproject-app-1"},
				Labels: map[string]string{"com.docker.compose.project": "myproject", "com.docker.compose.service": "app"},
			},
			"myproject/app",
		},
		{
			"compose container no service",
			dockercontainer.Summary{
				Names:  []string{"/myproject-1"},
				Labels: map[string]string{"com.docker.compose.project": "myproject"},
			},
			"myproject",
		},
		{
			"generic container",
			dockercontainer.Summary{Names: []string{"/some-container"}},
			"some-container",
		},
		{
			"no names fallback to ID",
			dockercontainer.Summary{ID: "abcdef123456789abcdef"},
			"abcdef123456",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := containerDisplayName(tt.c)
			if got != tt.want {
				t.Errorf("containerDisplayName() = %q, want %q", got, tt.want)
			}
		})
	}
}
