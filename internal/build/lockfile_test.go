package build

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLockfileRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "devcontainer-lock.json")

	lf := &Lockfile{
		Features: map[string]FeatureLock{
			"ghcr.io/devcontainers/features/go:1": {
				Version:  "1",
				Resolved: "sha256:abc123",
			},
			"ghcr.io/devcontainers/features/node:18": {
				Version:  "18",
				Resolved: "sha256:def456",
			},
		},
	}

	if err := WriteLockfile(path, lf); err != nil {
		t.Fatalf("WriteLockfile: %v", err)
	}

	got, err := ReadLockfile(path)
	if err != nil {
		t.Fatalf("ReadLockfile: %v", err)
	}

	if len(got.Features) != 2 {
		t.Fatalf("features count = %d, want 2", len(got.Features))
	}

	goLock := got.Features["ghcr.io/devcontainers/features/go:1"]
	if goLock.Version != "1" {
		t.Fatalf("go version = %q, want %q", goLock.Version, "1")
	}
	if goLock.Resolved != "sha256:abc123" {
		t.Fatalf("go resolved = %q, want %q", goLock.Resolved, "sha256:abc123")
	}

	nodeLock := got.Features["ghcr.io/devcontainers/features/node:18"]
	if nodeLock.Resolved != "sha256:def456" {
		t.Fatalf("node resolved = %q, want %q", nodeLock.Resolved, "sha256:def456")
	}
}

func TestReadLockfile_NotFound(t *testing.T) {
	lf, err := ReadLockfile("/nonexistent/path/devcontainer-lock.json")
	if err != nil {
		t.Fatalf("ReadLockfile should not error for missing file: %v", err)
	}
	if lf != nil {
		t.Fatalf("expected nil lockfile for missing file, got %v", lf)
	}
}

func TestReadLockfile_InvalidJSON(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "devcontainer-lock.json")
	if err := os.WriteFile(path, []byte("not json"), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := ReadLockfile(path)
	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}
}

func TestLockfilePath(t *testing.T) {
	path := LockfilePath("/home/user/project")
	want := "/home/user/project/.devcontainer/devcontainer-lock.json"
	if path != want {
		t.Fatalf("LockfilePath = %q, want %q", path, want)
	}
}

func TestWriteLockfile_Deterministic(t *testing.T) {
	dir := t.TempDir()
	path1 := filepath.Join(dir, "lock1.json")
	path2 := filepath.Join(dir, "lock2.json")

	lf := &Lockfile{
		Features: map[string]FeatureLock{
			"ghcr.io/b-feature:1": {Version: "1", Resolved: "sha256:bbb"},
			"ghcr.io/a-feature:1": {Version: "1", Resolved: "sha256:aaa"},
		},
	}

	if err := WriteLockfile(path1, lf); err != nil {
		t.Fatal(err)
	}
	if err := WriteLockfile(path2, lf); err != nil {
		t.Fatal(err)
	}

	data1, _ := os.ReadFile(path1)
	data2, _ := os.ReadFile(path2)
	if string(data1) != string(data2) {
		t.Fatalf("lockfile output is not deterministic:\n%s\nvs\n%s", data1, data2)
	}

	// Verify keys are sorted (a before b)
	content := string(data1)
	aIdx := indexOf(content, "a-feature")
	bIdx := indexOf(content, "b-feature")
	if aIdx > bIdx {
		t.Fatal("keys should be sorted alphabetically")
	}
}

func indexOf(s, substr string) int {
	for i := 0; i+len(substr) <= len(s); i++ {
		if s[i:i+len(substr)] == substr {
			return i
		}
	}
	return -1
}
