# BujiCoder

**AI coding assistant that runs in your terminal. Bring your own API keys.**

BujiCoder is a multi-agent AI coding tool with a terminal UI. It reads your codebase, runs tools locally, and streams responses from any major LLM provider — all without sending your code to a third-party server.

## Install

### macOS / Linux

```bash
curl -fsSL https://raw.githubusercontent.com/TechnoAllianceAE/bujicoder/main/scripts/install.sh | bash
```

### From source

```bash
go install github.com/TechnoAllianceAE/bujicoder/cli/cmd/buji@latest
```

### Windows

Download the latest `.exe` from [GitHub Releases](https://github.com/TechnoAllianceAE/bujicoder/releases).

## Getting Started

Run `buji` in your terminal. On first launch, the setup wizard guides you through:

1. **Quick Setup** — Enter an [OpenRouter](https://openrouter.ai/keys) API key and start coding immediately with recommended defaults.
2. **Advanced Setup** — Pick from 6 providers (OpenRouter, Groq, Cerebras, Together AI, OpenAI, Anthropic), enter your key, browse available models, and assign models to each agent role and cost mode.

Your config is saved to `~/.bujicoder/bujicoder.yaml`. You can edit it anytime to change models, add API keys, or configure MCP servers.

## Features

- **Multi-provider** — Anthropic, OpenAI, Gemini, xAI, OpenRouter, Groq, Together, Cerebras, Ollama
- **Multi-agent** — Specialized agents for editing, planning, research, code review, and git commits
- **Local tools** — File read/write, code search, terminal commands executed on your machine
- **MCP support** — Extend with Model Context Protocol servers
- **Cost modes** — Switch between normal / heavy / max to control model quality and spending
- **Built-in cost tracking** — Static pricing registry for 80+ models across 8 providers, works offline
- **Vision** — Attach images with `@path/to/image.png`
- **Self-updating** — Run `buji update` to get the latest version

## Usage

```bash
buji              # Start interactive session
buji update       # Update to latest version
```

### TUI Commands

| Command | What it does |
|---------|-------------|
| `/new` | Start a new conversation |
| `/mode <mode>` | Switch cost mode (normal / heavy / max / plan) |
| `/history` | Browse past conversations |
| `/models` | Show model-agent mappings |
| `/copy` | Copy last response to clipboard |
| `/help` | Show all commands and shortcuts |

## Configuration

Config file: `~/.bujicoder/bujicoder.yaml`

See [docs/CONFIGURATION.md](docs/CONFIGURATION.md) for the full configuration reference, environment variables, agent architecture, and customization options.

## Enterprise

Need team features like centralized billing, usage analytics, SSO, and shared agent configs? Visit [community.bujicoder.com](https://community.bujicoder.com).

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md) for development setup and guidelines.

## License

Apache License 2.0 — see [LICENSE](LICENSE).
