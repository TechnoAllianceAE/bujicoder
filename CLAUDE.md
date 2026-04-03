# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What is This Repository?

BujiCoder is an open-source, multi-agent AI coding assistant that runs in the terminal. It's a Go CLI tool (`buji`) with a Bubble Tea TUI that orchestrates specialized LLM agents to solve coding tasks. Users bring their own API keys; no code leaves their machine.

**Module:** `github.com/TechnoAllianceAE/bujicoder`

## Build & Development Commands

```bash
make build              # Build CLI binary to bin/buji
make install            # Build + install to ~/.local/bin/buji
make test               # go test ./... -race
make test-coverage      # Tests + HTML coverage report
make lint               # golangci-lint run ./...
make fmt                # gofmt + goimports
make dist               # Cross-compile for darwin/linux/windows (amd64+arm64)
make release VERSION=x.y.z  # dist + GitHub release via gh CLI

# Run a single test
go test -race -run TestFunctionName ./shared/agent/

# Run tests for a specific package
go test -race ./shared/agentruntime/
```

## Architecture

### Core Loop

```
CLI TUI (Bubble Tea) → Agent Runtime (step loop) → LLM Provider (streaming) → Tool Execution → repeat
```

The agent runtime (`shared/agentruntime/`) executes agents step-by-step: send context to LLM, parse tool calls from response, execute tools, append results, repeat until the agent signals completion or hits limits (max steps, max tokens, 200K input token budget). A loop guard detects 10+ identical consecutive tool calls.

### Agent System

Agents are YAML-defined in `agents/`. Each specifies: `id`, `model`, `tools`, `spawnable_agents`, `max_steps` (20-50), `max_tokens` (4096-8192), and `system_prompt`. Key agents:
- **base** — main orchestrator, can spawn all other agents
- **editor** / **parallel_editor** — surgical file editing via `str_replace`
- **file_explorer** — fast codebase navigation (cheapest model)
- **thinker** — deep reasoning for complex problems
- **researcher** — thorough investigation with web/code search
- **planner** — task decomposition
- **reviewer** / **ui_reviewer** / **judge** — code review and evaluation
- **git_committer** — git operations
- **implementor** — code implementation

Sub-agents do NOT inherit parent models — each has its own `model:` field. Cost mode can override models dynamically via `model_config.yaml`.

### Cost Mode & Model Resolution

`shared/costmode/` resolves which model each agent uses based on the active cost mode (normal/heavy/max). Mappings live in `model_config.yaml` with per-agent overrides via `agent_overrides`. The resolver maps agent roles (main, file_explorer, sub_agent) to specific models per mode.

### LLM Providers

`shared/llm/` implements a streaming `Provider` interface for 15+ vendors: Anthropic, OpenAI, Groq, Qwen, DeepSeek, XAI, Together, Gemini, OpenRouter, and more. Provider/model notation: `openai/gpt-4o`, `anthropic/claude-sonnet-4`. Tracks per-request usage (tokens, cost in cents).

### Tool System

`shared/tools/` provides ~20 built-in tools: `read_files`, `write_file`, `str_replace`, `glob`, `code_search`, `run_terminal_command`, `spawn_agents`, `web_search`, `think_deeply`, memory tools, etc. Supports plan mode (read-only except .md). MCP server tools are discovered and registered dynamically.

### Key Subsystems

| Package | Purpose |
|---------|---------|
| `shared/mcp/` | MCP server management (eager + lazy startup), tool discovery |
| `shared/codeintel/` | Multi-language symbol extraction (Go, Python, Rust, TS) for project understanding |
| `shared/contextcache/` | TTL-based file content cache (30s, 1MB limit) with mtime invalidation |
| `shared/smartctx/` | Keyword-based file relevance ranking for context selection |
| `shared/store/` | Conversation persistence via bbolt + Bleve full-text search |
| `shared/snapshot/` | Shadow git repo in `.bujicoder/snapshots/` for safe per-step revert |
| `shared/workflow/` | YAML-defined multi-agent pipelines (sequential/parallel, variables, approval gates) |
| `shared/logging/` | Structured JSON logging to `~/.bujicoder/logs/` with rotation |
| `shared/lsp/` | LSP client for post-edit diagnostics |
| `shared/selfupdate/` | CLI self-update logic |
| `shared/errutil/` | `Result[T]` pattern (success/failure) |

### CLI / TUI

`cli/app/model.go` is the Bubble Tea state machine with modes: StateChat, StateHistory, StateSetup. Slash commands (`/new`, `/mode`, `/history`, `/goal`, `/mcp`, `/models`, `/refresh`). Agent runs are async with streaming events (delta, tool_call, tool_result, step_start, step_end, complete, error).

Config file: `~/.bujicoder/bujicoder.yaml` (cost_mode, API keys, agents_dir, model_config_path). Env var overrides supported.

## Adding a New LLM Provider

1. Implement the `Provider` interface in `shared/llm/<provider>.go`
2. Register in the provider factory in `shared/llm/provider.go`
3. Add API key field to `cli/config/config.go` (`APIKeysConfig`)
4. Add env var fallback in `GetAPIKey()`
5. Register in `cli/app/model.go` (`registerLocalProviders()`)

## Adding a New Tool

1. Add tool definition in `shared/tools/tools.go`
2. Register in `NewRegistry()`
3. Add to relevant agent YAML files in `agents/`

## Naming Conventions

- **Product:** BujiCoder | **Binary:** `buji` | **Config dir:** `~/.bujicoder/`
- **Env vars:** `BUJICODER_*`
- **Commits:** conventional — `feat:`, `fix:`, `docs:`, `test:`, `chore:`, `refactor:`

## Go Style

- Go 1.24+, `gofmt` + `golangci-lint`, [Uber Go Style Guide](https://github.com/uber-go/guide/blob/master/style.md)
- Structured logging: `zerolog`
- Tests: `*_test.go` alongside code, `t.TempDir()` for fixtures
- Key patterns: registry (agents, tools, providers), event streaming, YAML-driven config
