package agentruntime

import "testing"

func TestNormalizeTaskIDs(t *testing.T) {
	tests := []struct {
		name string
		in   []CoordinatorTask
		want []string
	}{
		{
			name: "already unique ids are untouched",
			in:   []CoordinatorTask{{ID: "t1"}, {ID: "t2"}},
			want: []string{"t1", "t2"},
		},
		{
			name: "duplicate ids are made unique",
			in:   []CoordinatorTask{{ID: "t1"}, {ID: "t1"}, {ID: "t1"}},
			want: []string{"t1", "t1_1", "t1_2"},
		},
		{
			name: "blank ids are filled",
			in:   []CoordinatorTask{{ID: ""}, {ID: ""}, {ID: "t_1"}},
			want: []string{"t_1", "t_2", "t_1_1"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			normalizeTaskIDs(tt.in)

			seen := make(map[string]bool)
			for i, task := range tt.in {
				if task.ID == "" {
					t.Fatalf("task %d still has a blank id", i)
				}
				if seen[task.ID] {
					t.Fatalf("duplicate id %q survived normalization", task.ID)
				}
				seen[task.ID] = true
				if task.ID != tt.want[i] {
					t.Errorf("task %d id = %q, want %q", i, task.ID, tt.want[i])
				}
			}
		})
	}
}

// Duplicate planner IDs used to make Kahn's algorithm visit fewer nodes than
// there are tasks, aborting the whole goal with a bogus cycle error.
func TestDetectCyclesAfterNormalization(t *testing.T) {
	tasks := []CoordinatorTask{
		{ID: "t1"},
		{ID: "t1", DependsOn: []string{"t1"}},
		{ID: "t2", DependsOn: []string{"t1"}},
	}

	normalizeTaskIDs(tasks)

	if err := detectCycles(tasks); err != nil {
		t.Fatalf("unexpected cycle error after normalization: %v", err)
	}
}

func TestDetectCyclesRealCycle(t *testing.T) {
	tasks := []CoordinatorTask{
		{ID: "t1", DependsOn: []string{"t2"}},
		{ID: "t2", DependsOn: []string{"t1"}},
	}
	if err := detectCycles(tasks); err == nil {
		t.Fatal("expected a cycle to be detected")
	}
}
