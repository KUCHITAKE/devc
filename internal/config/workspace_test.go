package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCollectPorts(t *testing.T) {
	tests := []struct {
		name     string
		rawJSON  string
		cliPorts []string
		want     []string
	}{
		{
			name:     "cli ports only",
			rawJSON:  `{}`,
			cliPorts: []string{"3000:3000"},
			want:     []string{"3000:3000"},
		},
		{
			name:     "forwardPorts as numbers",
			rawJSON:  `{"forwardPorts": [3000, 5173]}`,
			cliPorts: nil,
			want:     []string{"3000", "5173"},
		},
		{
			name:     "appPort as number",
			rawJSON:  `{"appPort": 8080}`,
			cliPorts: nil,
			want:     []string{"8080"},
		},
		{
			name:     "appPort as string",
			rawJSON:  `{"appPort": "8080:8080"}`,
			cliPorts: nil,
			want:     []string{"8080:8080"},
		},
		{
			name:     "appPort as array of numbers",
			rawJSON:  `{"appPort": [3000, 5000]}`,
			cliPorts: nil,
			want:     []string{"3000", "5000"},
		},
		{
			name:     "appPort as array of strings",
			rawJSON:  `{"appPort": ["3000:3000", "5000:5000"]}`,
			cliPorts: nil,
			want:     []string{"3000:3000", "5000:5000"},
		},
		{
			name:     "deduplication: cli overrides forwardPorts",
			rawJSON:  `{"forwardPorts": [3000]}`,
			cliPorts: []string{"3000"},
			want:     []string{"3000"},
		},
		{
			name:     "combined forwardPorts and appPort",
			rawJSON:  `{"forwardPorts": [3000], "appPort": 8080}`,
			cliPorts: nil,
			want:     []string{"3000", "8080"},
		},
		{
			name:     "empty config no ports",
			rawJSON:  `{}`,
			cliPorts: nil,
			want:     []string{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var raw map[string]json.RawMessage
			if err := json.Unmarshal([]byte(tt.rawJSON), &raw); err != nil {
				t.Fatalf("invalid test JSON: %v", err)
			}
			got := CollectPorts(raw, tt.cliPorts)
			if len(got) == 0 && len(tt.want) == 0 {
				return
			}
			if len(got) != len(tt.want) {
				t.Fatalf("CollectPorts() = %v, want %v", got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Fatalf("CollectPorts() = %v, want %v", got, tt.want)
				}
			}
		})
	}
}

func TestResolvePort(t *testing.T) {
	tests := []struct {
		name string
		port string
	}{
		{"host:container passes through", "8080:3000"},
		{"non-numeric passes through", "invalid"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ResolvePort(tt.port)
			if got != tt.port {
				t.Fatalf("ResolvePort(%q) = %q, want %q", tt.port, got, tt.port)
			}
		})
	}

	// Test bare port resolution (binds to an available port)
	t.Run("bare port resolves to host:container", func(t *testing.T) {
		got := ResolvePort("39876")
		// Should be in host:container format
		if got == "39876" {
			t.Fatal("ResolvePort(\"39876\") should resolve to host:container format")
		}
		if len(got) < 5 || got[len(got)-6:] != ":39876" {
			t.Fatalf("ResolvePort(\"39876\") = %q, expected to end with :39876", got)
		}
	})
}

func TestResolveWorkspace_UniqueID(t *testing.T) {
	// Two directories with the same basename but different parents
	// must produce different IDs.
	dirA := filepath.Join(t.TempDir(), "app")
	dirB := filepath.Join(t.TempDir(), "app")
	if err := os.MkdirAll(dirA, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dirB, 0o755); err != nil {
		t.Fatal(err)
	}

	wsA, err := ResolveWorkspace(dirA)
	if err != nil {
		t.Fatal(err)
	}
	wsB, err := ResolveWorkspace(dirB)
	if err != nil {
		t.Fatal(err)
	}

	// Both should have the same name
	if wsA.Name != wsB.Name {
		t.Fatalf("names should match: %q vs %q", wsA.Name, wsB.Name)
	}
	if wsA.Name != "app" {
		t.Fatalf("name = %q, want %q", wsA.Name, "app")
	}

	// IDs must differ
	if wsA.ID == wsB.ID {
		t.Fatalf("IDs should differ for different paths, both got %q", wsA.ID)
	}

	// IDs should start with the basename
	if !strings.HasPrefix(wsA.ID, "app-") {
		t.Fatalf("id %q should start with 'app-'", wsA.ID)
	}
}

func TestComposeFiles(t *testing.T) {
	tmpDir := t.TempDir()
	ws := Workspace{Dir: tmpDir, Name: "test", ID: "test"}
	dcDir := filepath.Join(tmpDir, ".devcontainer")
	if err := os.MkdirAll(dcDir, 0o755); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name    string
		rawJSON string
		want    []string
	}{
		{
			name:    "no dockerComposeFile",
			rawJSON: `{}`,
			want:    nil,
		},
		{
			name:    "single string",
			rawJSON: `{"dockerComposeFile": "docker-compose.yml"}`,
			want:    []string{filepath.Join(dcDir, "docker-compose.yml")},
		},
		{
			name:    "array of strings",
			rawJSON: `{"dockerComposeFile": ["docker-compose.yml", "docker-compose.override.yml"]}`,
			want: []string{
				filepath.Join(dcDir, "docker-compose.yml"),
				filepath.Join(dcDir, "docker-compose.override.yml"),
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var raw map[string]json.RawMessage
			if err := json.Unmarshal([]byte(tt.rawJSON), &raw); err != nil {
				t.Fatalf("invalid test JSON: %v", err)
			}
			got := ComposeFiles(ws, raw)
			if len(got) != len(tt.want) {
				t.Fatalf("ComposeFiles() = %v, want %v", got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Fatalf("ComposeFiles() = %v, want %v", got, tt.want)
				}
			}
		})
	}
}
