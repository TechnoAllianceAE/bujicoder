# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What is This Repository?

This repository contains **BujiCoder Enterprise**, a high-performance AI coding assistant built in Go. Active development is in `bujicoder-enterprise/` — a Go microservices backend with a Bubble Tea CLI, designed to scale to 1000+ concurrent users.

## Repository Layout

```
bujicoder-enterprise/      # Active Go project (all current development)
├── gateway/               # Gateway server and all backend services
│   ├── cmd/               # Service entry points (gateway, core-api, llm-proxy, billing-worker, analytics-worker, migrate)
│   ├── server/            # HTTP gateway (handlers, middleware)
│   ├── analytics/         # Analytics worker
│   ├── billing/           # Billing worker (Stripe, credits)
│   ├── config/            # Server configuration
│   ├── coreapi/           # Core API service (users, orgs, credits)
│   ├── email/             # Email/SMTP service
│   └── llmproxy/          # LLM proxy service
├── cli/                   # Bubble Tea TUI client
│   ├── cmd/cli/           # CLI binary entry point
│   ├── app/               # TUI model (model.go)
│   ├── sdk/               # HTTP/SSE client (client.go)
│   └── config/            # CLI config management
├── shared/                # Packages shared by both gateway and CLI
│   ├── agent/             # Agent YAML loading and registry
│   ├── agentruntime/      # Core step loop (LLM ↔ tool execution)
│   ├── auth/              # JWT, GitHub OAuth, middleware
│   ├── buildinfo/         # Version/commit injected via ldflags
│   ├── cache/             # Redis client and rate limiting
│   ├── costmode/          # Cost mode → model resolution
│   ├── database/          # PostgreSQL pool, migrations, sqlc queries
│   ├── errutil/           # Error handling utilities
│   ├── events/            # NATS event helpers
│   ├── gen/               # Generated protobuf Go code
│   ├── llm/               # LLM provider abstraction, catalog, pricing
│   ├── selfupdate/        # CLI self-update logic
│   └── tools/             # Tool registry (file ops, terminal, search)
├── agents/                # YAML agent definitions (base, editor, file_explorer, git_committer, planner, researcher, reviewer, thinker)
├── db/queries/            # SQL query files for sqlc
├── proto/bujicoder/       # gRPC protobuf definitions (core/v1, llm/v1, events/v1)
├── deploy/docker/         # Per-service Dockerfiles
├── installer/             # Windows MSI installer (WiX)
├── scripts/               # Shell helpers (install.sh, proto-gen.sh)
├── docker-compose.yml     # Infrastructure stack (postgres, pgbouncer, redis, nats)
└── Makefile               # Build, test, migrate, proto, sqlc, release commands

bujicoder/                 # Open-source CLI repo (TechnoAllianceAE/bujicoder)
docs/                      # Supplementary documentation (SELF-HOSTING.md, etc.)
```

## Naming Convention

- **Product name:** BujiCoder
- **CLI binary:** `buji`
- **Config dir:** `~/.bujicoder/`
- **Config file:** `bujicoder.yaml`
- **Env vars:** `BUJICODER_*`
- **Public Go module:** `github.com/TechnoAllianceAE/bujicoder`
- **Private Go module:** `github.com/TechnoAllianceAE/bujicoder-enterprise`

## BujiCoder Build & Development Commands

All active development commands run from the `bujicoder-enterprise/` directory:

```bash
cd bujicoder-enterprise

# Start infrastructure (PostgreSQL, PgBouncer, Redis, NATS)
make docker-up

# Apply database migrations
make migrate-up

# Build all service binaries
make build

# Build a specific service
make build-gateway          # or build-cli, build-core-api, etc.

# Run a service locally (after docker-up + migrate-up)
make run-gateway            # or run-core-api, run-llm-proxy, etc.

# Start infra + run migrations in one command
make dev

# Install CLI binary to ~/.local/bin/buji
make install-cli

# Code generation
make proto                  # Generate Go code from .proto files
make sqlc                   # Generate Go code from SQL queries

# Testing
make test                   # Run all tests (go test ./... -race)
make test-coverage          # Tests with coverage report

# Lint & format
make lint                   # golangci-lint
make fmt                    # gofmt + goimports

# Database
make migrate-up             # Apply pending migrations
make migrate-down           # Rollback last migration
make migrate-status         # Show migration status

# Docker
make docker-up              # Start infrastructure
make docker-down            # Stop infrastructure
make docker-logs            # Tail container logs
make docker-build           # Build all Docker images

# Release
make release VERSION=x.y.z  # Build cross-platform CLI + create GitHub Release
make dist-cli               # Cross-compile CLI for all platforms
```

## BujiCoder Architecture

### Services

| Service | Port | Purpose |
|---------|------|---------|
| **gateway** | 8080 (HTTP) | REST/SSE entry point, auth, rate limiting, routes to core services |
| **core-api** | 9001 (gRPC) | User, org, credit, subscription, agent management |
| **llm-proxy** | 9002 (gRPC) + 8082 (HTTP) | LLM provider routing and streaming |
| **billing-worker** | — | NATS consumer for async credit deduction and Stripe |
| **analytics-worker** | — | NATS consumer for usage tracking |
| **cli** | — | Bubble Tea TUI for interactive agent sessions |
| **migrate** | — | Database migration runner |

### Request Flow

```
CLI (Bubble Tea) → HTTP/SSE → Gateway (8080)
  ├── Auth: GitHub OAuth + Password + JWT
  ├── Agent Runtime: YAML agent definitions → LLM calls → tool dispatch → sub-agent spawning
  ├── CoreAPI: users, orgs, credits, subscriptions (in-process dev / gRPC prod)
  └── LLM Proxy: routes to Anthropic, OpenAI, Gemini, XAI, OpenRouter, ZAI, Ollama
       └── Events → NATS → billing-worker, analytics-worker (async)
```

### Agent System

Agents are YAML-defined in `bujicoder-enterprise/agents/`. Each specifies a model, tools, spawnable sub-agents, system prompt, max steps, and max tokens. Key agents: `base` (orchestrator, Claude Sonnet 4), `editor` (file editing), `file_explorer` (GPT-4o-mini, cheapest), `planner` (Qwen 3 235B), `researcher`, `reviewer`, `thinker`, `git_committer` (Claude Haiku 4.5).

Sub-agents do NOT inherit parent models — each has its own `model:` field.

### Infrastructure Stack

- **PostgreSQL 16** + **PgBouncer** (transaction pooling, 200 client → 40 DB connections)
- **Redis 7** (auth cache, rate limiting)
- **NATS JetStream** (event bus for billing and analytics)

### Code Generation

- **sqlc**: SQL queries in `db/queries/*.sql` → generated Go in `shared/database/queries/`. Config in `sqlc.yaml`.
- **protobuf**: `.proto` files in `proto/bujicoder/` → generated Go in `shared/gen/`. Uses `buf generate`.

### LLM Providers

Seven providers in `shared/llm/`: Anthropic, OpenAI, Gemini, XAI, OpenRouter, ZAI, Ollama. Cost mode (normal/heavy/max) controls model selection per agent role via `model_config.yaml`. Individual sub-agents can have per-agent model overrides (`agent_overrides` in the config). The model catalog can be loaded statically from `MODELS_JSON_PATH` or dynamically from OpenRouter API (auto-refreshes every 6 hours). Provider-specific implementations handle streaming differences. OpenRouter pricing service (`shared/llm/pricing.go`) fetches real-time model costs.

## Environment Configuration

Copy `bujicoder-enterprise/.env.example` to `bujicoder-enterprise/.env`. Key groups:
- **Database**: `POSTGRES_*`, `PGBOUNCER_*` (default: `bujicoder:bujicoder_dev@localhost:5432/bujicoder`)
- **Infrastructure**: `REDIS_URL`, `NATS_URL`
- **Gateway**: `GATEWAY_PORT`, `BASE_URL`, `GATEWAY_CORS_ORIGINS`, `GATEWAY_RATE_LIMIT_RPM`
- **Auth**: `GITHUB_CLIENT_ID`, `GITHUB_CLIENT_SECRET`, `JWT_SECRET`, `ADMIN_SECRET` (password auth also supported, default password `pass123`)
- **LLM Keys**: `ANTHROPIC_API_KEY`, `OPENAI_API_KEY`, `GOOGLE_AI_API_KEY`, `XAI_API_KEY`, `OPENROUTER_API_KEY`
- **Stripe**: `STRIPE_SECRET_KEY`, `STRIPE_WEBHOOK_SECRET`
- **Email**: `SMTP_HOST`, `SMTP_FROM` (for password reset emails)
- **Agent config**: `AGENTS_DIR` (default `./agents`), `MODELS_JSON_PATH`

## Go Code Style

- Format with `gofmt`, lint with `golangci-lint`
- Follow [Uber Go Style Guide](https://github.com/uber-go/guide/blob/master/style.md)
- Tests alongside code in `*_test.go` files
- Use `zerolog` for structured logging
- HTTP routing via `go-chi/chi`
- Database access via `pgx/v5` + sqlc-generated queries
- Config via `caarlos0/env` (struct tags)
- Conventional commits: `feat:`, `fix:`, `docs:`, `test:`, `chore:`, `refactor:`

## Key File Locations

| What | Where |
|------|-------|
| Gateway routes & middleware | `bujicoder-enterprise/gateway/server/server.go`, `handlers/handlers.go` |
| Agent runtime loop | `bujicoder-enterprise/shared/agentruntime/runtime.go`, `step.go` |
| Agent YAML definitions | `bujicoder-enterprise/agents/*.yaml` |
| LLM provider implementations | `bujicoder-enterprise/shared/llm/*.go` |
| Database schema/migrations | `bujicoder-enterprise/shared/database/migrations/` |
| SQL queries (source) | `bujicoder-enterprise/db/queries/*.sql` |
| SQL queries (generated Go) | `bujicoder-enterprise/shared/database/queries/*.sql.go` |
| CLI TUI model | `bujicoder-enterprise/cli/app/model.go` |
| CLI HTTP/SSE client | `bujicoder-enterprise/cli/sdk/client.go` |
| Gateway config | `bujicoder-enterprise/gateway/config/config.go` |
| Protobuf definitions | `bujicoder-enterprise/proto/bujicoder/{core,llm,events}/v1/*.proto` |
| Generated proto code | `bujicoder-enterprise/shared/gen/` |
| Docker Compose (infra) | `bujicoder-enterprise/docker-compose.yml` |
| Per-service Dockerfiles | `bujicoder-enterprise/deploy/docker/Dockerfile.*` |
| Cost mode & model overrides | `bujicoder-enterprise/shared/costmode/costmode.go` |
| Model catalog (dynamic/static) | `bujicoder-enterprise/shared/llm/catalog.go` |
| Model pricing service | `bujicoder-enterprise/shared/llm/pricing.go` |
| Model config (mode→model map) | `bujicoder-enterprise/model_config.yaml` |
| Admin panel handlers | `bujicoder-enterprise/gateway/server/handlers/admin.go` |
| Self-hosting & Ollama guide | `docs/SELF-HOSTING.md` |
| CLI self-update logic | `bujicoder-enterprise/shared/selfupdate/selfupdate.go` |
| Tool implementations | `bujicoder-enterprise/shared/tools/tools.go` |
