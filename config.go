package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const dotfilesDir = "/opt/devc-dotfiles"

type hostMount struct {
	source string
	target string
}

type userConfig struct {
	Features map[string]map[string]interface{} `json:"features"`
	Dotfiles []string                          `json:"dotfiles"`
	Mounts   []mountEntry                      `json:"mounts"`
}

type mountEntry struct {
	Source string `json:"source"`
	Target string `json:"target"`
}

// loadUserConfig reads ~/.config/devc/config.json. Returns an empty config if
// the file does not exist.
func loadUserConfig() (*userConfig, error) {
	configDir := os.Getenv("XDG_CONFIG_HOME")
	if configDir == "" {
		home := os.Getenv("HOME")
		configDir = filepath.Join(home, ".config")
	}
	path := filepath.Join(configDir, "devc", "config.json")

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return &userConfig{}, nil
		}
		return nil, err
	}

	var cfg userConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	return &cfg, nil
}

// buildHostMounts builds the list of bind mounts from user config (dotfiles +
// mounts) plus the built-in credentials and daemon socket mounts.
func buildHostMounts(ucfg *userConfig, wsName string) []hostMount {
	var mounts []hostMount

	// Dotfile mounts: host path → staging dir
	for _, df := range ucfg.Dotfiles {
		src := expandHome(df)
		rel := dotfileRelPath(df)
		target := filepath.Join(dotfilesDir, rel)
		mounts = append(mounts, hostMount{source: src, target: target})
	}

	// Extra user mounts
	for _, m := range ucfg.Mounts {
		mounts = append(mounts, hostMount{
			source: expandHome(m.Source),
			target: expandHome(m.Target),
		})
	}

	// Built-in credentials mount (always present)
	mounts = append(mounts, hostMount{
		source: "/tmp/devc-credentials",
		target: "/tmp/devc-credentials",
	})

	// Daemon socket directory mount
	sockDir := daemonSockDir(wsName)
	_ = os.MkdirAll(sockDir, 0o755)
	mounts = append(mounts, hostMount{
		source: sockDir,
		target: devcMetaDir,
	})

	return mounts
}

// expandHome replaces a leading "~/" with the user's home directory.
// Absolute paths are returned unchanged.
func expandHome(path string) string {
	if strings.HasPrefix(path, "~/") {
		home := os.Getenv("HOME")
		return filepath.Join(home, path[2:])
	}
	return path
}

// dotfileRelPath converts a dotfile path to a relative path suitable for the
// staging directory. "~/.config/nvim" → ".config/nvim", "~/.ssh" → ".ssh".
// Absolute paths without "~/" use the base name.
func dotfileRelPath(path string) string {
	if strings.HasPrefix(path, "~/") {
		return path[2:]
	}
	return filepath.Base(path)
}
