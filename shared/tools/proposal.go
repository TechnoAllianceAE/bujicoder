package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"

	"github.com/pmezard/go-difflib/difflib"
)

// ProposedChange represents a file change proposed by an implementor agent.
// These changes are NOT written to disk — they accumulate in a ProposalCollector
// and are only applied after a judge selects the winning implementation.
type ProposedChange struct {
	Path     string `json:"path"`
	Type     string `json:"type"` // "edit" or "write_file"
	OldStr   string `json:"old_str,omitempty"`
	NewStr   string `json:"new_str,omitempty"`
	Content  string `json:"content,omitempty"`
	DiffText string `json:"diff_text"`
}

// ProposalCollector is a thread-safe accumulator for proposed file changes.
type ProposalCollector struct {
	mu      sync.Mutex
	changes []ProposedChange
}

// NewProposalCollector creates an empty ProposalCollector.
func NewProposalCollector() *ProposalCollector {
	return &ProposalCollector{}
}

// Add appends a proposed change.
func (pc *ProposalCollector) Add(c ProposedChange) {
	pc.mu.Lock()
	defer pc.mu.Unlock()
	pc.changes = append(pc.changes, c)
}

// Changes returns a snapshot of all collected proposals.
func (pc *ProposalCollector) Changes() []ProposedChange {
	pc.mu.Lock()
	defer pc.mu.Unlock()
	out := make([]ProposedChange, len(pc.changes))
	copy(out, pc.changes)
	return out
}

// --- Context key helpers ---

const proposalCtxKey contextKey = "proposal_collector"

// WithProposalCollector returns a child context carrying a ProposalCollector.
func WithProposalCollector(ctx context.Context, pc *ProposalCollector) context.Context {
	return context.WithValue(ctx, proposalCtxKey, pc)
}

// GetProposalCollector retrieves the ProposalCollector from context, or nil.
func GetProposalCollector(ctx context.Context) *ProposalCollector {
	pc, _ := ctx.Value(proposalCtxKey).(*ProposalCollector)
	return pc
}

// --- propose_edit tool ---

func proposeEdit(workDir string) func(ctx context.Context, args json.RawMessage) (string, error) {
	return func(ctx context.Context, args json.RawMessage) (string, error) {
		var params struct {
			Path   string `json:"path"`
			OldStr string `json:"old_str"`
			NewStr string `json:"new_str"`
		}
		if err := json.Unmarshal(args, &params); err != nil {
			return "", err
		}

		pc := GetProposalCollector(ctx)
		if pc == nil {
			return "", fmt.Errorf("propose_edit requires a ProposalCollector in context")
		}

		wd := effectiveWorkDir(ctx, workDir)
		absPath, err := SafePath(wd, params.Path)
		if err != nil {
			return "", err
		}

		data, err := os.ReadFile(absPath)
		if err != nil {
			return "", err
		}
		content := string(data)

		if !strings.Contains(content, params.OldStr) {
			return "", fmt.Errorf("old_str not found in %s", params.Path)
		}

		newContent := strings.Replace(content, params.OldStr, params.NewStr, 1)
		diff, err := unifiedDiff(params.Path, content, newContent)
		if err != nil {
			return "", fmt.Errorf("compute diff: %w", err)
		}

		pc.Add(ProposedChange{
			Path:     params.Path,
			Type:     "edit",
			OldStr:   params.OldStr,
			NewStr:   params.NewStr,
			DiffText: diff,
		})

		return fmt.Sprintf("Proposed edit for %s:\n%s", params.Path, diff), nil
	}
}

// --- propose_write_file tool ---

func proposeWriteFile(workDir string) func(ctx context.Context, args json.RawMessage) (string, error) {
	return func(ctx context.Context, args json.RawMessage) (string, error) {
		var params struct {
			Path    string `json:"path"`
			Content string `json:"content"`
		}
		if err := json.Unmarshal(args, &params); err != nil {
			return "", err
		}

		pc := GetProposalCollector(ctx)
		if pc == nil {
			return "", fmt.Errorf("propose_write_file requires a ProposalCollector in context")
		}

		wd := effectiveWorkDir(ctx, workDir)
		absPath, err := SafePath(wd, params.Path)
		if err != nil {
			return "", err
		}

		// Read existing content if the file exists (for diff).
		var oldContent string
		if data, err := os.ReadFile(absPath); err == nil {
			oldContent = string(data)
		}

		diff, err := unifiedDiff(params.Path, oldContent, params.Content)
		if err != nil {
			return "", fmt.Errorf("compute diff: %w", err)
		}

		pc.Add(ProposedChange{
			Path:     params.Path,
			Type:     "write_file",
			Content:  params.Content,
			DiffText: diff,
		})

		return fmt.Sprintf("Proposed write_file for %s:\n%s", params.Path, diff), nil
	}
}

// --- Diff helper ---

// unifiedDiff produces a unified diff between two strings.
func unifiedDiff(filename, a, b string) (string, error) {
	diff := difflib.UnifiedDiff{
		A:        difflib.SplitLines(a),
		B:        difflib.SplitLines(b),
		FromFile: "a/" + filename,
		ToFile:   "b/" + filename,
		Context:  3,
	}
	return difflib.GetUnifiedDiffString(diff)
}

// SafePath is the exported version of safePath for use by other packages.
func SafePath(workDir, path string) (string, error) {
	return safePath(workDir, path)
}
