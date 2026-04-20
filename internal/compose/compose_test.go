package compose

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/closer/devc/internal/config"
)

func TestParseComposeConfig_ServiceRequired(t *testing.T) {
	raw := map[string]json.RawMessage{
		"dockerComposeFile": json.RawMessage(`"docker-compose.yml"`),
	}
	ws := config.Workspace{Dir: "/tmp/test", Name: "test", ID: "test"}
	_, err := ParseConfig(ws, raw)
	if err == nil {
		t.Fatal("expected error for missing service")
	}
	if !strings.Contains(err.Error(), "service") {
		t.Fatalf("error = %q, want mention of service", err.Error())
	}
}

func TestParseComposeConfig_Defaults(t *testing.T) {
	raw := map[string]json.RawMessage{
		"dockerComposeFile": json.RawMessage(`"docker-compose.yml"`),
		"service":           json.RawMessage(`"app"`),
	}
	ws := config.Workspace{Dir: "/tmp/test", Name: "test", ID: "test"}
	cc, err := ParseConfig(ws, raw)
	if err != nil {
		t.Fatal(err)
	}
	if cc.Service != "app" {
		t.Fatalf("Service = %q, want %q", cc.Service, "app")
	}
	if !cc.OverrideCommand {
		t.Fatal("OverrideCommand should default to true")
	}
	if cc.RunServices != nil {
		t.Fatalf("RunServices should be nil, got %v", cc.RunServices)
	}
}

func TestParseComposeConfig_AllFields(t *testing.T) {
	raw := map[string]json.RawMessage{
		"dockerComposeFile": json.RawMessage(`["docker-compose.yml", "docker-compose.dev.yml"]`),
		"service":           json.RawMessage(`"web"`),
		"runServices":       json.RawMessage(`["web", "db", "redis"]`),
		"overrideCommand":   json.RawMessage(`false`),
	}
	ws := config.Workspace{Dir: "/tmp/test", Name: "test", ID: "test"}
	cc, err := ParseConfig(ws, raw)
	if err != nil {
		t.Fatal(err)
	}
	if cc.Service != "web" {
		t.Fatalf("Service = %q, want %q", cc.Service, "web")
	}
	if cc.OverrideCommand {
		t.Fatal("OverrideCommand should be false")
	}
	if len(cc.RunServices) != 3 {
		t.Fatalf("RunServices count = %d, want 3", len(cc.RunServices))
	}
	if len(cc.Files) != 2 {
		t.Fatalf("Files count = %d, want 2", len(cc.Files))
	}
}

func TestWriteComposeOverride_Basic(t *testing.T) {
	dir := t.TempDir()
	dcDir := filepath.Join(dir, ".devcontainer")
	if err := os.MkdirAll(dcDir, 0o755); err != nil {
		t.Fatal(err)
	}

	ws := config.Workspace{Dir: dir, Name: "myproject", ID: "myproject"}
	cc := &Config{
		Service:         "app",
		OverrideCommand: true,
	}
	credDir := filepath.Join(dir, "devc-credentials")
	if err := os.MkdirAll(credDir, 0o755); err != nil {
		t.Fatal(err)
	}
	mounts := []config.HostMount{
		{Source: credDir, Target: "/tmp/devc-credentials"},
	}
	ports := []string{"3000:3000", "5432:5432"}

	path, err := WriteOverride(ws, cc, "/workspaces/myproject", mounts, ports, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Remove(path) }()

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	content := string(data)

	// Check structure
	if !strings.Contains(content, "services:") {
		t.Fatal("missing services: key")
	}
	if !strings.Contains(content, "  app:") {
		t.Fatal("missing service name")
	}
	if !strings.Contains(content, "command: sleep infinity") {
		t.Fatal("missing sleep infinity command")
	}
	if !strings.Contains(content, "working_dir: /workspaces/myproject") {
		t.Fatal("missing working_dir")
	}
	if !strings.Contains(content, "volumes:") {
		t.Fatal("missing volumes section")
	}
	if !strings.Contains(content, credDir+":/tmp/devc-credentials") {
		t.Fatal("missing credentials mount")
	}
	if !strings.Contains(content, "ports:") {
		t.Fatal("missing ports section")
	}
	if !strings.Contains(content, `"3000:3000"`) {
		t.Fatal("missing port 3000")
	}
	if !strings.Contains(content, `"5432:5432"`) {
		t.Fatal("missing port 5432")
	}
}

func TestWriteComposeOverride_NoOverrideCommand(t *testing.T) {
	dir := t.TempDir()
	dcDir := filepath.Join(dir, ".devcontainer")
	if err := os.MkdirAll(dcDir, 0o755); err != nil {
		t.Fatal(err)
	}

	ws := config.Workspace{Dir: dir, Name: "myproject", ID: "myproject"}
	cc := &Config{
		Service:         "app",
		OverrideCommand: false,
	}

	path, err := WriteOverride(ws, cc, "/workspaces/myproject", nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Remove(path) }()

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	content := string(data)

	if strings.Contains(content, "sleep infinity") {
		t.Fatal("should not contain sleep infinity when overrideCommand=false")
	}
}

func TestWriteComposeOverride_NoPorts(t *testing.T) {
	dir := t.TempDir()
	dcDir := filepath.Join(dir, ".devcontainer")
	if err := os.MkdirAll(dcDir, 0o755); err != nil {
		t.Fatal(err)
	}

	ws := config.Workspace{Dir: dir, Name: "myproject", ID: "myproject"}
	cc := &Config{
		Service:         "app",
		OverrideCommand: true,
	}

	path, err := WriteOverride(ws, cc, "/workspaces/myproject", nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Remove(path) }()

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	content := string(data)

	if strings.Contains(content, "ports:") {
		t.Fatal("should not contain ports section when no ports specified")
	}
}

func TestWriteComposeOverride_ContainerEnv(t *testing.T) {
	dir := t.TempDir()
	dcDir := filepath.Join(dir, ".devcontainer")
	if err := os.MkdirAll(dcDir, 0o755); err != nil {
		t.Fatal(err)
	}

	ws := config.Workspace{Dir: dir, Name: "myproject", ID: "myproject"}
	cc := &Config{
		Service:         "app",
		OverrideCommand: true,
	}
	env := map[string]string{
		"DB_HOST": "postgres",
		"DB_PORT": "5432",
	}

	path, err := WriteOverride(ws, cc, "/workspaces/myproject", nil, nil, env)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Remove(path) }()

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	content := string(data)

	if !strings.Contains(content, "environment:") {
		t.Fatal("missing environment section")
	}
	if !strings.Contains(content, `DB_HOST: "postgres"`) {
		t.Fatal("missing DB_HOST")
	}
	if !strings.Contains(content, `DB_PORT: "5432"`) {
		t.Fatal("missing DB_PORT")
	}
}

func TestComposeProject(t *testing.T) {
	ws := config.Workspace{Dir: "/home/user/projects/myapp", Name: "myapp", ID: "myapp-abc12345"}
	got := Project(ws)
	want := "myapp-abc12345_devcontainer"
	if got != want {
		t.Fatalf("Project() = %q, want %q", got, want)
	}
}
