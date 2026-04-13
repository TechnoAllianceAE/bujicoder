package app

import (
	"context"
	"fmt"
	"os"

	agentdata "github.com/TechnoAllianceAE/bujicoder/agents"
	cliconfig "github.com/TechnoAllianceAE/bujicoder/cli/config"
	"github.com/TechnoAllianceAE/bujicoder/shared/agent"
	"github.com/TechnoAllianceAE/bujicoder/shared/agentruntime"
	"github.com/TechnoAllianceAE/bujicoder/shared/costmode"
	"github.com/TechnoAllianceAE/bujicoder/shared/llm"
	"github.com/TechnoAllianceAE/bujicoder/shared/logging"
	"github.com/TechnoAllianceAE/bujicoder/shared/tools"
)

// RunNonInteractive runs a single prompt through the agent runtime without
// the Bubble Tea TUI. Streams output text to stdout and exits.
func RunNonInteractive(prompt string, verbose bool) error {
	log := logging.New(logging.Config{Verbose: verbose})

	ucfg := cliconfig.LoadUnifiedConfig()
	if ucfg == nil {
		return fmt.Errorf("no config found — run 'buji' interactively first to complete setup")
	}

	// Load agents
	agentReg := agent.NewRegistry()
	if dir := ucfg.GetAgentsDir(); dir != "" {
		_ = agentReg.LoadDir(dir)
	}
	if len(agentReg.List()) == 0 {
		entries, _ := agentdata.FS.ReadDir(".")
		for _, e := range entries {
			if e.IsDir() {
				continue
			}
			data, err := agentdata.FS.ReadFile(e.Name())
			if err != nil {
				continue
			}
			def, err := agent.LoadBytes(data, e.Name())
			if err != nil {
				continue
			}
			agentReg.Register(def)
		}
	}

	agentDef, ok := agentReg.Get("base")
	if !ok {
		return fmt.Errorf("base agent not found")
	}

	// Model resolution
	mode := costmode.ModeNormal
	if ucfg.CostMode != "" && ucfg.CostMode != "plan" {
		mode = costmode.ParseMode(ucfg.CostMode)
	}
	var resolver *costmode.Resolver
	if len(ucfg.Modes) > 0 {
		resolver = costmode.NewResolverFromConfig(ucfg.ToModelConfig())
	}
	if resolver != nil {
		agentDef = agentDef.WithCostMode(mode, resolver)
	}

	// Build registries
	legacyCfg := ucfg.ToLegacyConfig()
	llmReg := llm.NewRegistry()
	registerLocalProviders(llmReg, ucfg)
	_ = legacyCfg // providers are registered from ucfg directly

	cwd, _ := os.Getwd()
	toolReg := tools.NewRegistry(cwd)

	rt := agentruntime.New(llmReg, toolReg, agentReg, log)

	ctx := context.Background()
	runCfg := agentruntime.RunConfig{
		AgentDef:      agentDef,
		UserMessage:   prompt,
		ProjectRoot:   cwd,
		CostMode:      mode,
		ModelResolver: resolver,
		OnEvent: func(ev agentruntime.Event) {
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
	}

	result, err := rt.Run(ctx, runCfg)
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
