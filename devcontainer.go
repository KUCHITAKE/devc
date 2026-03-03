package main

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/charmbracelet/log"
)

type workspace struct {
	dir  string
	name string
}

type upResult struct {
	ContainerID           string `json:"containerId"`
	RemoteUser            string `json:"remoteUser"`
	RemoteWorkspaceFolder string `json:"remoteWorkspaceFolder"`
}

func resolveWorkspace(dir string) (workspace, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return workspace{}, fmt.Errorf("resolve workspace: %w", err)
	}
	return workspace{dir: abs, name: filepath.Base(abs)}, nil
}

func ensureDevcontainerJSON(ws workspace) error {
	dcDir := filepath.Join(ws.dir, ".devcontainer")
	dcJSON := filepath.Join(dcDir, "devcontainer.json")
	if _, err := os.Stat(dcJSON); err == nil {
		return nil
	}
	if err := os.MkdirAll(dcDir, 0o755); err != nil {
		return err
	}
	content := fmt.Sprintf("{\n  \"name\": %q,\n  \"image\": \"mcr.microsoft.com/devcontainers/base:ubuntu\"\n}\n", ws.name)
	log.Info("Generated", "file", dcJSON)
	return os.WriteFile(dcJSON, []byte(content), 0o644)
}

func readDevcontainerJSON(ws workspace) (map[string]json.RawMessage, error) {
	dcJSON := filepath.Join(ws.dir, ".devcontainer", "devcontainer.json")
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

func composeFiles(ws workspace, raw map[string]json.RawMessage) []string {
	dcf, ok := raw["dockerComposeFile"]
	if !ok {
		return nil
	}
	dcDir := filepath.Join(ws.dir, ".devcontainer")

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

func collectPorts(raw map[string]json.RawMessage, cliPorts []string) []string {
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

// resolvePort converts a port string to host:container format.
// If the port is already in host:container format, it's returned as-is.
// If it's a bare number (e.g. "3000"), it tries host=3000 first, then increments
// the host port until an available one is found.
func resolvePort(port string) string {
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
				log.Warn("Port in use, remapped", "container", containerPort, "host", hostPort)
			}
			return fmt.Sprintf("%d:%d", hostPort, containerPort)
		}
		hostPort++
	}
	// Give up, let Docker decide
	log.Warn("Could not find available host port", "container", containerPort)
	return port
}

func mergedConfigPath(ws workspace, raw map[string]json.RawMessage, ports []string) (string, error) {
	if len(ports) == 0 {
		return "", nil
	}

	// Resolve bare ports to host:container format
	resolved := make([]string, len(ports))
	for i, p := range ports {
		resolved[i] = resolvePort(p)
	}

	log.Info("Ports", "ports", strings.Join(resolved, ", "))

	var portArgs []string
	for _, p := range resolved {
		portArgs = append(portArgs, "-p", p)
	}

	// Merge with existing runArgs
	var existingRunArgs []string
	if ra, ok := raw["runArgs"]; ok {
		_ = json.Unmarshal(ra, &existingRunArgs)
	}
	merged := append(existingRunArgs, portArgs...)

	mergedJSON, err := json.Marshal(merged)
	if err != nil {
		return "", err
	}
	raw["runArgs"] = json.RawMessage(mergedJSON)

	out, err := json.MarshalIndent(raw, "", "  ")
	if err != nil {
		return "", err
	}

	configPath := filepath.Join(ws.dir, ".devcontainer", ".devcontainer.json")
	if err := os.WriteFile(configPath, out, 0o644); err != nil {
		return "", err
	}
	return configPath, nil
}

func parseUpOutput(out []byte) (upResult, error) {
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	// Reverse scan for the JSON line containing containerId
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		if line == "" || line[0] != '{' {
			continue
		}
		var result upResult
		if err := json.Unmarshal([]byte(line), &result); err == nil && result.ContainerID != "" {
			return result, nil
		}
	}
	return upResult{}, fmt.Errorf("no valid JSON with containerId found in devcontainer up output")
}
