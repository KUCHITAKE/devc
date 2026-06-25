package main

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/closer/devc/internal/meta"
	"github.com/closer/devc/internal/ui"
	"github.com/spf13/cobra"
)

var version = "dev"

var knownSubcommands = map[string]bool{
	"up": true, "down": true, "clean": true, "rebuild": true, "ls": true, "list": true, "ps": true, "exec": true, "help": true,
}

// rewriteLegacyArgs translates the bash script's flag-style aliases into
// subcommands. A bare invocation (no args) still defaults to "up" so that
// running devc inside a project launches it, but naming a workspace requires
// the explicit "up" subcommand (see unknownCommandHint).
func rewriteLegacyArgs(args []string) []string {
	if len(args) == 0 {
		return []string{"up"}
	}

	switch args[0] {
	case "-h", "--help":
		return append([]string{"help"}, args[1:]...)
	case "-V":
		return []string{"--version"}
	case "--clean":
		return append([]string{"clean"}, args[1:]...)
	case "--rebuild":
		return append([]string{"up", "--rebuild"}, args[1:]...)
	}

	return args
}

// unknownCommandHint detects the old bare-path form (e.g. "devc ~/project")
// and returns guidance toward the explicit "devc up <path>" form. It returns
// ok=false for flags, known subcommands, and empty args, leaving those to
// cobra.
func unknownCommandHint(args []string) (detail string, ok bool) {
	if len(args) == 0 {
		return "", false
	}
	first := args[0]
	if strings.HasPrefix(first, "-") || knownSubcommands[first] {
		return "", false
	}
	return "To launch a devcontainer, run: devc up " + first, true
}

func buildRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:   "devc <command> [options] [workspace-dir]",
		Short: "Launch and manage devcontainers",
		Long: `devc launches devcontainers using the Docker Engine API.
User-specific features and dotfiles are configured in ~/.config/devc/config.json.
No devcontainer CLI or Node.js required. Ports from forwardPorts/appPort are
automatically published.`,
		Example: `  devc up ~/project
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
	root.AddCommand(newLsCmd())
	root.AddCommand(newExecCmd())

	return root
}

func main() {
	// Inside a devc container, use the internal command set
	if meta.IsInsideContainer() {
		if err := buildInternalRootCmd().Execute(); err != nil {
			ui.PrintError(err.Error(), "")
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

	if detail, ok := unknownCommandHint(os.Args[1:]); ok {
		ui.PrintError("unknown command \""+os.Args[1]+"\" for \"devc\"", detail)
		os.Exit(1)
	}

	if err := buildRootCmd().Execute(); err != nil {
		ui.PrintError(err.Error(), "")
		os.Exit(1)
	}
}
