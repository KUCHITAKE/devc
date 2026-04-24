package build

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
)

// Lockfile records resolved digests for devcontainer features,
// ensuring reproducible builds across environments and time.
type Lockfile struct {
	Features map[string]FeatureLock `json:"features"`
}

// FeatureLock holds the resolved OCI digest for a single feature.
type FeatureLock struct {
	Version  string `json:"version"`
	Resolved string `json:"resolved"`
}

// LockfilePath returns the path to the lockfile for a workspace directory.
func LockfilePath(wsDir string) string {
	return filepath.Join(wsDir, ".devcontainer", "devcontainer-lock.json")
}

// ReadLockfile reads and parses the lockfile. Returns nil, nil if the file does not exist.
func ReadLockfile(path string) (*Lockfile, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read lockfile: %w", err)
	}
	var lf Lockfile
	if err := json.Unmarshal(data, &lf); err != nil {
		return nil, fmt.Errorf("parse lockfile: %w", err)
	}
	return &lf, nil
}

// WriteLockfile writes the lockfile with deterministic (sorted) key order.
func WriteLockfile(path string, lf *Lockfile) error {
	// Sort keys for deterministic output
	ordered := make([]string, 0, len(lf.Features))
	for k := range lf.Features {
		ordered = append(ordered, k)
	}
	sort.Strings(ordered)

	type orderedEntry struct {
		Key   string
		Value FeatureLock
	}
	entries := make([]orderedEntry, len(ordered))
	for i, k := range ordered {
		entries[i] = orderedEntry{Key: k, Value: lf.Features[k]}
	}

	// Build JSON manually for sorted keys
	buf := []byte("{\n  \"features\": {")
	for i, e := range entries {
		val, err := json.Marshal(e.Value)
		if err != nil {
			return fmt.Errorf("marshal feature lock: %w", err)
		}
		if i > 0 {
			buf = append(buf, ',')
		}
		buf = append(buf, '\n')
		key, _ := json.Marshal(e.Key)
		buf = append(buf, "    "...)
		buf = append(buf, key...)
		buf = append(buf, ": "...)
		buf = append(buf, val...)
	}
	if len(entries) > 0 {
		buf = append(buf, '\n')
		buf = append(buf, "  "...)
	}
	buf = append(buf, "}\n}\n"...)

	return os.WriteFile(path, buf, 0o644)
}
