package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"
	"sync"

	"github.com/charmbracelet/huh/spinner"
	"github.com/charmbracelet/lipgloss"
	"github.com/mattn/go-isatty"
	"golang.org/x/term"
)

var isTTY = isatty.IsTerminal(os.Stderr.Fd()) || isatty.IsCygwinTerminal(os.Stderr.Fd())

const msgWidth = 32

// TTY symbols
var (
	symbolSuccess  = lipgloss.NewStyle().Foreground(lipgloss.Color("#04B575")).Render("✓")
	symbolProgress = lipgloss.NewStyle().Foreground(lipgloss.Color("#00BFFF")).Render("▸")
	symbolWarn     = lipgloss.NewStyle().Foreground(lipgloss.Color("#FFD700")).Render("!")
	symbolError    = lipgloss.NewStyle().Foreground(lipgloss.Color("#FF5050")).Render("✗")
	detailStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("240"))
)

func printLine(symbol, msg, detail string) {
	padded := msg + strings.Repeat(" ", max(0, msgWidth-len(msg)))
	if detail != "" {
		detail = detailStyle.Render(detail)
	}
	fmt.Fprintf(os.Stderr, "%s %s %s\n", symbol, padded, detail)
}

func printLinePlain(tag, msg, detail string) {
	if detail != "" {
		fmt.Fprintf(os.Stderr, "%s %s  %s\n", tag, msg, detail)
	} else {
		fmt.Fprintf(os.Stderr, "%s %s\n", tag, msg)
	}
}

func printDone(msg, detail string) {
	if isTTY {
		printLine(symbolSuccess, msg, detail)
	} else {
		printLinePlain("[ok]", msg, detail)
	}
}

func printProgress(msg, detail string) {
	if isTTY {
		printLine(symbolProgress, msg, detail)
	} else {
		printLinePlain("[..]", msg, detail)
	}
}

func printWarn(msg, detail string) {
	if isTTY {
		printLine(symbolWarn, msg, detail)
	} else {
		printLinePlain("[!!]", msg, detail)
	}
}

func printError(msg, detail string) {
	if isTTY {
		printLine(symbolError, msg, detail)
	} else {
		printLinePlain("[ERR]", msg, detail)
	}
}

// printDetail prints an indented line with no symbol (sub-step).
func printDetail(msg, detail string) {
	if isTTY {
		padded := msg + strings.Repeat(" ", max(0, msgWidth-len(msg)))
		if detail != "" {
			detail = detailStyle.Render(detail)
		}
		fmt.Fprintf(os.Stderr, "  %s %s\n", padded, detail)
	} else {
		printLinePlain("    ", msg, detail)
	}
}

// runWithSpinner runs fn while showing a spinner on the same line as msg.
// On success it replaces the spinner line with a ✓ done line.
func runWithSpinner(msg, detail string, fn func() error) error {
	if !isTTY {
		printLinePlain("[..]", msg, detail)
		return fn()
	}

	// Print progress line, then run with huh spinner
	// The spinner replaces itself, so we just show progress before and done after
	var fnErr error
	if err := spinner.New().
		Title(msg + "...").
		Action(func() { fnErr = fn() }).
		Run(); err != nil {
		return err
	}
	return fnErr
}

// ansiRe matches ANSI escape sequences (CSI, OSC) and carriage returns.
var ansiRe = regexp.MustCompile(`\x1b\[[0-9;?]*[A-Za-z]|\x1b\][^\x07]*\x07|\x1b[()][AB012]|\r`)

// stripANSI removes ANSI escape sequences from a string.
func stripANSI(s string) string {
	return ansiRe.ReplaceAllString(s, "")
}

// tailRenderer displays the last N lines of output in dim style,
// updating in place using ANSI escape codes. It implements io.Writer.
type tailRenderer struct {
	mu        sync.Mutex
	buf       []byte   // partial line buffer
	lines     []string // last maxLines complete lines
	maxLines  int
	displayed int // lines currently rendered on terminal
	width     int // terminal width for truncation
}

func newTailRenderer(maxLines int) *tailRenderer {
	width := 80
	if isTTY {
		if w, _, err := term.GetSize(int(os.Stderr.Fd())); err == nil && w > 0 {
			width = w
		}
	}
	return &tailRenderer{maxLines: maxLines, width: width}
}

// Write implements io.Writer. It buffers input, extracts complete lines,
// and updates the tail display.
func (t *tailRenderer) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()

	t.buf = append(t.buf, p...)
	for {
		idx := bytes.IndexByte(t.buf, '\n')
		if idx < 0 {
			break
		}
		line := stripANSI(strings.TrimRight(string(t.buf[:idx]), "\r"))
		t.buf = t.buf[idx+1:]
		if line == "" {
			continue
		}
		t.lines = append(t.lines, line)
		if len(t.lines) > t.maxLines {
			t.lines = t.lines[1:]
		}
	}

	t.render()
	return len(p), nil
}

// render redraws the tail lines on the terminal.
// Must be called with t.mu held.
func (t *tailRenderer) render() {
	if !isTTY || len(t.lines) == 0 {
		return
	}

	// Erase previously displayed lines (move up + clear, bottom to top)
	for i := 0; i < t.displayed; i++ {
		fmt.Fprint(os.Stderr, "\x1b[A\x1b[2K")
	}

	// Write current tail lines
	t.displayed = len(t.lines)
	const prefix = "  │ "
	maxContent := t.width - len(prefix) - 1
	for _, line := range t.lines {
		if maxContent > 0 && len(line) > maxContent {
			line = line[:maxContent-3] + "..."
		}
		fmt.Fprintf(os.Stderr, "\r%s\n", detailStyle.Render(prefix+line))
	}
}

// Clear removes the tail display from the terminal.
func (t *tailRenderer) Clear() {
	t.mu.Lock()
	defer t.mu.Unlock()

	if !isTTY || t.displayed == 0 {
		return
	}

	for i := 0; i < t.displayed; i++ {
		fmt.Fprint(os.Stderr, "\x1b[A\x1b[2K")
	}
	t.displayed = 0
}

// dockerStreamMsg represents a Docker NDJSON stream message.
type dockerStreamMsg struct {
	Stream      string `json:"stream"`
	Status      string `json:"status"`
	Error       string `json:"error"`
	ErrorDetail *struct {
		Message string `json:"message"`
	} `json:"errorDetail"`
}

// drainDockerOutput reads Docker NDJSON output, discarding stream/status lines.
// If an error is detected in the stream, it returns an error containing the
// last 20 lines of output for debugging.
func drainDockerOutput(r io.Reader) error {
	scanner := bufio.NewScanner(r)
	// Docker build output can have long lines
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	var recent []string
	const keepLines = 20

	for scanner.Scan() {
		line := scanner.Text()

		// Keep recent lines for error context
		recent = append(recent, line)
		if len(recent) > keepLines {
			recent = recent[1:]
		}

		var msg dockerStreamMsg
		if err := json.Unmarshal([]byte(line), &msg); err != nil {
			continue
		}
		if msg.Error != "" {
			return fmt.Errorf("docker error: %s\n%s", msg.Error, strings.Join(recent, "\n"))
		}
		if msg.ErrorDetail != nil && msg.ErrorDetail.Message != "" {
			return fmt.Errorf("docker error: %s\n%s", msg.ErrorDetail.Message, strings.Join(recent, "\n"))
		}
	}
	return scanner.Err()
}
