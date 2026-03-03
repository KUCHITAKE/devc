package main

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/charmbracelet/log"
	"github.com/spf13/cobra"
)

var version = "dev"

var knownSubcommands = map[string]bool{
	"up": true, "down": true, "clean": true, "rebuild": true, "help": true,
}

// rewriteLegacyArgs provides backward compatibility with the bash script's
// argument parsing. It ensures a subcommand is always present.
func rewriteLegacyArgs(args []string) []string {
	if len(args) == 0 {
		return []string{"up"}
	}

	first := args[0]

	switch {
	case first == "-h" || first == "--help":
		return append([]string{"help"}, args[1:]...)
	case first == "-V":
		return []string{"--version"}
	case first == "--version":
		return args
	case first == "--clean":
		return append([]string{"clean"}, args[1:]...)
	case first == "--rebuild":
		return append([]string{"up", "--rebuild"}, args[1:]...)
	case knownSubcommands[first]:
		return args
	case strings.HasPrefix(first, "-"):
		// Unknown flag — assume it's for "up"
		return append([]string{"up"}, args...)
	default:
		// Bare path — treat as "up <path>"
		return append([]string{"up"}, args...)
	}
}

func buildRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:   "devc <command> [options] [workspace-dir]",
		Short: "Launch a devcontainer with Neovim, Claude Code, and ripgrep",
		Long: `devc launches a devcontainer with Neovim (nightly), Claude Code, and ripgrep.

Features are injected via --additional-features so existing devcontainer.json
configs work as-is. Ports from forwardPorts/appPort are auto-converted to runArgs.`,
		Example: `  devc ~/project
  devc up -p 3000:3000 -p 5173:5173 ~/project
  devc rebuild .
  devc down ~/project
  devc clean ~/project`,
		Version:       version,
		SilenceUsage:  true,
		SilenceErrors: true,
	}

	root.AddCommand(newUpCmd())
	root.AddCommand(newDownCmd())
	root.AddCommand(newCleanCmd())
	root.AddCommand(newRebuildCmd())

	return root
}

func main() {
	log.SetReportTimestamp(false)

	// Expand ~ in arguments (shell doesn't expand it in all contexts)
	for i, arg := range os.Args[1:] {
		if strings.HasPrefix(arg, "~/") {
			if home := os.Getenv("HOME"); home != "" {
				os.Args[i+1] = filepath.Join(home, arg[2:])
			}
		}
	}

	os.Args = append(os.Args[:1], rewriteLegacyArgs(os.Args[1:])...)

	if err := buildRootCmd().Execute(); err != nil {
		log.Error(err)
		os.Exit(1)
	}
}
