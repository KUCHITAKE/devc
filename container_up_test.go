package main

import (
	"encoding/json"
	"testing"

	"github.com/docker/docker/api/types/mount"
)

func TestBuildContainerMounts(t *testing.T) {
	tmpDir := t.TempDir()
	ws := workspace{dir: tmpDir, name: "project"}
	wsFolder := "/workspaces/project"

	// Create a real directory for the host mount so os.Stat succeeds
	existingDir := t.TempDir()
	mounts := []hostMount{
		{source: existingDir, target: "/opt/devc-dotfiles/config-nvim"},
		{source: "/nonexistent/path/12345", target: "/opt/devc-dotfiles/missing"},
	}

	got := buildContainerMounts(ws, wsFolder, mounts)

	// Should have workspace mount + 1 existing host mount (nonexistent skipped)
	if len(got) != 2 {
		t.Fatalf("mount count = %d, want 2", len(got))
	}
	// First should be workspace mount
	if got[0].Source != tmpDir || got[0].Target != wsFolder {
		t.Fatalf("workspace mount = %s:%s, want %s:%s", got[0].Source, got[0].Target, tmpDir, wsFolder)
	}
	// Second should be the existing host mount
	if got[1].Source != existingDir || got[1].Target != "/opt/devc-dotfiles/config-nvim" {
		t.Fatalf("host mount = %s:%s", got[1].Source, got[1].Target)
	}
	// All should be bind mounts
	for _, m := range got {
		if m.Type != mount.TypeBind {
			t.Fatalf("mount type = %v, want %v", m.Type, mount.TypeBind)
		}
	}
}

func TestBuildPortBindings(t *testing.T) {
	tests := []struct {
		name      string
		ports     []string
		wantPorts int
		wantErr   bool
	}{
		{
			name:      "single port mapping",
			ports:     []string{"8080:3000"},
			wantPorts: 1,
		},
		{
			name:      "multiple port mappings",
			ports:     []string{"8080:3000", "5173:5173"},
			wantPorts: 2,
		},
		{
			name:      "empty list",
			ports:     nil,
			wantPorts: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			portSet, portMap, err := buildPortBindings(tt.ports)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(portSet) != tt.wantPorts {
				t.Fatalf("portSet count = %d, want %d", len(portSet), tt.wantPorts)
			}
			if len(portMap) != tt.wantPorts {
				t.Fatalf("portMap count = %d, want %d", len(portMap), tt.wantPorts)
			}
		})
	}

	// Specific check for 8080:3000
	t.Run("8080:3000 mapping details", func(t *testing.T) {
		portSet, portMap, err := buildPortBindings([]string{"8080:3000"})
		if err != nil {
			t.Fatal(err)
		}
		if _, ok := portSet["3000/tcp"]; !ok {
			t.Fatal("portSet should contain 3000/tcp")
		}
		bindings, ok := portMap["3000/tcp"]
		if !ok {
			t.Fatal("portMap should contain 3000/tcp")
		}
		if len(bindings) != 1 || bindings[0].HostPort != "8080" {
			t.Fatalf("binding = %v, want [{HostPort: 8080}]", bindings)
		}
	})
}

func TestParseLifecycleHook_Nil(t *testing.T) {
	got := parseLifecycleHook(nil)
	if len(got) != 0 {
		t.Fatalf("expected empty slice, got %v", got)
	}
}

func TestParseLifecycleHook_String(t *testing.T) {
	raw := json.RawMessage(`"echo hello"`)
	got := parseLifecycleHook(raw)
	if len(got) != 1 {
		t.Fatalf("expected 1 command, got %d", len(got))
	}
	want := []string{"sh", "-c", "echo hello"}
	if len(got[0].Command) != len(want) {
		t.Fatalf("Command = %v, want %v", got[0].Command, want)
	}
	for i := range want {
		if got[0].Command[i] != want[i] {
			t.Fatalf("Command = %v, want %v", got[0].Command, want)
		}
	}
	if got[0].Name != "" {
		t.Fatalf("Name = %q, want empty", got[0].Name)
	}
}

func TestParseLifecycleHook_Array(t *testing.T) {
	raw := json.RawMessage(`["echo", "hello"]`)
	got := parseLifecycleHook(raw)
	if len(got) != 1 {
		t.Fatalf("expected 1 command, got %d", len(got))
	}
	want := []string{"echo", "hello"}
	if len(got[0].Command) != len(want) {
		t.Fatalf("Command = %v, want %v", got[0].Command, want)
	}
	for i := range want {
		if got[0].Command[i] != want[i] {
			t.Fatalf("Command = %v, want %v", got[0].Command, want)
		}
	}
}

func TestParseLifecycleHook_MapStringValues(t *testing.T) {
	raw := json.RawMessage(`{"a": "cmd1", "b": "cmd2"}`)
	got := parseLifecycleHook(raw)
	if len(got) != 2 {
		t.Fatalf("expected 2 commands, got %d", len(got))
	}
	// Check that both commands exist (map order is not guaranteed)
	found := map[string]bool{}
	for _, lc := range got {
		found[lc.Name] = true
		if lc.Name == "a" {
			want := []string{"sh", "-c", "cmd1"}
			if len(lc.Command) != len(want) || lc.Command[0] != want[0] || lc.Command[2] != want[2] {
				t.Fatalf("Command for 'a' = %v, want %v", lc.Command, want)
			}
		}
		if lc.Name == "b" {
			want := []string{"sh", "-c", "cmd2"}
			if len(lc.Command) != len(want) || lc.Command[0] != want[0] || lc.Command[2] != want[2] {
				t.Fatalf("Command for 'b' = %v, want %v", lc.Command, want)
			}
		}
	}
	if !found["a"] || !found["b"] {
		t.Fatalf("expected both 'a' and 'b', got %v", found)
	}
}

func TestParseLifecycleHook_MapArrayValue(t *testing.T) {
	raw := json.RawMessage(`{"a": ["echo", "hello"]}`)
	got := parseLifecycleHook(raw)
	if len(got) != 1 {
		t.Fatalf("expected 1 command, got %d", len(got))
	}
	if got[0].Name != "a" {
		t.Fatalf("Name = %q, want %q", got[0].Name, "a")
	}
	want := []string{"echo", "hello"}
	if len(got[0].Command) != len(want) {
		t.Fatalf("Command = %v, want %v", got[0].Command, want)
	}
	for i := range want {
		if got[0].Command[i] != want[i] {
			t.Fatalf("Command = %v, want %v", got[0].Command, want)
		}
	}
}
