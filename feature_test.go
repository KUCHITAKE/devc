package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestParseFeatureRef(t *testing.T) {
	tests := []struct {
		name       string
		ref        string
		wantReg    string
		wantRepo   string
		wantTag    string
		wantID     string
		wantErr    bool
	}{
		{
			name:     "github-cli",
			ref:      "ghcr.io/devcontainers/features/github-cli:1",
			wantReg:  "ghcr.io",
			wantRepo: "devcontainers/features/github-cli",
			wantTag:  "1",
			wantID:   "github-cli",
		},
		{
			name:     "neovim",
			ref:      "ghcr.io/duduribeiro/devcontainer-features/neovim:1",
			wantReg:  "ghcr.io",
			wantRepo: "duduribeiro/devcontainer-features/neovim",
			wantTag:  "1",
			wantID:   "neovim",
		},
		{
			name:     "no tag defaults to latest",
			ref:      "ghcr.io/devcontainers/features/go",
			wantReg:  "ghcr.io",
			wantRepo: "devcontainers/features/go",
			wantTag:  "latest",
			wantID:   "go",
		},
		{
			name:    "invalid ref - no slash",
			ref:     "ghcr.io",
			wantErr: true,
		},
		{
			name:    "empty ref",
			ref:     "",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseFeatureRef(tt.ref)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got.Registry != tt.wantReg {
				t.Fatalf("Registry = %q, want %q", got.Registry, tt.wantReg)
			}
			if got.Repository != tt.wantRepo {
				t.Fatalf("Repository = %q, want %q", got.Repository, tt.wantRepo)
			}
			if got.Tag != tt.wantTag {
				t.Fatalf("Tag = %q, want %q", got.Tag, tt.wantTag)
			}
			if got.ID != tt.wantID {
				t.Fatalf("ID = %q, want %q", got.ID, tt.wantID)
			}
		})
	}
}

func createTestTgz(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gw)
	for name, content := range files {
		hdr := &tar.Header{
			Name: name,
			Mode: 0o755,
			Size: int64(len(content)),
		}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestExtractFeatureTar(t *testing.T) {
	tgz := createTestTgz(t, map[string]string{
		"install.sh":          "#!/bin/bash\necho hello",
		"devcontainer-feature.json": `{"id": "test"}`,
	})
	files, err := extractFeatureTar(tgz)
	if err != nil {
		t.Fatal(err)
	}
	if string(files.InstallSh) != "#!/bin/bash\necho hello" {
		t.Fatalf("InstallSh = %q", string(files.InstallSh))
	}
	if len(files.AllFiles) != 2 {
		t.Fatalf("AllFiles count = %d, want 2", len(files.AllFiles))
	}
}

func TestExtractFeatureTar_PlainTar(t *testing.T) {
	// Create a plain tar (not gzip) — this is what OCI registries actually return
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	content := "#!/bin/bash\necho plain"
	_ = tw.WriteHeader(&tar.Header{Name: "./install.sh", Mode: 0o755, Size: int64(len(content))})
	_, _ = tw.Write([]byte(content))
	_ = tw.Close()

	files, err := extractFeatureTar(buf.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	if string(files.InstallSh) != "#!/bin/bash\necho plain" {
		t.Fatalf("InstallSh = %q", string(files.InstallSh))
	}
}

func TestExtractFeatureTar_MissingInstall(t *testing.T) {
	tgz := createTestTgz(t, map[string]string{
		"readme.md": "hello",
	})
	_, err := extractFeatureTar(tgz)
	if err == nil {
		t.Fatal("expected error for missing install.sh")
	}
}

func TestPullFeature(t *testing.T) {
	// Create a test tgz with install.sh
	tgz := createTestTgz(t, map[string]string{
		"install.sh": "#!/bin/bash\necho installed",
	})
	digest := computeFeatureDigest(tgz)

	// Create a mock OCI registry
	mux := http.NewServeMux()

	// Token endpoint
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{"token": "test-token"})
	})

	// Manifest endpoint
	mux.HandleFunc("/v2/org/features/test/manifests/1", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-token" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		manifest := ociManifest{
			Layers: []ociDescriptor{
				{
					MediaType: "application/vnd.devcontainers.layer.v1+tar",
					Digest:    digest,
					Size:      int64(len(tgz)),
				},
			},
		}
		_ = json.NewEncoder(w).Encode(manifest)
	})

	// Blob endpoint
	mux.HandleFunc(fmt.Sprintf("/v2/org/features/test/blobs/%s", digest), func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-token" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		_, _ = w.Write(tgz)
	})

	srv := httptest.NewServer(mux)
	defer srv.Close()

	// Parse the server URL to get host:port
	host := srv.Listener.Addr().String()

	// Override the pullFeature to use HTTP by using a custom function
	// Instead, we test the individual fetch functions
	ref := featureRef{
		Registry:   host,
		Repository: "org/features/test",
		Tag:        "1",
		ID:         "test",
	}

	ctx := context.Background()

	// Test token fetch
	tokenURL := fmt.Sprintf("%s/token?service=%s&scope=repository:%s:pull", srv.URL, host, ref.Repository)
	token, err := fetchToken(ctx, tokenURL, "")
	if err != nil {
		t.Fatalf("fetchToken: %v", err)
	}
	if token != "test-token" {
		t.Fatalf("token = %q, want %q", token, "test-token")
	}

	// Test manifest fetch
	manifestURL := fmt.Sprintf("%s/v2/%s/manifests/%s", srv.URL, ref.Repository, ref.Tag)
	manifest, err := fetchManifest(ctx, manifestURL, token)
	if err != nil {
		t.Fatalf("fetchManifest: %v", err)
	}
	if len(manifest.Layers) != 1 {
		t.Fatalf("layers = %d, want 1", len(manifest.Layers))
	}
	if manifest.Layers[0].Digest != digest {
		t.Fatalf("digest = %q, want %q", manifest.Layers[0].Digest, digest)
	}

	// Test blob fetch
	blobURL := fmt.Sprintf("%s/v2/%s/blobs/%s", srv.URL, ref.Repository, manifest.Layers[0].Digest)
	blobData, err := fetchBlob(ctx, blobURL, token)
	if err != nil {
		t.Fatalf("fetchBlob: %v", err)
	}

	// Extract and verify
	files, err := extractFeatureTar(blobData)
	if err != nil {
		t.Fatalf("extractFeatureTar: %v", err)
	}
	if string(files.InstallSh) != "#!/bin/bash\necho installed" {
		t.Fatalf("InstallSh = %q", string(files.InstallSh))
	}
}
