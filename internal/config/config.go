package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const DotfilesDir = "/opt/devc-dotfiles"
const DevcMetaDir = "/opt/devc"

type HostMount struct {
	Source string
	Target string
}

type UserConfig struct {
	Features map[string]map[string]interface{} `json:"features"`
	Dotfiles []string                          `json:"dotfiles"`
	Mounts   []MountEntry                      `json:"mounts"`
}

type MountEntry struct {
	Source string `json:"source"`
	Target string `json:"target"`
}

// LoadUserConfig reads ~/.config/devc/config.json. Returns an empty config if
// the file does not exist.
func LoadUserConfig() (*UserConfig, error) {
	configDir := os.Getenv("XDG_CONFIG_HOME")
	if configDir == "" {
		home := os.Getenv("HOME")
		configDir = filepath.Join(home, ".config")
	}
	path := filepath.Join(configDir, "devc", "config.json")

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return &UserConfig{}, nil
		}
		return nil, err
	}

	var cfg UserConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	return &cfg, nil
}

// BuildHostMounts builds the list of bind mounts from user config (dotfiles +
// mounts) plus the built-in credentials and daemon socket mounts.
func BuildHostMounts(ucfg *UserConfig, wsName string, daemonSockDir string) []HostMount {
	var mounts []HostMount

	// Dotfile mounts: host path → staging dir
	for _, df := range ucfg.Dotfiles {
		src := ExpandHome(df)
		rel := DotfileRelPath(df)
		target := filepath.Join(DotfilesDir, rel)
		mounts = append(mounts, HostMount{Source: src, Target: target})
	}

	// Extra user mounts
	for _, m := range ucfg.Mounts {
		mounts = append(mounts, HostMount{
			Source: ExpandHome(m.Source),
			Target: ExpandHome(m.Target),
		})
	}

	// Built-in credentials mount (always present)
	mounts = append(mounts, HostMount{
		Source: "/tmp/devc-credentials",
		Target: "/tmp/devc-credentials",
	})

	// Daemon socket directory mount
	_ = os.MkdirAll(daemonSockDir, 0o755)
	mounts = append(mounts, HostMount{
		Source: daemonSockDir,
		Target: DevcMetaDir,
	})

	return mounts
}

// ExpandHome replaces a leading "~/" with the user's home directory.
// Absolute paths are returned unchanged.
func ExpandHome(path string) string {
	if strings.HasPrefix(path, "~/") {
		home := os.Getenv("HOME")
		return filepath.Join(home, path[2:])
	}
	return path
}

// DotfileRelPath converts a dotfile path to a relative path suitable for the
// staging directory. "~/.config/nvim" → ".config/nvim", "~/.ssh" → ".ssh".
// Absolute paths without "~/" use the base name.
func DotfileRelPath(path string) string {
	if strings.HasPrefix(path, "~/") {
		return path[2:]
	}
	return filepath.Base(path)
}
