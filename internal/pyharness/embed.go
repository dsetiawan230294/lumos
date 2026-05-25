// Package pyharness embeds the Lumos Python scenario harness and helper
// package into the Go binary, so the CLI can drop them onto disk at runtime.
//
// This is what implements the "vendored Python helper" half of the Q3
// decision: when the user has not installed lumos-py from PyPI, Lumos
// extracts these files to a temp dir and prepends it to PYTHONPATH before
// spawning the scenario subprocess.
package pyharness

import (
	"embed"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
)

//go:embed python/*.py python/lumos/*.py
var content embed.FS

var (
	extractOnce sync.Once
	extractDir  string
	extractErr  error
)

// Extract writes the embedded harness + lumos/ package to a stable directory
// (cached for the process lifetime) and returns (dir, harnessPath).
//
// dir is suitable to prepend to PYTHONPATH; harnessPath is the absolute path
// to harness.py to pass as argv[1] to python3.
func Extract() (dir, harnessPath string, err error) {
	extractOnce.Do(func() {
		extractDir, extractErr = doExtract()
	})
	if extractErr != nil {
		return "", "", extractErr
	}
	return extractDir, filepath.Join(extractDir, "harness.py"), nil
}

func doExtract() (string, error) {
	base, err := os.MkdirTemp("", "lumos-py-")
	if err != nil {
		return "", fmt.Errorf("mkdir temp: %w", err)
	}
	walkErr := fs.WalkDir(content, "python", func(p string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		// Strip the "python/" prefix so files land at the root of base.
		rel := p[len("python"):]
		rel = filepath.FromSlash(rel)
		if rel == "" {
			return nil
		}
		dst := filepath.Join(base, rel)
		if d.IsDir() {
			return os.MkdirAll(dst, 0o755)
		}
		b, err := content.ReadFile(p)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return err
		}
		return os.WriteFile(dst, b, 0o644)
	})
	if walkErr != nil {
		_ = os.RemoveAll(base)
		return "", walkErr
	}
	return base, nil
}
