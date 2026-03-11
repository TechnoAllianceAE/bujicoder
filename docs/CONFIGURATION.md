# BujiCoder Configuration Reference

## Config File

Location: `~/.bujicoder/bujicoder.yaml`

This file is created automatically by the setup wizard on first run. You can edit it manually at any time.

```yaml
mode: local
cost_mode: normal

api_keys:
  openrouter: sk-or-...          # OpenRouter (access 200+ models)
  anthropic: sk-ant-...           # Anthropic (Claude)
  openai: sk-...                  # OpenAI (GPT-4o)
  google_ai: AI...                # Google AI (Gemini)
  xai: xai-...                    # xAI (Grok)
  groq: gsk_...                   # Groq
  cerebras: csk-...               # Cerebras
  together: ...                   # Together AI
  ollama_url: http://localhost:11434  # Ollama (local models)

modes:
  normal:
    main: x-ai/grok-code-fast-1
    file_explorer: openai/gpt-4o-mini
    sub_agent: z-ai/glm-5
  heavy:
    main: qwen/qwen3.5-35b-a3b
    file_explorer: openai/gpt-4o-mini
    sub_agent: z-ai/glm-5
  max:
    main: minimax/minimax-m2.5
    file_explorer: openai/gpt-4o-mini
    sub_agent: z-ai/glm-5

mcp_servers:
  - name: my-server
    command: npx
    args: ["-y", "@my/mcp-server"]
    lazy: true
```

## Cost Modes

BujiCoder supports three cost modes that control which models are used:

| Mode | Purpose |
|------|---------|
| **normal** | Everyday coding — fast, cheap models |
| **heavy** | Complex tasks — smarter models, higher cost |
| **max** | Maximum quality — best models, parallel evolution for edits |

Switch modes in the TUI with `/mode normal`, `/mode heavy`, or `/mode max`.

## Model Roles

Each cost mode assigns models to three roles:

| Role | Description |
|------|-------------|
| **main** | Primary model for the orchestrator agent |
| **file_explorer** | Lightweight model for codebase navigation |
| **sub_agent** | Model used by specialized sub-agents (editor, planner, researcher, etc.) |

### Per-Agent Overrides

You can override the model for a specific agent within a cost mode using `agent_overrides`:

```yaml
modes:
  max:
    main: openai/gpt-oss-120b:free
    file_explorer: openai/gpt-4o-mini
    sub_agent: openai/gpt-oss-120b:free
    agent_overrides:
      editor: openai/gpt-oss-120b:free
      planner: qwen/qwen3-235b-a22b
      researcher: google/gemini-2.5-pro-preview
```

## Environment Variables

| Variable | Description |
|----------|-------------|
| `BUJICODER_CONFIG_DIR` | Config directory (default: `~/.bujicoder`) |
| `BUJICODER_AGENTS_DIR` | Custom agents directory |
| `BUJICODER_ENABLE_MOUSE` | Enable mouse capture in TUI |
| `BUJICODER_DISABLE_UPDATE_CHECK` | Skip update check on launch |
| `OPENROUTER_API_KEY` | OpenRouter API key (fallback if not in config) |
| `ANTHROPIC_API_KEY` | Anthropic API key (fallback) |
| `OPENAI_API_KEY` | OpenAI API key (fallback) |

## Agent Architecture

BujiCoder uses a multi-agent system. Each agent has a specialized role, its own tools, and a model assignment:

| Agent | Role | Tools |
|-------|------|-------|
| **base** | Orchestrator — routes tasks to sub-agents | All tools + spawns sub-agents |
| **editor** | Precise file modifications | edit_file, write_file, read_files |
| **file_explorer** | Fast codebase navigation | read_files, glob, list_directory |
| **planner** | Task decomposition and planning | read_files, code_search, think_deeply |
| **researcher** | Deep research and analysis | read_files, web_search, think_deeply |
| **reviewer** | Code quality evaluation | read_files, code_search, glob |
| **thinker** | Pure reasoning (no file access) | think_deeply |
| **git_committer** | Git staging and commits | run_terminal_command, read_files |
| **parallel_editor** | Max-mode parallel implementation | spawn_agents, apply_proposals |

Agent definitions live in `agents/*.yaml`. You can customize them or add your own.

## MCP Servers

BujiCoder supports [Model Context Protocol](https://modelcontextprotocol.io/) servers for extending tool capabilities:

```yaml
mcp_servers:
  - name: filesystem
    command: npx
    args: ["-y", "@modelcontextprotocol/server-filesystem", "/path/to/dir"]
    lazy: true    # Only start when a tool from this server is needed

  - name: github
    command: npx
    args: ["-y", "@modelcontextprotocol/server-github"]
    env:
      GITHUB_TOKEN: ghp_...
```

## Project-Level Permissions

Create a `.bujicoder/permissions.yaml` file in your project root (or globally
at `~/.bujicoder/permissions.yaml`) to set permissions:

```yaml
mode: ask    # ask | yolo | strict
tools:
  write_file: allow
  run_terminal_command: ask
commands:
  - pattern: "go test*"
    action: allow
  - pattern: "npm run*"
    action: allow
  - pattern: "git push --force*"
    action: deny
restricted_paths:
  - ".env"
  - "**/*.pem"
```

**Lookup order** (highest priority first):
1. `<project>/.bujicoder/permissions.yaml`
2. `~/.bujicoder/permissions.yaml` (global default)
3. `<project>/.bujicoderrc` (legacy, still supported)

## Conversations

Chat history is saved to `~/.bujicoder/conversations/` as JSON files. Browse them with `/history` in the TUI.

## Ollama (Local Models)

To use local models via Ollama:

1. Install [Ollama](https://ollama.ai)
2. Pull a model: `ollama pull llama3`
3. Set in config:
   ```yaml
   api_keys:
     ollama_url: http://localhost:11434
   modes:
     normal:
       main: ollama/llama3
   ```
