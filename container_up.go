package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/mount"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/go-connections/nat"
)

type lifecycleCommand struct {
	Name    string
	Command []string
}

func parseLifecycleHook(raw json.RawMessage) []lifecycleCommand {
	if raw == nil {
		return nil
	}

	// Try string: "echo hello" → ["sh", "-c", "echo hello"]
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return []lifecycleCommand{{Command: []string{"sh", "-c", s}}}
	}

	// Try array: ["echo", "hello"] → ["echo", "hello"]
	var arr []string
	if err := json.Unmarshal(raw, &arr); err == nil {
		return []lifecycleCommand{{Command: arr}}
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

		var cmds []lifecycleCommand
		for _, k := range keys {
			v := m[k]
			// Try as string
			var str string
			if err := json.Unmarshal(v, &str); err == nil {
				cmds = append(cmds, lifecycleCommand{Name: k, Command: []string{"sh", "-c", str}})
				continue
			}
			// Try as array
			var a []string
			if err := json.Unmarshal(v, &a); err == nil {
				cmds = append(cmds, lifecycleCommand{Name: k, Command: a})
			}
		}
		return cmds
	}

	return nil
}

func buildContainerMounts(ws workspace, wsFolder string, mounts []hostMount) []mount.Mount {
	result := []mount.Mount{
		{
			Type:   mount.TypeBind,
			Source: ws.dir,
			Target: wsFolder,
		},
	}
	for _, m := range mounts {
		if _, err := os.Stat(m.source); err == nil {
			result = append(result, mount.Mount{
				Type:   mount.TypeBind,
				Source: m.source,
				Target: m.target,
			})
		}
	}
	return result
}

func buildPortBindings(ports []string) (nat.PortSet, nat.PortMap, error) {
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

func createAndStartContainer(ctx context.Context, ws workspace, cfg *devcontainerConfig, imageTag string, ports []string, mounts []hostMount) (string, error) {
	cli, err := getDockerClient()
	if err != nil {
		return "", fmt.Errorf("docker client: %w", err)
	}

	// Build env vars
	var envList []string
	for k, v := range cfg.ContainerEnv {
		envList = append(envList, k+"="+v)
	}
	sort.Strings(envList)

	// Build port bindings
	portSet, portMap, err := buildPortBindings(ports)
	if err != nil {
		return "", fmt.Errorf("port bindings: %w", err)
	}

	// Build mounts
	containerMounts := buildContainerMounts(ws, cfg.RemoteWorkspaceFolder, mounts)

	containerCfg := &container.Config{
		Image:      imageTag,
		Cmd:        []string{"sleep", "infinity"},
		Env:        envList,
		WorkingDir: cfg.RemoteWorkspaceFolder,
		Labels: map[string]string{
			"devcontainer.local_folder": ws.dir,
		},
		ExposedPorts: portSet,
	}

	hostCfg := &container.HostConfig{
		Mounts:       containerMounts,
		PortBindings: portMap,
	}

	name := "devc-" + ws.name

	resp, err := cli.ContainerCreate(ctx, containerCfg, hostCfg, &network.NetworkingConfig{}, nil, name)
	if err != nil {
		return "", fmt.Errorf("container create: %w", err)
	}

	if err := cli.ContainerStart(ctx, resp.ID, container.StartOptions{}); err != nil {
		return "", fmt.Errorf("container start: %w", err)
	}

	printDone("Container started", name+" ("+resp.ID[:8]+")")
	return resp.ID, nil
}

func runLifecycleHooks(ctx context.Context, containerID, user string, hooks ...[]lifecycleCommand) error {
	for _, hookSet := range hooks {
		for _, lc := range hookSet {
			label := strings.Join(lc.Command, " ")
			if lc.Name != "" {
				label = lc.Name + ": " + label
			}
			printProgress("Running hook", label)
			if err := containerExecTail(ctx, containerID, user, lc.Command); err != nil {
				printWarn("Hook failed", label+": "+err.Error())
			} else {
				printDone("Hook completed", label)
			}
		}
	}
	return nil
}
