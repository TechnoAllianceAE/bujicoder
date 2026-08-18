package tools

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// unmarshalArgs decodes an LLM-produced tool argument blob. An empty or null
// blob is treated as "no arguments" so tools without required parameters still
// work, and a malformed blob yields a descriptive tool error naming the tool
// instead of a bare json error (or a nil dereference further down).
func unmarshalArgs(tool string, args json.RawMessage, dst any) error {
	trimmed := bytes.TrimSpace(args)
	if len(trimmed) == 0 || string(trimmed) == "null" {
		return nil
	}
	if err := json.Unmarshal(trimmed, dst); err != nil {
		return fmt.Errorf("%s: invalid arguments: %w", tool, err)
	}
	return nil
}

// writeFileAtomic writes data to path via a temporary file in the same
// directory followed by os.Rename. os.WriteFile truncates the destination
// before writing, so a failure part-way through (disk full, process killed)
// destroys the user's file; a rename either fully succeeds or leaves the
// original untouched.
//
// The existing file mode is preserved; perm applies to newly created files. A
// symlinked destination is resolved first so the rename updates the file the
// link points at instead of replacing the link — callers validate the resolved
// target with safePath before getting here.
func writeFileAtomic(path string, data []byte, perm os.FileMode) error {
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		path = resolved
	}

	mode := perm
	if info, err := os.Stat(path); err == nil && info.Mode().IsRegular() {
		mode = info.Mode().Perm()
	}

	f, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp")
	if err != nil {
		return fmt.Errorf("create temp file for %s: %w", path, err)
	}
	tmp := f.Name()
	defer func() {
		if tmp != "" {
			_ = os.Remove(tmp)
		}
	}()

	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return fmt.Errorf("write temp file for %s: %w", path, err)
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return fmt.Errorf("sync temp file for %s: %w", path, err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("close temp file for %s: %w", path, err)
	}
	if err := os.Chmod(tmp, mode); err != nil {
		return fmt.Errorf("chmod temp file for %s: %w", path, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("replace %s: %w", path, err)
	}
	tmp = "" // renamed away; nothing to clean up
	return nil
}
