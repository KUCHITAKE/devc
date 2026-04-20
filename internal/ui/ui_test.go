package ui

import (
	"os"
	"strings"
	"testing"
)

func TestPrintFunctions_NonTTY(t *testing.T) {
	// Force non-TTY mode
	origTTY := IsTTY
	IsTTY = false
	defer func() { IsTTY = origTTY }()

	tests := []struct {
		name   string
		fn     func(string, string)
		msg    string
		detail string
		want   string
	}{
		{"done", PrintDone, "Image built", "tag:abc", "[ok] Image built  tag:abc\n"},
		{"done_no_detail", PrintDone, "Ready", "", "[ok] Ready\n"},
		{"progress", PrintProgress, "Building", "proj", "[..] Building  proj\n"},
		{"warn", PrintWarn, "Port remapped", "3000", "[!!] Port remapped  3000\n"},
		{"error", PrintError, "Build failed", "", "[ERR] Build failed\n"},
		{"detail", PrintDetail, "Pulling feature", "nvim", "     Pulling feature  nvim\n"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r, w, _ := os.Pipe()
			origStderr := os.Stderr
			os.Stderr = w

			tt.fn(tt.msg, tt.detail)

			_ = w.Close()
			os.Stderr = origStderr

			buf := make([]byte, 1024)
			n, _ := r.Read(buf)
			got := string(buf[:n])

			if got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestDrainDockerOutput_Success(t *testing.T) {
	lines := []string{
		`{"stream":"Step 1/3 : FROM ubuntu\n"}`,
		`{"status":"Pulling from library/ubuntu"}`,
		`{"stream":"Step 2/3 : RUN echo hello\n"}`,
		`{"stream":"Step 3/3 : CMD bash\n"}`,
	}
	r := strings.NewReader(strings.Join(lines, "\n"))
	if err := DrainDockerOutput(r); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestDrainDockerOutput_Error(t *testing.T) {
	lines := []string{
		`{"stream":"Step 1/2 : FROM ubuntu\n"}`,
		`{"error":"something went wrong","errorDetail":{"message":"something went wrong"}}`,
	}
	r := strings.NewReader(strings.Join(lines, "\n"))
	err := DrainDockerOutput(r)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "something went wrong") {
		t.Errorf("error should contain docker error message, got: %v", err)
	}
}

func TestDrainDockerOutput_EmptyInput(t *testing.T) {
	r := strings.NewReader("")
	if err := DrainDockerOutput(r); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}
