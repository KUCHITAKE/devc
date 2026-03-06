package main

import (
	"os"
	"path/filepath"
	"strings"

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
		Short: "Launch and manage devcontainers",
		Long: `devc launches devcontainers using the Docker Engine API.
User-specific features and dotfiles are configured in ~/.config/devc/config.json.
No devcontainer CLI or Node.js required. Ports from forwardPorts/appPort are
automatically published.`,
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
	// Inside a devc container, use the internal command set
	if isInsideContainer() {
		if err := buildInternalRootCmd().Execute(); err != nil {
			printError(err.Error(), "")
			os.Exit(1)
		}
		return
	}

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
		printError(err.Error(), "")
		os.Exit(1)
	}
}
