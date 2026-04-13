package app

import (
	"context"
	"fmt"
	"os"

	cliconfig "github.com/TechnoAllianceAE/bujicoder/cli/config"
	"github.com/TechnoAllianceAE/bujicoder/shared/agentruntime"
	"github.com/TechnoAllianceAE/bujicoder/shared/costmode"
)

// RunNonInteractive runs a single prompt through the agent runtime without
// the Bubble Tea TUI. Streams output text to stdout and exits.
// Uses the shared AgentOrchestrator so behavior matches the TUI.
func RunNonInteractive(prompt string, verbose bool) error {
	ucfg := cliconfig.LoadUnifiedConfig()
	if ucfg == nil {
		return fmt.Errorf("no config found — run 'buji' interactively first to complete setup")
	}

	mode := costmode.ModeNormal
	if ucfg.CostMode != "" && ucfg.CostMode != "plan" {
		mode = costmode.ParseMode(ucfg.CostMode)
	}

	orch, err := NewOrchestrator(OrchestratorConfig{
		UnifiedCfg: ucfg,
		CostMode:   mode,
		PlanMode:   ucfg.CostMode == "plan",
		Verbose:    verbose,
	})
	if err != nil {
		return fmt.Errorf("init orchestrator: %w", err)
	}
	defer orch.Shutdown()

	if !orch.HasProviders() {
		return fmt.Errorf("no LLM providers configured — add API keys to config or set env vars (e.g. OPENROUTER_API_KEY)")
	}

	ctx := context.Background()
	result, err := orch.RunPrompt(ctx, prompt, nil, mode, ucfg.CostMode == "plan",
		func(ev agentruntime.Event) {
			switch ev.Type {
			case agentruntime.EventDelta:
				fmt.Print(ev.Text)
			case agentruntime.EventToolCall:
				if verbose {
					fmt.Fprintf(os.Stderr, "\n[tool: %s]\n", ev.ToolName)
				}
			case agentruntime.EventError:
				fmt.Fprintf(os.Stderr, "\nError: %s\n", ev.Text)
			}
		},
	)
	if err != nil {
		return err
	}

	fmt.Println()

	if verbose && result != nil {
		fmt.Fprintf(os.Stderr, "\n[%d steps, %d input tokens, %d output tokens]\n",
			result.TotalSteps, result.TotalInputTokens, result.TotalOutputTokens)
	}

	return nil
}
