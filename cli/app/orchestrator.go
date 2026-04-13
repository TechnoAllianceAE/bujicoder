package app

import (
	"context"
	"fmt"
	"os"

	"github.com/rs/zerolog"

	agentdata "github.com/TechnoAllianceAE/bujicoder/agents"
	cliconfig "github.com/TechnoAllianceAE/bujicoder/cli/config"
	"github.com/TechnoAllianceAE/bujicoder/shared/agent"
	"github.com/TechnoAllianceAE/bujicoder/shared/agentruntime"
	"github.com/TechnoAllianceAE/bujicoder/shared/costmode"
	"github.com/TechnoAllianceAE/bujicoder/shared/hooks"
	"github.com/TechnoAllianceAE/bujicoder/shared/llm"
	"github.com/TechnoAllianceAE/bujicoder/shared/logging"
	"github.com/TechnoAllianceAE/bujicoder/shared/memory"
	"github.com/TechnoAllianceAE/bujicoder/shared/mcp"
	"github.com/TechnoAllianceAE/bujicoder/shared/tools"
)

// AgentOrchestrator wraps the agent runtime and all its dependencies into a
// reusable unit that can be shared between the TUI and GUI frontends.
// It owns the runtime lifecycle, provider registration, tool registry, and
// MCP server management.
type AgentOrchestrator struct {
	Runtime       *agentruntime.Runtime
	AgentRegistry *agent.Registry
	ToolRegistry  *tools.Registry
	LLMRegistry   *llm.Registry
	MCPManager    *mcp.Manager
	ModelResolver *costmode.Resolver
	HookMgr       *hooks.Manager
	MemoryStore   *memory.Store
	Log           zerolog.Logger

	// Channels for interactive tool features (ask_user, approval)
	AskQuestionCh  chan string
	AskAnswerCh    chan string
	ApprovalCmdCh  chan string
	ApprovalRespCh chan bool
}

// OrchestratorConfig holds the configuration for creating an orchestrator.
type OrchestratorConfig struct {
	UnifiedCfg  *cliconfig.UnifiedConfig
	CostMode    costmode.Mode
	PlanMode    bool
	Verbose     bool
	ProjectRoot string // working directory

	// Optional callbacks for interactive tools. If nil, tools that need
	// user input will use channels (for TUI) or can be set by GUI.
	UserPrompt func(question string) (string, error)
	Approval   func(command, reason string) (bool, error)
}

// NewOrchestrator creates a fully initialized AgentOrchestrator ready to run
// agent prompts. This is the shared entry point for both TUI and GUI.
func NewOrchestrator(cfg OrchestratorConfig) (*AgentOrchestrator, error) {
	log := logging.New(logging.Config{Verbose: cfg.Verbose})
	log.Info().
		Str("cost_mode", string(cfg.CostMode)).
		Bool("plan_mode", cfg.PlanMode).
		Msg("orchestrator started")

	o := &AgentOrchestrator{
		Log:            log,
		AskQuestionCh:  make(chan string, 1),
		AskAnswerCh:    make(chan string, 1),
		ApprovalCmdCh:  make(chan string, 1),
		ApprovalRespCh: make(chan bool, 1),
	}

	ucfg := cfg.UnifiedCfg

	// Load agents
	o.AgentRegistry = agent.NewRegistry()
	if ucfg != nil {
		if dir := ucfg.GetAgentsDir(); dir != "" {
			_ = o.AgentRegistry.LoadDir(dir)
		}
	}
	if len(o.AgentRegistry.List()) == 0 {
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
			o.AgentRegistry.Register(def)
		}
	}

	// Model resolution
	if ucfg != nil && len(ucfg.Modes) > 0 {
		o.ModelResolver = costmode.NewResolverFromConfig(ucfg.ToModelConfig())
	}

	// Tool registry
	cwd := cfg.ProjectRoot
	if cwd == "" {
		cwd, _ = os.Getwd()
	}

	userPrompt := cfg.UserPrompt
	if userPrompt == nil {
		userPrompt = func(question string) (string, error) {
			o.AskQuestionCh <- question
			answer := <-o.AskAnswerCh
			return answer, nil
		}
	}
	approval := cfg.Approval
	if approval == nil {
		approval = func(command, reason string) (bool, error) {
			o.ApprovalCmdCh <- command + "\n" + reason
			approved := <-o.ApprovalRespCh
			return approved, nil
		}
	}

	perms := tools.LoadProjectPermissions(cwd)
	o.ToolRegistry = tools.NewRegistry(cwd, tools.RegistryOpts{
		UserPrompt:  userPrompt,
		Approval:    approval,
		Permissions: perms,
	})

	// MCP servers
	if ucfg != nil && len(ucfg.MCPServers) > 0 {
		var mcpConfigs []mcp.ServerConfig
		for _, s := range ucfg.MCPServers {
			mcpConfigs = append(mcpConfigs, mcp.ServerConfig{
				Name:    s.Name,
				Command: s.Command,
				Args:    s.Args,
				Lazy:    s.Lazy,
			})
		}
		o.MCPManager = mcp.NewManager(mcpConfigs)
		_ = o.MCPManager.RegisterTools(o.ToolRegistry)
	}

	// LLM providers
	o.LLMRegistry = llm.NewRegistry()
	registerLocalProviders(o.LLMRegistry, ucfg)

	// Hooks
	home, _ := os.UserHomeDir()
	configDir := home + "/.bujicoder"
	o.HookMgr = hooks.NewManagerFromConfigDir(configDir, cwd)

	// Memory
	o.MemoryStore = memory.NewStore(configDir, cwd)

	// Create the runtime
	o.Runtime = agentruntime.New(o.LLMRegistry, o.ToolRegistry, o.AgentRegistry, log)

	return o, nil
}

// HasProviders returns true if at least one LLM provider is registered.
func (o *AgentOrchestrator) HasProviders() bool {
	return o.LLMRegistry != nil && o.LLMRegistry.HasProviders()
}

// RunPrompt executes a single prompt through the base agent and calls onEvent
// for each runtime event. This is the primary interface for both TUI and GUI.
func (o *AgentOrchestrator) RunPrompt(
	ctx context.Context,
	prompt string,
	history []llm.Message,
	mode costmode.Mode,
	planMode bool,
	onEvent func(agentruntime.Event),
) (*agentruntime.RunResult, error) {
	agentDef, ok := o.AgentRegistry.Get("base")
	if !ok {
		return nil, fmt.Errorf("base agent not found in registry")
	}

	if mode != "" && o.ModelResolver != nil {
		agentDef = agentDef.WithCostMode(mode, o.ModelResolver)
	}

	if planMode {
		prompt = "[PLAN MODE] You are in documentation-only mode. Do NOT modify any source code files. " +
			"You may: READ any files for understanding, CREATE or MODIFY only .md files, analyze code, write plans and documentation. " +
			"Do not use write_file or str_replace on non-.md files.\n\n" + prompt
	}

	cwd, _ := os.Getwd()

	runCfg := agentruntime.RunConfig{
		AgentDef:      agentDef,
		UserMessage:   prompt,
		History:       history,
		ProjectRoot:   cwd,
		CostMode:      mode,
		ModelResolver: o.ModelResolver,
		HookManager:   o.HookMgr,
		SessionMemory: o.MemoryStore,
		OnEvent:       onEvent,
	}

	return o.Runtime.Run(ctx, runCfg)
}

// Shutdown cleans up all resources.
func (o *AgentOrchestrator) Shutdown() {
	if o.MCPManager != nil {
		o.MCPManager.ShutdownAll()
	}
}

// BuildRunConfig creates a RunConfig for direct use. This gives callers
// full control over the run configuration (e.g. for /goal coordinator).
func (o *AgentOrchestrator) BuildRunConfig(
	agentDef *agent.Definition,
	userMessage string,
	history []llm.Message,
	mode costmode.Mode,
	onEvent func(agentruntime.Event),
) agentruntime.RunConfig {
	cwd, _ := os.Getwd()
	return agentruntime.RunConfig{
		AgentDef:      agentDef,
		UserMessage:   userMessage,
		History:       history,
		ProjectRoot:   cwd,
		CostMode:      mode,
		ModelResolver: o.ModelResolver,
		HookManager:   o.HookMgr,
		SessionMemory: o.MemoryStore,
		OnEvent:       onEvent,
	}
}
