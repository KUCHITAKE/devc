package main

import (
	"reflect"
	"testing"

	"github.com/closer/devc/internal/preflight"
	dockercontainer "github.com/docker/docker/api/types/container"
)

func TestToRunningWorkspaces_ImageWorkspace(t *testing.T) {
	containers := []dockercontainer.Summary{
		{
			Labels: map[string]string{"devcontainer.local_folder": "/home/me/app"},
			Ports: []dockercontainer.Port{
				{PublicPort: 3000, PrivatePort: 3000},
				{PublicPort: 0, PrivatePort: 5432}, // unpublished, skipped
			},
		},
		{
			// Not devc-managed (no label, no compose project): skipped.
			Labels: map[string]string{},
			Ports:  []dockercontainer.Port{{PublicPort: 8080, PrivatePort: 80}},
		},
	}

	got := toRunningWorkspaces(containers, "/somewhere/else", "")
	want := []preflight.RunningWS{
		{Name: "app", Path: "/home/me/app", HostPorts: []int{3000}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("toRunningWorkspaces()\n got: %+v\nwant: %+v", got, want)
	}
}

func TestToRunningWorkspaces_ComposeAggregated(t *testing.T) {
	// Two service containers of the same compose project collapse into one
	// workspace whose ports are the union of both.
	containers := []dockercontainer.Summary{
		{
			Labels: map[string]string{"com.docker.compose.project": "webapp-abc123_devcontainer"},
			Ports:  []dockercontainer.Port{{PublicPort: 8765, PrivatePort: 8765}},
		},
		{
			Labels: map[string]string{"com.docker.compose.project": "webapp-abc123_devcontainer"},
			Ports:  []dockercontainer.Port{{PublicPort: 5432, PrivatePort: 5432}},
		},
	}

	got := toRunningWorkspaces(containers, "/home/user/app", "")
	want := []preflight.RunningWS{
		{Name: "webapp-abc123", Path: "", HostPorts: []int{5432, 8765}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("toRunningWorkspaces()\n got: %+v\nwant: %+v", got, want)
	}
}

func TestToRunningWorkspaces_ComposeWorkingDirRecoversPath(t *testing.T) {
	containers := []dockercontainer.Summary{
		{
			Labels: map[string]string{
				"com.docker.compose.project":             "webapp-abc123_devcontainer",
				"com.docker.compose.project.working_dir": "/home/user/checkout-a/webapp/.devcontainer",
			},
			Ports: []dockercontainer.Port{{PublicPort: 8765, PrivatePort: 8080}},
		},
	}
	got := toRunningWorkspaces(containers, "/home/user/checkout-b/webapp", "")
	want := []preflight.RunningWS{
		{Name: "webapp", Path: "/home/user/checkout-a/webapp", HostPorts: []int{8765}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("toRunningWorkspaces()\n got: %+v\nwant: %+v", got, want)
	}
}

func TestToRunningWorkspaces_ExcludesSelfProject(t *testing.T) {
	containers := []dockercontainer.Summary{
		{
			Labels: map[string]string{"com.docker.compose.project": "app-self_devcontainer"},
			Ports:  []dockercontainer.Port{{PublicPort: 8765, PrivatePort: 8765}},
		},
	}
	got := toRunningWorkspaces(containers, "/home/me/app", "app-self_devcontainer")
	if len(got) != 0 {
		t.Fatalf("self compose project must be excluded, got %+v", got)
	}
}

func TestToRunningWorkspaces_ExcludesSelfPath(t *testing.T) {
	containers := []dockercontainer.Summary{
		{
			Labels: map[string]string{"devcontainer.local_folder": "/home/me/app"},
			Ports:  []dockercontainer.Port{{PublicPort: 3000, PrivatePort: 3000}},
		},
	}
	got := toRunningWorkspaces(containers, "/home/me/app", "")
	if len(got) != 0 {
		t.Fatalf("self path must be excluded, got %+v", got)
	}
}
