package container

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/closer/devc/internal/config"
	"github.com/closer/devc/internal/docker"
	"github.com/closer/devc/internal/meta"
	"github.com/closer/devc/internal/ui"
	dockercontainer "github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/mount"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/go-connections/nat"
)

type LifecycleCommand struct {
	Name    string
	Command []string
}

func ParseLifecycleHook(raw json.RawMessage) []LifecycleCommand {
	if raw == nil {
		return nil
	}

	// Try string: "echo hello" → ["sh", "-c", "echo hello"]
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return []LifecycleCommand{{Command: []string{"sh", "-c", s}}}
	}

	// Try array: ["echo", "hello"] → ["echo", "hello"]
	var arr []string
	if err := json.Unmarshal(raw, &arr); err == nil {
		return []LifecycleCommand{{Command: arr}}
	}

	// Try map: {"name": "cmd" | ["cmd"]}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err == nil {
		// Sort keys for deterministic order
		keys := make([]string, 0, len(m))
		for k := range m {
			keys = append(keys, k)
		}
		sort.Strings(keys)

		var cmds []LifecycleCommand
		for _, k := range keys {
			v := m[k]
			// Try as string
			var str string
			if err := json.Unmarshal(v, &str); err == nil {
				cmds = append(cmds, LifecycleCommand{Name: k, Command: []string{"sh", "-c", str}})
				continue
			}
			// Try as array
			var a []string
			if err := json.Unmarshal(v, &a); err == nil {
				cmds = append(cmds, LifecycleCommand{Name: k, Command: a})
			}
		}
		return cmds
	}

	return nil
}

func BuildContainerMounts(ws config.Workspace, wsFolder string, mounts []config.HostMount) []mount.Mount {
	result := []mount.Mount{
		{
			Type:   mount.TypeBind,
			Source: ws.Dir,
			Target: wsFolder,
		},
	}
	for _, m := range mounts {
		if _, err := os.Stat(m.Source); err == nil {
			result = append(result, mount.Mount{
				Type:   mount.TypeBind,
				Source: m.Source,
				Target: m.Target,
			})
		}
	}
	return result
}

func BuildPortBindings(ports []string) (nat.PortSet, nat.PortMap, error) {
	portSet := nat.PortSet{}
	portMap := nat.PortMap{}

	for _, p := range ports {
		parts := strings.SplitN(p, ":", 2)
		if len(parts) != 2 {
			return nil, nil, fmt.Errorf("invalid port format %q, expected host:container", p)
		}
		hostPort := parts[0]
		containerPort := parts[1]

		natPort := nat.Port(containerPort + "/tcp")
		portSet[natPort] = struct{}{}
		portMap[natPort] = []nat.PortBinding{
			{HostPort: hostPort},
		}
	}

	return portSet, portMap, nil
}

func CreateAndStartContainer(ctx context.Context, ws config.Workspace, cfg *config.DevcontainerConfig, imageTag string, ports []string, mounts []config.HostMount) (string, error) {
	cli, err := docker.GetClient()
	if err != nil {
		return "", fmt.Errorf("docker client: %w", err)
	}

	// Build env vars
	var envList []string
	for k, v := range cfg.ContainerEnv {
		envList = append(envList, k+"="+v)
	}
	envList = append(envList, meta.DevcContainerEnv+"=1")
	sort.Strings(envList)

	// Build port bindings
	portSet, portMap, err := BuildPortBindings(ports)
	if err != nil {
		return "", fmt.Errorf("port bindings: %w", err)
	}

	// Build mounts
	containerMounts := BuildContainerMounts(ws, cfg.RemoteWorkspaceFolder, mounts)

	containerCfg := &dockercontainer.Config{
		Image:      imageTag,
		Cmd:        []string{"sleep", "infinity"},
		Env:        envList,
		WorkingDir: cfg.RemoteWorkspaceFolder,
		Labels: map[string]string{
			"devcontainer.local_folder": ws.Dir,
		},
		ExposedPorts: portSet,
	}

	hostCfg := &dockercontainer.HostConfig{
		Mounts:       containerMounts,
		PortBindings: portMap,
	}

	name := "devc-" + ws.ID

	resp, err := cli.ContainerCreate(ctx, containerCfg, hostCfg, &network.NetworkingConfig{}, nil, name)
	if err != nil {
		return "", fmt.Errorf("container create: %w", err)
	}

	if err := cli.ContainerStart(ctx, resp.ID, dockercontainer.StartOptions{}); err != nil {
		return "", fmt.Errorf("container start: %w", err)
	}

	ui.PrintDone("Container started", name+" ("+resp.ID[:8]+")")
	return resp.ID, nil
}

func RunLifecycleHooks(ctx context.Context, containerID, user string, hooks ...[]LifecycleCommand) error {
	for _, hookSet := range hooks {
		for _, lc := range hookSet {
			label := strings.Join(lc.Command, " ")
			if lc.Name != "" {
				label = lc.Name + ": " + label
			}
			ui.PrintProgress("Running hook", label)
			if err := docker.ExecTail(ctx, containerID, user, lc.Command); err != nil {
				ui.PrintWarn("Hook failed", label+": "+err.Error())
			} else {
				ui.PrintDone("Hook completed", label)
			}
		}
	}
	return nil
}
