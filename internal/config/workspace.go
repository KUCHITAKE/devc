package config

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/closer/devc/internal/ui"
)

type Workspace struct {
	Dir  string
	Name string // directory basename (for display and workspace folder)
	ID   string // unique identifier: {basename}-{hash(abs_path)[:8]}
}

type DevcontainerConfig struct {
	Image                 string
	Build                 *BuildConfig
	Features              map[string]map[string]interface{}
	RemoteUser            string
	RemoteWorkspaceFolder string
	ContainerEnv          map[string]string
	OnCreateCommand       json.RawMessage
	PostCreateCommand     json.RawMessage
	PostStartCommand      json.RawMessage
	Raw                   map[string]json.RawMessage
}

type BuildConfig struct {
	Dockerfile string            `json:"dockerfile"`
	Context    string            `json:"context"`
	Args       map[string]string `json:"args"`
	Target     string            `json:"target"`
}

func ParseDevcontainerConfig(ws Workspace) (*DevcontainerConfig, error) {
	raw, err := ReadDevcontainerJSON(ws)
	if err != nil {
		return nil, err
	}

	cfg := &DevcontainerConfig{
		RemoteWorkspaceFolder: "/workspaces/" + ws.Name,
		Raw:                   raw,
	}

	if v, ok := raw["image"]; ok {
		_ = json.Unmarshal(v, &cfg.Image)
	}
	if v, ok := raw["build"]; ok {
		var bc BuildConfig
		if err := json.Unmarshal(v, &bc); err == nil {
			cfg.Build = &bc
		}
	}
	if v, ok := raw["features"]; ok {
		_ = json.Unmarshal(v, &cfg.Features)
	}
	if v, ok := raw["remoteUser"]; ok {
		var u string
		if err := json.Unmarshal(v, &u); err == nil && u != "" {
			cfg.RemoteUser = u
		}
	}
	if v, ok := raw["workspaceFolder"]; ok {
		var f string
		if err := json.Unmarshal(v, &f); err == nil && f != "" {
			cfg.RemoteWorkspaceFolder = f
		}
	}
	if v, ok := raw["containerEnv"]; ok {
		_ = json.Unmarshal(v, &cfg.ContainerEnv)
	}
	if v, ok := raw["onCreateCommand"]; ok {
		cfg.OnCreateCommand = v
	}
	if v, ok := raw["postCreateCommand"]; ok {
		cfg.PostCreateCommand = v
	}
	if v, ok := raw["postStartCommand"]; ok {
		cfg.PostStartCommand = v
	}

	return cfg, nil
}

func ResolveWorkspace(dir string) (Workspace, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return Workspace{}, fmt.Errorf("resolve workspace: %w", err)
	}
	name := filepath.Base(abs)
	hash := fmt.Sprintf("%x", sha256.Sum256([]byte(abs)))[:8]
	return Workspace{Dir: abs, Name: name, ID: name + "-" + hash}, nil
}

func EnsureDevcontainerJSON(ws Workspace) error {
	dcDir := filepath.Join(ws.Dir, ".devcontainer")
	dcJSON := filepath.Join(dcDir, "devcontainer.json")
	if _, err := os.Stat(dcJSON); err == nil {
		return nil
	}
	if err := os.MkdirAll(dcDir, 0o755); err != nil {
		return err
	}
	content := fmt.Sprintf("{\n  \"name\": %q,\n  \"image\": \"mcr.microsoft.com/devcontainers/base:ubuntu\"\n}\n", ws.Name)
	ui.PrintDone("Generated devcontainer.json", dcJSON)
	return os.WriteFile(dcJSON, []byte(content), 0o644)
}

func ReadDevcontainerJSON(ws Workspace) (map[string]json.RawMessage, error) {
	dcJSON := filepath.Join(ws.Dir, ".devcontainer", "devcontainer.json")
	data, err := os.ReadFile(dcJSON)
	if err != nil {
		return nil, err
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("parse devcontainer.json: %w", err)
	}
	return raw, nil
}

func ComposeFiles(ws Workspace, raw map[string]json.RawMessage) []string {
	dcf, ok := raw["dockerComposeFile"]
	if !ok {
		return nil
	}
	dcDir := filepath.Join(ws.Dir, ".devcontainer")

	// Try as string first
	var single string
	if err := json.Unmarshal(dcf, &single); err == nil {
		return []string{filepath.Join(dcDir, single)}
	}
	// Try as array
	var arr []string
	if err := json.Unmarshal(dcf, &arr); err == nil {
		result := make([]string, len(arr))
		for i, f := range arr {
			result[i] = filepath.Join(dcDir, f)
		}
		return result
	}
	return nil
}

func CollectPorts(raw map[string]json.RawMessage, cliPorts []string) []string {
	ports := append([]string{}, cliPorts...)
	seen := make(map[string]bool)
	for _, p := range ports {
		seen[p] = true
	}

	addPort := func(s string) {
		if !seen[s] {
			seen[s] = true
			ports = append(ports, s)
		}
	}

	// forwardPorts: array of numbers
	if fp, ok := raw["forwardPorts"]; ok {
		var nums []json.Number
		if err := json.Unmarshal(fp, &nums); err == nil {
			for _, n := range nums {
				if i, err := n.Int64(); err == nil {
					addPort(fmt.Sprintf("%d", i))
				}
			}
		}
	}

	// appPort: int, string, or array
	if ap, ok := raw["appPort"]; ok {
		// Try number
		var num json.Number
		if err := json.Unmarshal(ap, &num); err == nil {
			if i, err := num.Int64(); err == nil {
				addPort(fmt.Sprintf("%d", i))
			}
		} else {
			// Try string
			var s string
			if err := json.Unmarshal(ap, &s); err == nil {
				addPort(s)
			} else {
				// Try array
				var arr []json.RawMessage
				if err := json.Unmarshal(ap, &arr); err == nil {
					for _, elem := range arr {
						var n json.Number
						if err := json.Unmarshal(elem, &n); err == nil {
							if i, err := n.Int64(); err == nil {
								addPort(fmt.Sprintf("%d", i))
							}
						} else {
							var str string
							if err := json.Unmarshal(elem, &str); err == nil {
								addPort(str)
							}
						}
					}
				}
			}
		}
	}

	return ports
}

// ResolvePort converts a port string to host:container format.
// If the port is already in host:container format, it's returned as-is.
// If it's a bare number (e.g. "3000"), it tries host=3000 first, then increments
// the host port until an available one is found.
func ResolvePort(port string) string {
	if strings.Contains(port, ":") {
		return port
	}
	containerPort, err := strconv.Atoi(port)
	if err != nil {
		return port
	}
	hostPort := containerPort
	for i := 0; i < 100; i++ {
		ln, err := net.Listen("tcp", fmt.Sprintf(":%d", hostPort))
		if err == nil {
			_ = ln.Close()
			if hostPort != containerPort {
				ui.PrintWarn("Port remapped", fmt.Sprintf("%d → %d", containerPort, hostPort))
			}
			return fmt.Sprintf("%d:%d", hostPort, containerPort)
		}
		hostPort++
	}
	// Give up, let Docker decide
	ui.PrintWarn("No host port available", fmt.Sprintf("%d", containerPort))
	return port
}

// ResolveAllPorts resolves bare port numbers to host:container format.
func ResolveAllPorts(ports []string) []string {
	resolved := make([]string, len(ports))
	for i, p := range ports {
		resolved[i] = ResolvePort(p)
	}
	return resolved
}
