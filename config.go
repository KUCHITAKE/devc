package main

import (
	"os"
	"path/filepath"
)

const dotfilesDir = "/opt/devc-dotfiles"

const additionalFeatures = `{
  "ghcr.io/duduribeiro/devcontainer-features/neovim:1": { "version": "nightly" },
  "ghcr.io/anthropics/devcontainer-features/claude-code:1": {},
  "ghcr.io/jungaretti/features/ripgrep:1": {},
  "ghcr.io/devcontainers/features/github-cli:1": {}
}`

type hostMount struct {
	source string
	target string
}

func hostMounts() []hostMount {
	home := os.Getenv("HOME")
	return []hostMount{
		{filepath.Join(home, ".config/nvim"), dotfilesDir + "/config-nvim"},
		{filepath.Join(home, ".claude"), dotfilesDir + "/claude"},
		{filepath.Join(home, ".claude.json"), dotfilesDir + "/claude.json"},
		{filepath.Join(home, ".ssh"), dotfilesDir + "/ssh"},
		{"/tmp/devc-credentials", "/tmp/devc-credentials"},
		// Obsidian vault - MCP config references this absolute path
		{filepath.Join(home, "work"), filepath.Join(home, "work")},
	}
}
