package build

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
	"sort"
	"strings"
)

type FeatureRef struct {
	Registry   string
	Repository string
	Tag        string
	ID         string
}

func ParseFeatureRef(ref string) (FeatureRef, error) {
	if ref == "" {
		return FeatureRef{}, fmt.Errorf("empty feature ref")
	}

	// Split registry from rest: ghcr.io/org/repo/name:tag
	slashIdx := strings.Index(ref, "/")
	if slashIdx < 0 {
		return FeatureRef{}, fmt.Errorf("invalid feature ref %q: no path separator", ref)
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
		return FeatureRef{}, fmt.Errorf("invalid feature ref %q: need at least org/name", ref)
	}
	id := rest[lastSlash+1:]

	return FeatureRef{
		Registry:   registry,
		Repository: rest,
		Tag:        tag,
		ID:         id,
	}, nil
}

type FeatureFiles struct {
	InstallSh []byte
	AllFiles  map[string][]byte
}

// featureJSON represents the relevant fields of devcontainer-feature.json.
type featureJSON struct {
	ContainerEnv map[string]string `json:"containerEnv"`
}

// ParseContainerEnv extracts the containerEnv map from devcontainer-feature.json
// in the feature's file set. Returns nil if the file is absent or has no containerEnv.
func ParseContainerEnv(files *FeatureFiles) map[string]string {
	data, ok := files.AllFiles["devcontainer-feature.json"]
	if !ok {
		return nil
	}
	var fj featureJSON
	if err := json.Unmarshal(data, &fj); err != nil {
		return nil
	}
	return fj.ContainerEnv
}

func ExtractFeatureTar(data []byte) (*FeatureFiles, error) {
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

	files := &FeatureFiles{AllFiles: make(map[string][]byte)}
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

func FeatureCacheDir() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".cache", "devc", "features")
}

// PullResult holds the extracted feature files and the resolved OCI layer digest.
type PullResult struct {
	Files  *FeatureFiles
	Digest string
}

func PullFeature(ctx context.Context, ref FeatureRef) (*PullResult, error) {
	cacheDir := FeatureCacheDir()

	// 1. Get auth token
	tokenURL := fmt.Sprintf("https://%s/token?service=%s&scope=repository:%s:pull",
		ref.Registry, ref.Registry, ref.Repository)
	basic := registryBasicAuth(ref.Registry)
	token, err := fetchToken(ctx, tokenURL, basic)
	if err != nil {
		return nil, fmt.Errorf("auth token: %w", err)
	}

	// 2. Get manifest
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
		files, err := ExtractFeatureTar(data)
		if err != nil {
			return nil, err
		}
		return &PullResult{Files: files, Digest: layer.Digest}, nil
	}

	// 3. Download blob
	blobURL := fmt.Sprintf("https://%s/v2/%s/blobs/%s",
		ref.Registry, ref.Repository, layer.Digest)
	blobData, err := fetchBlob(ctx, blobURL, token)
	if err != nil {
		return nil, fmt.Errorf("blob download: %w", err)
	}

	// Save to cache
	_ = os.MkdirAll(cacheDir, 0o755)
	_ = os.WriteFile(cacheFile, blobData, 0o644)

	files, err := ExtractFeatureTar(blobData)
	if err != nil {
		return nil, err
	}
	return &PullResult{Files: files, Digest: layer.Digest}, nil
}

// PullFeatureByDigest pulls a feature using a locked digest, skipping tag resolution.
func PullFeatureByDigest(ctx context.Context, ref FeatureRef, digest string) (*PullResult, error) {
	cacheDir := FeatureCacheDir()

	// Check cache first
	cacheFile := filepath.Join(cacheDir, strings.ReplaceAll(digest, ":", "-")+".tgz")
	if data, err := os.ReadFile(cacheFile); err == nil {
		files, err := ExtractFeatureTar(data)
		if err != nil {
			return nil, err
		}
		return &PullResult{Files: files, Digest: digest}, nil
	}

	// Get auth token
	tokenURL := fmt.Sprintf("https://%s/token?service=%s&scope=repository:%s:pull",
		ref.Registry, ref.Registry, ref.Repository)
	basic := registryBasicAuth(ref.Registry)
	token, err := fetchToken(ctx, tokenURL, basic)
	if err != nil {
		return nil, fmt.Errorf("auth token: %w", err)
	}

	// Download blob directly by digest
	blobURL := fmt.Sprintf("https://%s/v2/%s/blobs/%s",
		ref.Registry, ref.Repository, digest)
	blobData, err := fetchBlob(ctx, blobURL, token)
	if err != nil {
		return nil, fmt.Errorf("blob download: %w", err)
	}

	// Save to cache
	_ = os.MkdirAll(cacheDir, 0o755)
	_ = os.WriteFile(cacheFile, blobData, 0o644)

	files, err := ExtractFeatureTar(blobData)
	if err != nil {
		return nil, err
	}
	return &PullResult{Files: files, Digest: digest}, nil
}

type ociManifest struct {
	Layers []ociDescriptor `json:"layers"`
}

type ociDescriptor struct {
	MediaType string `json:"mediaType"`
	Digest    string `json:"digest"`
	Size      int64  `json:"size"`
}

func registryBasicAuth(registry string) string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	data, err := os.ReadFile(filepath.Join(home, ".docker", "config.json"))
	if err != nil {
		return ""
	}
	var cfg struct {
		Auths map[string]struct {
			Auth string `json:"auth"`
		} `json:"auths"`
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		return ""
	}
	if entry, ok := cfg.Auths[registry]; ok && entry.Auth != "" {
		return entry.Auth
	}
	return ""
}

func fetchToken(ctx context.Context, url, basicAuth string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return "", err
	}
	if basicAuth != "" {
		req.Header.Set("Authorization", "Basic "+basicAuth)
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

// ComputeFeatureDigest computes SHA-256 of a tgz blob for cache keying.
func ComputeFeatureDigest(data []byte) string {
	h := sha256.Sum256(data)
	return fmt.Sprintf("sha256:%x", h)
}

// IsLocalFeature returns true if the feature ref is a local path (starts with "./").
func IsLocalFeature(ref string) bool {
	return strings.HasPrefix(ref, "./")
}

// LoadLocalFeature loads a devcontainer feature from a local directory.
// The ref must start with "./" (e.g., "./myFeature"). The feature directory
// is resolved relative to <wsDir>/.devcontainer/.
func LoadLocalFeature(wsDir string, ref string) (*PullResult, error) {
	dirName := strings.TrimPrefix(ref, "./")
	featureDir := filepath.Join(wsDir, ".devcontainer", dirName)

	info, err := os.Stat(featureDir)
	if err != nil {
		return nil, fmt.Errorf("local feature %q: %w", ref, err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("local feature %q: not a directory", ref)
	}

	files := &FeatureFiles{AllFiles: make(map[string][]byte)}
	h := sha256.New()

	// Walk directory and collect all regular files
	var sortedPaths []string
	fileData := make(map[string][]byte)

	err = filepath.WalkDir(featureDir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		if !d.Type().IsRegular() {
			return nil
		}
		relPath, err := filepath.Rel(featureDir, path)
		if err != nil {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("read %s: %w", relPath, err)
		}
		sortedPaths = append(sortedPaths, relPath)
		fileData[relPath] = data
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("local feature %q: %w", ref, err)
	}

	// Sort paths for deterministic digest
	sort.Strings(sortedPaths)
	for _, p := range sortedPaths {
		data := fileData[p]
		files.AllFiles[p] = data
		_, _ = fmt.Fprintf(h, "%s\n%d\n", p, len(data))
		_, _ = h.Write(data)
	}

	installSh, ok := files.AllFiles["install.sh"]
	if !ok {
		return nil, fmt.Errorf("local feature %q: missing install.sh", ref)
	}
	files.InstallSh = installSh

	digest := fmt.Sprintf("sha256:%x", h.Sum(nil))
	return &PullResult{Files: files, Digest: digest}, nil
}
