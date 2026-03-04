package main

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/docker/docker/api/types/build"
	"github.com/docker/docker/api/types/image"
)

type featureInstall struct {
	ID      string
	Files   *featureFiles
	Options map[string]interface{}
}

func generateDockerfile(baseImage string, features []featureInstall) string {
	var b strings.Builder
	fmt.Fprintf(&b, "FROM %s\n", baseImage)

	for _, f := range features {
		fmt.Fprintf(&b, "COPY %s/ /tmp/build-features/%s/\n", f.ID, f.ID)
		fmt.Fprintf(&b, "RUN cd /tmp/build-features/%s", f.ID)

		envs := featureEnvVars(f.Options)
		// Sort keys for deterministic output
		keys := make([]string, 0, len(envs))
		for k := range envs {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			fmt.Fprintf(&b, " \\\n    && export %s=%s", k, envs[k])
		}

		b.WriteString(" \\\n    && chmod +x install.sh && ./install.sh")
		fmt.Fprintf(&b, " \\\n    && rm -rf /tmp/build-features/%s\n", f.ID)
	}

	return b.String()
}

func featureEnvVars(options map[string]interface{}) map[string]string {
	envs := make(map[string]string)
	for k, v := range options {
		key := strings.ToUpper(k)
		switch val := v.(type) {
		case string:
			envs[key] = val
		case bool:
			envs[key] = fmt.Sprintf("%t", val)
		case float64:
			if val == float64(int64(val)) {
				envs[key] = fmt.Sprintf("%d", int64(val))
			} else {
				envs[key] = fmt.Sprintf("%g", val)
			}
		default:
			envs[key] = fmt.Sprintf("%v", val)
		}
	}
	return envs
}

func computeImageTag(wsName, baseImage string, features map[string]map[string]interface{}) string {
	h := sha256.New()
	_, _ = fmt.Fprintf(h, "base=%s\n", baseImage)

	// Sort feature keys for determinism
	keys := make([]string, 0, len(features))
	for k := range features {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		_, _ = fmt.Fprintf(h, "feature=%s\n", k)
		opts := features[k]
		optKeys := make([]string, 0, len(opts))
		for ok := range opts {
			optKeys = append(optKeys, ok)
		}
		sort.Strings(optKeys)
		for _, ok := range optKeys {
			_, _ = fmt.Fprintf(h, "  %s=%v\n", ok, opts[ok])
		}
	}

	sum := fmt.Sprintf("%x", h.Sum(nil))
	return fmt.Sprintf("devc-%s:%s", wsName, sum[:12])
}

func prepareBuildContext(dockerfile string, features []featureInstall) (io.Reader, error) {
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)

	// Add Dockerfile
	dfBytes := []byte(dockerfile)
	if err := tw.WriteHeader(&tar.Header{
		Name: "Dockerfile",
		Mode: 0o644,
		Size: int64(len(dfBytes)),
	}); err != nil {
		return nil, err
	}
	if _, err := tw.Write(dfBytes); err != nil {
		return nil, err
	}

	// Add feature files
	for _, f := range features {
		for name, data := range f.Files.AllFiles {
			hdr := &tar.Header{
				Name: f.ID + "/" + name,
				Mode: 0o755,
				Size: int64(len(data)),
			}
			if err := tw.WriteHeader(hdr); err != nil {
				return nil, err
			}
			if _, err := tw.Write(data); err != nil {
				return nil, err
			}
		}
	}

	if err := tw.Close(); err != nil {
		return nil, err
	}
	return &buf, nil
}

func buildFeatureImage(ctx context.Context, ws workspace, cfg *devcontainerConfig,
	userFeatures map[string]map[string]interface{}) (string, error) {
	// Merge user features with project features (project wins)
	allFeatures := mergeFeatures(userFeatures, cfg.Features)

	baseImage := cfg.Image
	if cfg.Build != nil && cfg.Build.Dockerfile != "" {
		// Build user Dockerfile first
		var err error
		baseImage, err = buildUserDockerfile(ctx, ws, cfg)
		if err != nil {
			return "", fmt.Errorf("build user dockerfile: %w", err)
		}
	}

	if baseImage == "" {
		return "", fmt.Errorf("no image or build.dockerfile specified")
	}

	imageTag := computeImageTag(ws.name, baseImage, allFeatures)

	// Check if image already exists locally (cache hit)
	cli, err := getDockerClient()
	if err != nil {
		return "", fmt.Errorf("docker client: %w", err)
	}
	if _, err := cli.ImageInspect(ctx, imageTag); err == nil {
		printDone("Using cached image", imageTag)
		return imageTag, nil
	}

	if len(allFeatures) == 0 {
		// No features — just pull and tag the base image
		if err := runWithSpinner("Pulling base image", baseImage, func() error {
			reader, err := cli.ImagePull(ctx, baseImage, image.PullOptions{})
			if err != nil {
				return fmt.Errorf("pull image: %w", err)
			}
			if err := drainDockerOutput(reader); err != nil {
				_ = reader.Close()
				return fmt.Errorf("pull image: %w", err)
			}
			_ = reader.Close()
			return nil
		}); err != nil {
			return "", err
		}
		printDone("Pulled base image", baseImage)

		// Tag
		if err := cli.ImageTag(ctx, baseImage, imageTag); err != nil {
			return "", fmt.Errorf("tag image: %w", err)
		}
		return imageTag, nil
	}

	// Pull features
	printProgress("Pulling features", fmt.Sprintf("%d features", len(allFeatures)))
	var installs []featureInstall
	for ref, opts := range allFeatures {
		fr, err := parseFeatureRef(ref)
		if err != nil {
			return "", fmt.Errorf("parse feature ref %q: %w", ref, err)
		}
		var files *featureFiles
		if err := runWithSpinner("Pulling feature", fr.ID, func() error {
			var pullErr error
			files, pullErr = pullFeature(ctx, fr)
			return pullErr
		}); err != nil {
			return "", fmt.Errorf("pull feature %q: %w", ref, err)
		}
		printDone("Pulled feature", fr.ID)
		installs = append(installs, featureInstall{
			ID:      fr.ID,
			Files:   files,
			Options: opts,
		})
	}

	// Sort installs by ID for deterministic Dockerfile
	sort.Slice(installs, func(i, j int) bool {
		return installs[i].ID < installs[j].ID
	})

	// Generate Dockerfile
	dockerfile := generateDockerfile(baseImage, installs)

	// Prepare build context
	buildCtx, err := prepareBuildContext(dockerfile, installs)
	if err != nil {
		return "", fmt.Errorf("prepare build context: %w", err)
	}

	// Build (with spinner — this can take a while)
	if err := runWithSpinner("Building image", imageTag, func() error {
		resp, err := cli.ImageBuild(ctx, buildCtx, build.ImageBuildOptions{
			Tags:        []string{imageTag},
			Remove:      true,
			ForceRemove: true,
		})
		if err != nil {
			return fmt.Errorf("image build: %w", err)
		}
		defer func() { _ = resp.Body.Close() }()
		if err := drainDockerOutput(resp.Body); err != nil {
			return fmt.Errorf("image build: %w", err)
		}
		return nil
	}); err != nil {
		return "", err
	}

	return imageTag, nil
}

func buildUserDockerfile(ctx context.Context, ws workspace, cfg *devcontainerConfig) (string, error) {
	cli, err := getDockerClient()
	if err != nil {
		return "", fmt.Errorf("docker client: %w", err)
	}

	dcDir := ws.dir + "/.devcontainer"
	dockerfilePath := cfg.Build.Dockerfile

	// Determine build context directory
	contextDir := dcDir
	if cfg.Build.Context != "" {
		if cfg.Build.Context == ".." {
			contextDir = ws.dir
		} else {
			contextDir = dcDir + "/" + cfg.Build.Context
		}
	}

	// Create build context tar from the context directory
	buildCtx, err := createDirTar(contextDir, dockerfilePath, dcDir)
	if err != nil {
		return "", fmt.Errorf("create build context: %w", err)
	}

	intermediateTag := fmt.Sprintf("devc-%s-intermediate:%s", ws.name, "latest")

	buildOpts := build.ImageBuildOptions{
		Tags:        []string{intermediateTag},
		Dockerfile:  dockerfilePath,
		Remove:      true,
		ForceRemove: true,
	}
	if cfg.Build.Target != "" {
		buildOpts.Target = cfg.Build.Target
	}
	if cfg.Build.Args != nil {
		buildOpts.BuildArgs = make(map[string]*string)
		for k, v := range cfg.Build.Args {
			val := v
			buildOpts.BuildArgs[k] = &val
		}
	}

	if err := runWithSpinner("Building Dockerfile", dockerfilePath, func() error {
		resp, err := cli.ImageBuild(ctx, buildCtx, buildOpts)
		if err != nil {
			return fmt.Errorf("image build: %w", err)
		}
		defer func() { _ = resp.Body.Close() }()
		if err := drainDockerOutput(resp.Body); err != nil {
			return fmt.Errorf("image build: %w", err)
		}
		return nil
	}); err != nil {
		return "", err
	}
	printDone("Built Dockerfile", dockerfilePath)

	return intermediateTag, nil
}

// createDirTar creates a tar archive from a directory for Docker build context.
// It walks the context directory and includes all regular files. If the Dockerfile
// is outside the context directory (e.g. in .devcontainer while context is ".."),
// it is added separately.
func createDirTar(contextDir, dockerfilePath, dcDir string) (io.Reader, error) {
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)

	// Walk context directory and add all regular files
	err := filepath.WalkDir(contextDir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}

		// Skip .git, node_modules, and .devcontainer subdirectories,
		// but never skip the walk root itself (contextDir may be .devcontainer).
		if d.IsDir() {
			if path != contextDir {
				name := d.Name()
				if name == ".git" || name == "node_modules" || name == ".devcontainer" {
					return filepath.SkipDir
				}
			}
			return nil
		}

		// Only include regular files
		if !d.Type().IsRegular() {
			return nil
		}

		relPath, err := filepath.Rel(contextDir, path)
		if err != nil {
			return err
		}

		data, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("read %s: %w", relPath, err)
		}

		info, err := d.Info()
		if err != nil {
			return err
		}

		if err := tw.WriteHeader(&tar.Header{
			Name: relPath,
			Mode: int64(info.Mode() & 0o777),
			Size: int64(len(data)),
		}); err != nil {
			return err
		}
		_, err = tw.Write(data)
		return err
	})
	if err != nil {
		return nil, fmt.Errorf("walk context dir %s: %w", contextDir, err)
	}

	// Add Dockerfile from dcDir (it may have been skipped if .devcontainer was excluded,
	// or it may not be in the context dir at all)
	dfInDcDir := filepath.Join(dcDir, dockerfilePath)
	dfInContext := filepath.Join(contextDir, dockerfilePath)

	// Check if the Dockerfile was already included during the walk
	dfAlreadyIncluded := false
	if abs1, err1 := filepath.Abs(dfInDcDir); err1 == nil {
		if abs2, err2 := filepath.Abs(dfInContext); err2 == nil {
			if abs1 == abs2 {
				// Dockerfile is in the context dir and was already walked
				// But only if we didn't skip its parent (.devcontainer)
				if _, statErr := os.Stat(dfInContext); statErr == nil {
					// Check if it's actually under .devcontainer which we skipped
					relToCtx, _ := filepath.Rel(contextDir, dfInDcDir)
					if !strings.HasPrefix(relToCtx, ".devcontainer") {
						dfAlreadyIncluded = true
					}
				}
			}
		}
	}

	if !dfAlreadyIncluded {
		dfData, err := os.ReadFile(dfInDcDir)
		if err != nil {
			return nil, fmt.Errorf("read Dockerfile %s: %w", dfInDcDir, err)
		}
		if err := tw.WriteHeader(&tar.Header{
			Name: dockerfilePath,
			Mode: 0o644,
			Size: int64(len(dfData)),
		}); err != nil {
			return nil, err
		}
		if _, err := tw.Write(dfData); err != nil {
			return nil, err
		}
	}

	if err := tw.Close(); err != nil {
		return nil, err
	}
	return &buf, nil
}

// mergeFeatures merges user features with project features. Project features
// take precedence over user features (project overrides user).
func mergeFeatures(userFeatures, projectFeatures map[string]map[string]interface{}) map[string]map[string]interface{} {
	result := make(map[string]map[string]interface{})

	for k, v := range userFeatures {
		result[k] = v
	}

	// Project features override user features
	for k, v := range projectFeatures {
		result[k] = v
	}

	return result
}

// readTarContents is a test helper to read all files from a tar reader.
func readTarContents(t interface{ Helper(); Fatal(...interface{}) }, r io.Reader) map[string][]byte {
	t.Helper()
	tr := tar.NewReader(r)
	contents := make(map[string][]byte)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		data, err := io.ReadAll(tr)
		if err != nil {
			t.Fatal(err)
		}
		contents[hdr.Name] = data
	}
	return contents
}
