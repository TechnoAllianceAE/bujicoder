package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/TechnoAllianceAE/bujicoder/shared/tools/editmatch"
)

// multiEdit applies multiple str_replace edits in a single call.
// Edits are grouped by file and applied sequentially within each file.
func multiEdit(workDir string, perms *ProjectPermissions) func(ctx context.Context, args json.RawMessage) (string, error) {
	return func(ctx context.Context, args json.RawMessage) (string, error) {
		var params struct {
			Edits []struct {
				Path   string `json:"path"`
				OldStr string `json:"old_str"`
				NewStr string `json:"new_str"`
			} `json:"edits"`
		}
		if err := json.Unmarshal(args, &params); err != nil {
			return "", fmt.Errorf("parse multi_edit args: %w", err)
		}

		if len(params.Edits) == 0 {
			return "No edits provided.", nil
		}

		if IsPlanMode(ctx) {
			return "", fmt.Errorf("BLOCKED (plan mode): multi_edit is not allowed in plan mode")
		}

		dir := effectiveWorkDir(ctx, workDir)

		// Group edits by file path, preserving order within each file.
		type fileEdit struct {
			oldStr string
			newStr string
		}
		fileEdits := make(map[string][]fileEdit)
		var fileOrder []string
		for _, e := range params.Edits {
			if _, seen := fileEdits[e.Path]; !seen {
				fileOrder = append(fileOrder, e.Path)
			}
			fileEdits[e.Path] = append(fileEdits[e.Path], fileEdit{e.OldStr, e.NewStr})
		}

		var results []string
		totalApplied := 0
		totalFailed := 0

		for _, path := range fileOrder {
			edits := fileEdits[path]

			if perms.IsPathRestricted(path) {
				results = append(results, fmt.Sprintf("SKIP %s: restricted by permissions", path))
				totalFailed += len(edits)
				continue
			}

			absPath, err := safePath(dir, path)
			if err != nil {
				results = append(results, fmt.Sprintf("SKIP %s: %v", path, err))
				totalFailed += len(edits)
				continue
			}

			data, err := os.ReadFile(absPath)
			if err != nil {
				results = append(results, fmt.Sprintf("SKIP %s: %v", path, err))
				totalFailed += len(edits)
				continue
			}

			content := string(data)
			applied := 0

			// Apply edits sequentially — each one modifies the content for the next.
			for _, edit := range edits {
				match := editmatch.Find(content, edit.oldStr)
				if match == nil {
					totalFailed++
					continue
				}
				content = content[:match.Start] + edit.newStr + content[match.End:]
				applied++
			}

			if applied > 0 {
				if err := os.WriteFile(absPath, []byte(content), 0o644); err != nil {
					results = append(results, fmt.Sprintf("FAIL %s: write error: %v", path, err))
					totalFailed += applied
					continue
				}
				// Invalidate cache.
				if cache := getContextCache(ctx); cache != nil {
					cache.Invalidate(path)
				}
			}

			totalApplied += applied
			failed := len(edits) - applied
			if failed > 0 {
				results = append(results, fmt.Sprintf("OK %s: %d/%d edits applied (%d not found)", path, applied, len(edits), failed))
			} else {
				results = append(results, fmt.Sprintf("OK %s: %d/%d edits applied", path, applied, len(edits)))
			}
		}

		summary := fmt.Sprintf("MultiEdit: %d applied, %d failed across %d files", totalApplied, totalFailed, len(fileOrder))
		return summary + "\n" + strings.Join(results, "\n"), nil
	}
}

// PatchOp represents a single file operation parsed from a unified diff.
type PatchOp struct {
	Action  string // "add", "update", "move", "delete"
	Path    string
	NewPath string // for "move" only
	Content string // full content for "add"; diff hunks for "update"
}

// parsePatch parses a unified diff into a list of patch operations.
func parsePatch(patchText string) ([]PatchOp, error) {
	var ops []PatchOp
	lines := strings.Split(patchText, "\n")
	i := 0

	for i < len(lines) {
		line := lines[i]

		// Look for diff headers.
		if !strings.HasPrefix(line, "--- ") {
			i++
			continue
		}

		// Parse --- line.
		fromPath := parseDiffPath(line)
		i++
		if i >= len(lines) || !strings.HasPrefix(lines[i], "+++ ") {
			continue
		}
		toPath := parseDiffPath(lines[i])
		i++

		// Determine operation type.
		switch {
		case fromPath == "" && toPath != "":
			// New file — collect added lines.
			var content strings.Builder
			for i < len(lines) && !strings.HasPrefix(lines[i], "--- ") {
				if strings.HasPrefix(lines[i], "+") && !strings.HasPrefix(lines[i], "+++") {
					content.WriteString(strings.TrimPrefix(lines[i], "+"))
					content.WriteString("\n")
				}
				i++
			}
			ops = append(ops, PatchOp{Action: "add", Path: toPath, Content: content.String()})

		case fromPath != "" && toPath == "":
			// Deleted file.
			for i < len(lines) && !strings.HasPrefix(lines[i], "--- ") {
				i++
			}
			ops = append(ops, PatchOp{Action: "delete", Path: fromPath})

		case fromPath != toPath:
			// Move/rename.
			var content strings.Builder
			for i < len(lines) && !strings.HasPrefix(lines[i], "--- ") {
				if strings.HasPrefix(lines[i], "+") && !strings.HasPrefix(lines[i], "+++") {
					content.WriteString(strings.TrimPrefix(lines[i], "+"))
					content.WriteString("\n")
				}
				i++
			}
			ops = append(ops, PatchOp{Action: "move", Path: fromPath, NewPath: toPath, Content: content.String()})

		default:
			// Update — collect the diff hunks for the `patch` command.
			start := i - 2 // include --- and +++ lines
			for i < len(lines) && !strings.HasPrefix(lines[i], "--- ") {
				i++
			}
			hunkText := strings.Join(lines[start:i], "\n") + "\n"
			ops = append(ops, PatchOp{Action: "update", Path: fromPath, Content: hunkText})
		}
	}

	if len(ops) == 0 {
		return nil, fmt.Errorf("no valid patch operations found")
	}
	return ops, nil
}

// parseDiffPath extracts the file path from a --- or +++ line.
func parseDiffPath(line string) string {
	// "--- a/path/to/file" or "--- /dev/null"
	parts := strings.SplitN(line, " ", 2)
	if len(parts) < 2 {
		return ""
	}
	path := parts[1]
	if path == "/dev/null" {
		return ""
	}
	// Strip "a/" or "b/" prefix.
	path = strings.TrimPrefix(path, "a/")
	path = strings.TrimPrefix(path, "b/")
	return path
}

// applyPatch applies a unified diff/patch to the project.
func applyPatch(workDir string, perms *ProjectPermissions) func(ctx context.Context, args json.RawMessage) (string, error) {
	return func(ctx context.Context, args json.RawMessage) (string, error) {
		var params struct {
			Patch string `json:"patch"`
		}
		if err := json.Unmarshal(args, &params); err != nil {
			return "", fmt.Errorf("parse apply_patch args: %w", err)
		}

		if IsPlanMode(ctx) {
			return "", fmt.Errorf("BLOCKED (plan mode): apply_patch is not allowed in plan mode")
		}

		dir := effectiveWorkDir(ctx, workDir)

		ops, err := parsePatch(params.Patch)
		if err != nil {
			return "", err
		}

		var results []string
		for _, op := range ops {
			if perms.IsPathRestricted(op.Path) {
				results = append(results, fmt.Sprintf("SKIP %s %s: restricted", strings.ToUpper(op.Action), op.Path))
				continue
			}

			switch op.Action {
			case "add":
				absPath, err := safePath(dir, op.Path)
				if err != nil {
					results = append(results, fmt.Sprintf("FAIL ADD %s: %v", op.Path, err))
					continue
				}
				os.MkdirAll(filepath.Dir(absPath), 0o755)
				if err := os.WriteFile(absPath, []byte(op.Content), 0o644); err != nil {
					results = append(results, fmt.Sprintf("FAIL ADD %s: %v", op.Path, err))
					continue
				}
				if cache := getContextCache(ctx); cache != nil {
					cache.Invalidate(op.Path)
				}
				results = append(results, fmt.Sprintf("ADD %s", op.Path))

			case "update":
				absPath, err := safePath(dir, op.Path)
				if err != nil {
					results = append(results, fmt.Sprintf("FAIL UPDATE %s: %v", op.Path, err))
					continue
				}
				_ = absPath

				// Try using the `patch` command.
				cmd := exec.Command("patch", "-p1", "--no-backup-if-mismatch")
				cmd.Dir = dir
				cmd.Stdin = strings.NewReader(op.Content)
				output, err := cmd.CombinedOutput()
				if err != nil {
					results = append(results, fmt.Sprintf("FAIL UPDATE %s: %s", op.Path, strings.TrimSpace(string(output))))
					continue
				}
				if cache := getContextCache(ctx); cache != nil {
					cache.Invalidate(op.Path)
				}
				results = append(results, fmt.Sprintf("UPDATE %s", op.Path))

			case "move":
				srcAbs, err := safePath(dir, op.Path)
				if err != nil {
					results = append(results, fmt.Sprintf("FAIL MOVE %s: %v", op.Path, err))
					continue
				}
				dstAbs, err := safePath(dir, op.NewPath)
				if err != nil {
					results = append(results, fmt.Sprintf("FAIL MOVE %s: %v", op.NewPath, err))
					continue
				}
				os.MkdirAll(filepath.Dir(dstAbs), 0o755)
				if err := os.Rename(srcAbs, dstAbs); err != nil {
					results = append(results, fmt.Sprintf("FAIL MOVE %s→%s: %v", op.Path, op.NewPath, err))
					continue
				}
				if cache := getContextCache(ctx); cache != nil {
					cache.Invalidate(op.Path)
					cache.Invalidate(op.NewPath)
				}
				results = append(results, fmt.Sprintf("MOVE %s → %s", op.Path, op.NewPath))

			case "delete":
				absPath, err := safePath(dir, op.Path)
				if err != nil {
					results = append(results, fmt.Sprintf("FAIL DELETE %s: %v", op.Path, err))
					continue
				}
				if err := os.Remove(absPath); err != nil {
					results = append(results, fmt.Sprintf("FAIL DELETE %s: %v", op.Path, err))
					continue
				}
				if cache := getContextCache(ctx); cache != nil {
					cache.Invalidate(op.Path)
				}
				results = append(results, fmt.Sprintf("DELETE %s", op.Path))
			}
		}

		return fmt.Sprintf("Patch applied (%d operations):\n%s", len(ops), strings.Join(results, "\n")), nil
	}
}
