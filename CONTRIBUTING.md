# Contributing to BujiCoder

Thank you for your interest in contributing to BujiCoder!

## Prerequisites

- Go 1.24+
- Git

## Development Setup

```bash
# Clone the repository
git clone https://github.com/TechnoAllianceAE/bujicoder.git
cd bujicoder

# Build
make build

# Run tests
make test

# Lint
make lint
```

## Building

```bash
# Build the CLI binary
make build

# Install to ~/.local/bin/
make install

# Cross-compile for all platforms
make dist
```

## Project Structure

```
cli/
  cmd/buji/         # CLI entry point
  app/              # Bubble Tea TUI model
  config/           # Configuration management
  localstore/       # Local conversation persistence
shared/
  agent/            # YAML agent loading and registry
  agentruntime/     # Core agent step loop
  llm/              # LLM provider implementations
  tools/            # Local tool executors
  costmode/         # Cost mode -> model resolution
  mcp/              # MCP server integration
  buildinfo/        # Version info (injected via ldflags)
  selfupdate/       # CLI self-update
agents/             # YAML agent definitions
```

## Code Style

- Format with `gofmt`
- Lint with `golangci-lint`
- Follow [Uber Go Style Guide](https://github.com/uber-go/guide/blob/master/style.md)
- Tests alongside code in `*_test.go` files
- Use `zerolog` for structured logging

## Commit Messages

We use conventional commits:

- `feat:` — new feature
- `fix:` — bug fix
- `docs:` — documentation only
- `test:` — tests only
- `chore:` — build/tooling changes
- `refactor:` — code restructuring without behavior change

## Pull Requests

1. Fork the repo and create a feature branch from `main`
2. Make your changes with clear commit messages
3. Ensure `make test` and `make lint` pass
4. Open a PR with a clear description of the changes

## Adding a New LLM Provider

1. Create `shared/llm/<provider>.go` implementing the `Provider` interface
2. Register in the provider factory in `shared/llm/provider.go`
3. Add API key field to `cli/config/config.go` (`APIKeysConfig`)
4. Add env var fallback in `GetAPIKey()`
5. Register in `cli/app/model.go` (`registerLocalProviders()`)
6. Add tests

## Adding a New Tool

1. Add tool definition in `shared/tools/tools.go`
2. Register in `NewRegistry()`
3. Add to relevant agent YAML files (`agents/*.yaml`)

## License

By contributing, you agree that your contributions will be licensed under the Apache License 2.0.
