# BujiCoder

**AI coding assistant that runs in your terminal. Bring your own API keys.**

BujiCoder is a multi-agent AI coding tool with a beautiful terminal UI. It reads your codebase, runs tools locally, and streams responses from any major LLM provider — all without sending your code to a third-party server.

## Features

- **Multi-provider**: Anthropic, OpenAI, Google Gemini, xAI, OpenRouter, Groq, Together, Cerebras, Ollama, and more
- **Multi-agent**: YAML-defined agents with specialized roles (editor, planner, researcher, reviewer, git committer)
- **Local tools**: File read/write, code search, terminal commands — all executed on your machine
- **MCP support**: Extend with Model Context Protocol servers for custom tool integrations
- **Local conversations**: Chat history saved to `~/.bujicoder/conversations/` as JSON files
- **Cost modes**: Switch between normal/heavy/max to control model selection and spending
- **Vision**: Attach images with `@path/to/image.png` in your messages
- **Self-updating**: `buji update` to get the latest version

## Quick Start

### Install (macOS/Linux)

```bash
curl -fsSL https://raw.githubusercontent.com/TechnoAllianceAE/bujicoder/main/scripts/install.sh | bash
```

### Install from source

```bash
go install github.com/TechnoAllianceAE/bujicoder/cli/cmd/buji@latest
```

### First run

```bash
buji
```

On first launch, BujiCoder walks you through provider selection and API key setup. Your config is saved to `~/.bujicoder/bujicoder.yaml`.

## Configuration

### Config file: `~/.bujicoder/bujicoder.yaml`

```yaml
mode: local
cost_mode: normal

api_keys:
  openrouter: sk-or-...     # OpenRouter (access 200+ models)
  anthropic: sk-ant-...      # Anthropic (Claude)
  openai: sk-...             # OpenAI (GPT-4o)
  google_ai: AI...           # Google AI (Gemini)
  xai: xai-...               # xAI (Grok)
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

### Environment variables

| Variable | Description |
|----------|-------------|
| `BUJICODER_CONFIG_DIR` | Config directory (default: `~/.bujicoder`) |
| `BUJICODER_AGENTS_DIR` | Custom agents directory |
| `BUJICODER_ENABLE_MOUSE` | Enable mouse capture in TUI |
| `BUJICODER_DISABLE_UPDATE_CHECK` | Skip update check |
| `OPENROUTER_API_KEY` | OpenRouter API key (fallback) |
| `ANTHROPIC_API_KEY` | Anthropic API key (fallback) |
| `OPENAI_API_KEY` | OpenAI API key (fallback) |

## TUI Commands

| Command | Description |
|---------|-------------|
| `/new` | Start a new conversation |
| `/mode <mode>` | Switch mode (normal / heavy / max / plan) |
| `/history` | Browse and resume past conversations |
| `/models` | List available models and mode mappings |
| `/refresh` | Refresh model-agent assignments |
| `/copy` | Copy last response to clipboard |
| `/init` | Analyze project docs and explain codebase |
| `/about` | Show version and system info |
| `/update` | Check for updates |
| `/help` | Show help and keyboard shortcuts |

## Agent Architecture

BujiCoder uses a multi-agent system where each agent has a specialized role:

| Agent | Role | Default Model |
|-------|------|---------------|
| **base** | Orchestrator — routes tasks to sub-agents | Configurable via cost mode |
| **editor** | File editing specialist | Sub-agent model |
| **file_explorer** | Fast codebase navigation | Cheapest available |
| **planner** | Implementation planning | Sub-agent model |
| **researcher** | Research and analysis | Sub-agent model |
| **reviewer** | Code review | Sub-agent model |
| **thinker** | Deep analysis and reasoning | Sub-agent model |
| **git_committer** | Git operations | Sub-agent model |

Agent definitions are YAML files in `agents/`. You can customize them or add your own.

## Enterprise

Need team features like centralized billing, usage analytics, SSO, and shared agent configs?

Visit [bujicoder.ai](https://bujicoder.ai) for BujiCoder Enterprise.

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md) for development setup and guidelines.

## License

Apache License 2.0 — see [LICENSE](LICENSE).
