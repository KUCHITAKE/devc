package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

type featureRef struct {
	Registry   string
	Repository string
	Tag        string
	ID         string
}

func parseFeatureRef(ref string) (featureRef, error) {
	if ref == "" {
		return featureRef{}, fmt.Errorf("empty feature ref")
	}

	// Split registry from rest: ghcr.io/org/repo/name:tag
	slashIdx := strings.Index(ref, "/")
	if slashIdx < 0 {
		return featureRef{}, fmt.Errorf("invalid feature ref %q: no path separator", ref)
	}

	registry := ref[:slashIdx]
	rest := ref[slashIdx+1:]

	// Split tag
	tag := "latest"
	if colonIdx := strings.LastIndex(rest, ":"); colonIdx >= 0 {
		tag = rest[colonIdx+1:]
		rest = rest[:colonIdx]
	}

	// rest is the repository path, ID is the last segment
	lastSlash := strings.LastIndex(rest, "/")
	if lastSlash < 0 {
		return featureRef{}, fmt.Errorf("invalid feature ref %q: need at least org/name", ref)
	}
	id := rest[lastSlash+1:]

	return featureRef{
		Registry:   registry,
		Repository: rest,
		Tag:        tag,
		ID:         id,
	}, nil
}

type featureFiles struct {
	InstallSh []byte
	AllFiles  map[string][]byte
}

func extractFeatureTar(data []byte) (*featureFiles, error) {
	// OCI feature blobs are plain tar; detect gzip and unwrap if needed.
	var r io.Reader = bytes.NewReader(data)
	if len(data) >= 2 && data[0] == 0x1f && data[1] == 0x8b {
		gr, err := gzip.NewReader(r)
		if err != nil {
			return nil, fmt.Errorf("gzip open: %w", err)
		}
		defer func() { _ = gr.Close() }()
		r = gr
	}

	files := &featureFiles{AllFiles: make(map[string][]byte)}
	tr := tar.NewReader(r)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("tar read: %w", err)
		}
		if hdr.Typeflag != tar.TypeReg {
			continue
		}
		data, err := io.ReadAll(tr)
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", hdr.Name, err)
		}
		// Normalize path: strip leading ./ or /
		name := strings.TrimPrefix(hdr.Name, "./")
		name = strings.TrimPrefix(name, "/")
		files.AllFiles[name] = data
	}

	installSh, ok := files.AllFiles["install.sh"]
	if !ok {
		return nil, fmt.Errorf("feature archive missing install.sh")
	}
	files.InstallSh = installSh

	return files, nil
}

func featureCacheDir() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".cache", "devc", "features")
}

func pullFeature(ctx context.Context, ref featureRef) (*featureFiles, error) {
	// 1. Check cache
	cacheDir := featureCacheDir()

	// 2. Get auth token
	tokenURL := fmt.Sprintf("https://%s/token?service=%s&scope=repository:%s:pull",
		ref.Registry, ref.Registry, ref.Repository)
	token, err := fetchToken(ctx, tokenURL)
	if err != nil {
		return nil, fmt.Errorf("auth token: %w", err)
	}

	// 3. Get manifest
	manifestURL := fmt.Sprintf("https://%s/v2/%s/manifests/%s",
		ref.Registry, ref.Repository, ref.Tag)
	manifest, err := fetchManifest(ctx, manifestURL, token)
	if err != nil {
		return nil, fmt.Errorf("manifest: %w", err)
	}

	// Check cache by digest
	if len(manifest.Layers) == 0 {
		return nil, fmt.Errorf("manifest has no layers")
	}
	layer := manifest.Layers[0]
	cacheFile := filepath.Join(cacheDir, strings.ReplaceAll(layer.Digest, ":", "-")+".tgz")

	if data, err := os.ReadFile(cacheFile); err == nil {
		return extractFeatureTar(data)
	}

	// 4. Download blob
	blobURL := fmt.Sprintf("https://%s/v2/%s/blobs/%s",
		ref.Registry, ref.Repository, layer.Digest)
	blobData, err := fetchBlob(ctx, blobURL, token)
	if err != nil {
		return nil, fmt.Errorf("blob download: %w", err)
	}

	// Save to cache
	_ = os.MkdirAll(cacheDir, 0o755)
	_ = os.WriteFile(cacheFile, blobData, 0o644)

	return extractFeatureTar(blobData)
}

type ociManifest struct {
	Layers []ociDescriptor `json:"layers"`
}

type ociDescriptor struct {
	MediaType string `json:"mediaType"`
	Digest    string `json:"digest"`
	Size      int64  `json:"size"`
}

func fetchToken(ctx context.Context, url string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return "", err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("token endpoint returned %d", resp.StatusCode)
	}
	var result struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", err
	}
	return result.Token, nil
}

func fetchManifest(ctx context.Context, url, token string) (*ociManifest, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.oci.image.manifest.v1+json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("manifest endpoint returned %d", resp.StatusCode)
	}
	var m ociManifest
	if err := json.NewDecoder(resp.Body).Decode(&m); err != nil {
		return nil, err
	}
	return &m, nil
}

func fetchBlob(ctx context.Context, url, token string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, err
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("blob endpoint returned %d", resp.StatusCode)
	}
	return io.ReadAll(resp.Body)
}

// computeFeatureDigest computes SHA-256 of a tgz blob for cache keying.
func computeFeatureDigest(data []byte) string {
	h := sha256.Sum256(data)
	return fmt.Sprintf("sha256:%x", h)
}
