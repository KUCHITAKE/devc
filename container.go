package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
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
	ContainerID          string `json:"containerId"`
	RemoteUser           string `json:"remoteUser"`
	RemoteWorkspaceFolder string `json:"remoteWorkspaceFolder"`
}

func resolveWorkspace(dir string) (workspace, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return workspace{}, fmt.Errorf("resolve workspace: %w", err)
	}
	return workspace{dir: abs, name: filepath.Base(abs)}, nil
}

func extractCredentials() error {
	dir := "/tmp/devc-credentials"
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	// git user.name
	if out, err := exec.Command("git", "config", "--global", "user.name").Output(); err == nil {
		_ = os.WriteFile(filepath.Join(dir, "git-user-name"), bytes.TrimSpace(out), 0o644)
	}
	// git user.email
	if out, err := exec.Command("git", "config", "--global", "user.email").Output(); err == nil {
		_ = os.WriteFile(filepath.Join(dir, "git-user-email"), bytes.TrimSpace(out), 0o644)
	}
	// gh auth token
	if out, err := exec.Command("gh", "auth", "token").Output(); err == nil {
		_ = os.WriteFile(filepath.Join(dir, "gh-token"), bytes.TrimSpace(out), 0o644)
	}
	return nil
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
			ln.Close()
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

func buildMountArgs(mounts []hostMount) []string {
	var args []string
	for _, m := range mounts {
		if _, err := os.Stat(m.source); err == nil {
			args = append(args, "--mount", fmt.Sprintf("type=bind,source=%s,target=%s", m.source, m.target))
		}
	}
	return args
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

// isContainerRunning checks if a devcontainer is already running for the workspace.
func isContainerRunning(ws workspace) bool {
	out, err := exec.Command("docker", "ps", "-q",
		"--filter", "label=devcontainer.local_folder="+ws.dir,
	).Output()
	return err == nil && strings.TrimSpace(string(out)) != ""
}

// findContainerByWorkspace finds a devcontainer by its workspace folder label.
func findContainerByWorkspace(ws workspace) (string, error) {
	out, err := exec.Command("docker", "ps", "-aq",
		"--filter", "label=devcontainer.local_folder="+ws.dir,
	).Output()
	if err != nil {
		return "", fmt.Errorf("docker ps failed: %w", err)
	}
	id := strings.TrimSpace(string(out))
	if id == "" {
		return "", fmt.Errorf("no devcontainer found for %s", ws.dir)
	}
	// If multiple containers match (shouldn't happen for non-compose), take the first.
	if lines := strings.Split(id, "\n"); len(lines) > 1 {
		id = lines[0]
	}
	return id, nil
}

func setupContainer(containerID, remoteUser string) error {
	dockerExec := func(args ...string) error {
		cmdArgs := append([]string{"exec", "-u", remoteUser, containerID}, args...)
		cmd := exec.Command("docker", cmdArgs...)
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		return cmd.Run()
	}
	dockerExecOutput := func(args ...string) (string, error) {
		cmdArgs := append([]string{"exec", "-u", remoteUser, containerID}, args...)
		out, err := exec.Command("docker", cmdArgs...).Output()
		return strings.TrimSpace(string(out)), err
	}

	// Discover remote home
	remoteHome, err := dockerExecOutput("sh", "-c", "echo $HOME")
	if err != nil {
		return fmt.Errorf("get remote home: %w", err)
	}
	log.Info("Remote home", "path", remoteHome)

	// Create .config and symlinks
	_ = dockerExec("mkdir", "-p", remoteHome+"/.config")
	_ = dockerExec("ln", "-sfn", dotfilesDir+"/config-nvim", remoteHome+"/.config/nvim")
	_ = dockerExec("ln", "-sfn", dotfilesDir+"/claude", remoteHome+"/.claude")
	_ = dockerExec("ln", "-sfn", dotfilesDir+"/claude.json", remoteHome+"/.claude.json")
	_ = dockerExec("ln", "-sfn", dotfilesDir+"/ssh", remoteHome+"/.ssh")

	// Git config (non-fatal)
	if data, err := os.ReadFile("/tmp/devc-credentials/git-user-name"); err == nil {
		_ = dockerExec("git", "config", "--global", "user.name", strings.TrimSpace(string(data)))
	}
	if data, err := os.ReadFile("/tmp/devc-credentials/git-user-email"); err == nil {
		_ = dockerExec("git", "config", "--global", "user.email", strings.TrimSpace(string(data)))
	}
	// gh auth (non-fatal)
	if _, err := os.Stat("/tmp/devc-credentials/gh-token"); err == nil {
		_ = dockerExec("bash", "-c", "gh auth login --with-token < /tmp/devc-credentials/gh-token && gh auth setup-git")
	}

	return nil
}
