package docker

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func fixedSource(name, value string) credentialSource {
	return credentialSource{name: name, read: func() ([]byte, error) {
		return []byte(value), nil
	}}
}

func unavailableSource(name string) credentialSource {
	return credentialSource{name: name, read: func() ([]byte, error) {
		return nil, errors.New("not available")
	}}
}

func TestExtractCredentialsWritesSources(t *testing.T) {
	dir := t.TempDir()

	err := extractCredentials(dir, []credentialSource{
		fixedSource("git-user-name", "Alice\n"),
		fixedSource("gh-token", "gho_secret\n"),
	})
	if err != nil {
		t.Fatalf("extractCredentials() = %v", err)
	}

	got, err := os.ReadFile(filepath.Join(dir, "git-user-name"))
	if err != nil {
		t.Fatalf("read git-user-name: %v", err)
	}
	if string(got) != "Alice" {
		t.Errorf("git-user-name = %q, want %q (trimmed)", got, "Alice")
	}

	got, err = os.ReadFile(filepath.Join(dir, "gh-token"))
	if err != nil {
		t.Fatalf("read gh-token: %v", err)
	}
	if string(got) != "gho_secret" {
		t.Errorf("gh-token = %q, want %q (trimmed)", got, "gho_secret")
	}
}

func TestExtractCredentialsCreatesDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "sub")

	if err := extractCredentials(dir, []credentialSource{fixedSource("gh-token", "x")}); err != nil {
		t.Fatalf("extractCredentials() = %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "gh-token")); err != nil {
		t.Errorf("gh-token not written: %v", err)
	}
}

func TestExtractCredentialsSkipsUnavailableSources(t *testing.T) {
	// A missing credential (e.g. gh not installed or not logged in) is not an
	// error — the file is simply absent and container setup skips that step.
	dir := t.TempDir()

	err := extractCredentials(dir, []credentialSource{
		unavailableSource("gh-token"),
		fixedSource("git-user-name", "Alice"),
	})
	if err != nil {
		t.Fatalf("extractCredentials() = %v", err)
	}

	if _, err := os.Stat(filepath.Join(dir, "gh-token")); !os.IsNotExist(err) {
		t.Errorf("gh-token should not exist, stat err = %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "git-user-name")); err != nil {
		t.Errorf("git-user-name should exist: %v", err)
	}
}

func TestExtractCredentialsRemovesStaleFiles(t *testing.T) {
	// A credential that existed on a previous run but is no longer available
	// must not leak into the container.
	dir := t.TempDir()
	stale := filepath.Join(dir, "gh-token")
	if err := os.WriteFile(stale, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}

	err := extractCredentials(dir, []credentialSource{unavailableSource("gh-token")})
	if err != nil {
		t.Fatalf("extractCredentials() = %v", err)
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Errorf("stale gh-token should be removed, stat err = %v", err)
	}
}

func TestExtractCredentialsFailsOnUnwritableDir(t *testing.T) {
	// Regression: a credentials dir owned by another user (e.g. created by a
	// root-run devc) used to fail silently, leaving containers without gh auth.
	// Write failures must surface as errors.
	if os.Getuid() == 0 {
		t.Skip("root bypasses file permissions")
	}
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })

	err := extractCredentials(dir, []credentialSource{fixedSource("gh-token", "x")})
	if err == nil {
		t.Fatal("extractCredentials() = nil, want error for unwritable dir")
	}
}
