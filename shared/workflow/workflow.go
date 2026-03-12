// Package workflow provides a multi-agent workflow engine that lets users
// compose and execute pipelines of agent steps as YAML-defined workflows.
// Workflows support sequential and parallel execution, variable interpolation,
// conditional steps, and approval gates.
package workflow

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// Workflow defines a multi-agent pipeline.
type Workflow struct {
	ID          string `yaml:"id" json:"id"`
	DisplayName string `yaml:"display_name" json:"display_name"`
	Description string `yaml:"description" json:"description"`
	Steps       []Step `yaml:"steps" json:"steps"`
}

// Step defines a single step in a workflow pipeline.
type Step struct {
	// Agent is the ID of the agent to invoke (e.g., "researcher", "editor").
	Agent string `yaml:"agent" json:"agent"`
	// Task is the task template with {{variable}} interpolation.
	Task string `yaml:"task" json:"task"`
	// OutputVar is the variable name to store this step's output.
	OutputVar string `yaml:"output_var" json:"output_var"`
	// RequireApproval pauses execution and asks the user before proceeding.
	RequireApproval bool `yaml:"require_approval" json:"require_approval"`
	// Condition is an expression to evaluate. If false, the step is skipped.
	// Example: "{{review}} contains 'NEEDS_CHANGES'"
	Condition string `yaml:"condition" json:"condition"`
	// Parallel is a list of sub-steps to execute concurrently.
	Parallel []Step `yaml:"parallel,omitempty" json:"parallel,omitempty"`
}

// Validate checks the workflow for basic correctness.
func (w *Workflow) Validate() error {
	if w.ID == "" {
		return fmt.Errorf("workflow ID is required")
	}
	if len(w.Steps) == 0 {
		return fmt.Errorf("workflow %q has no steps", w.ID)
	}
	for i, step := range w.Steps {
		if step.Agent == "" && len(step.Parallel) == 0 {
			return fmt.Errorf("step %d in workflow %q: agent is required (or use parallel)", i, w.ID)
		}
		for j, pStep := range step.Parallel {
			if pStep.Agent == "" {
				return fmt.Errorf("step %d parallel[%d] in workflow %q: agent is required", i, j, w.ID)
			}
		}
	}
	return nil
}

// Registry holds loaded workflows.
type Registry struct {
	workflows map[string]*Workflow
}

// NewRegistry creates an empty workflow registry.
func NewRegistry() *Registry {
	return &Registry{workflows: make(map[string]*Workflow)}
}

// LoadDir loads all workflow YAML files from a directory.
func (r *Registry) LoadDir(dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil // No workflows directory is OK
		}
		return err
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		ext := strings.ToLower(filepath.Ext(entry.Name()))
		if ext != ".yaml" && ext != ".yml" {
			continue
		}
		if err := r.LoadFile(filepath.Join(dir, entry.Name())); err != nil {
			return fmt.Errorf("load workflow %s: %w", entry.Name(), err)
		}
	}
	return nil
}

// LoadFile loads a single workflow YAML file.
func (r *Registry) LoadFile(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return r.LoadBytes(data, path)
}

// LoadBytes parses workflow YAML from raw bytes.
func (r *Registry) LoadBytes(data []byte, source string) error {
	var wf Workflow
	if err := yaml.Unmarshal(data, &wf); err != nil {
		return fmt.Errorf("parse workflow from %s: %w", source, err)
	}
	if err := wf.Validate(); err != nil {
		return err
	}
	r.workflows[wf.ID] = &wf
	return nil
}

// Get retrieves a workflow by ID.
func (r *Registry) Get(id string) (*Workflow, bool) {
	wf, ok := r.workflows[id]
	return wf, ok
}

// Register adds or replaces a workflow.
func (r *Registry) Register(wf *Workflow) {
	r.workflows[wf.ID] = wf
}

// List returns all workflow IDs in alphabetical order.
func (r *Registry) List() []string {
	ids := make([]string, 0, len(r.workflows))
	for id := range r.workflows {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// ListWorkflows returns all workflows with their metadata.
func (r *Registry) ListWorkflows() []*Workflow {
	wfs := make([]*Workflow, 0, len(r.workflows))
	for _, wf := range r.workflows {
		wfs = append(wfs, wf)
	}
	sort.Slice(wfs, func(i, j int) bool {
		return wfs[i].ID < wfs[j].ID
	})
	return wfs
}
