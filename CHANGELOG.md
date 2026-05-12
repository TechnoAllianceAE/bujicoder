# Changelog

All notable changes to BujiCoder are documented here. This project follows
[Semantic Versioning](https://semver.org/) and [Keep a Changelog](https://keepachangelog.com/en/1.1.0/)
conventions.

## [v0.9.4] — 2026-05-12

Provider expansion release: three new inference backends and reliability
fixes around streaming and retries.

### Added

- **HuggingFace Inference Providers** (`shared/llm/huggingface.go`) — single
  `hf_...` access token routes through `router.huggingface.co` to the
  underlying provider for any model on the HF router. Model IDs use HF
  format (e.g. `meta-llama/Meta-Llama-3-8B-Instruct`).
- **Cloudflare Workers AI** (`shared/llm/cloudflare.go`) — OpenAI-compatible
  endpoint at `api.cloudflare.com/client/v4/accounts/<id>/ai/v1`. Requires
  an API token with `Workers AI - Read/Run` plus the account ID. Model IDs
  use Cloudflare's `@cf/<publisher>/<model>` form.
- **Fireworks AI** (`shared/llm/fireworks.go`) — dynamic catalog and
  pricing for serverless OSS models.
- **Short-name provider aliases** — register providers under multiple names
  (e.g. `or` → `openrouter`) for routing.
- **Dynamic Groq + Kilocode catalogs** — model lists fetched live from each
  provider's API; OpenRouter key now optional.
- **Cerebras model catalog** + prompt-cache plumbing for cached-token
  usage and pricing.
- **Per-publisher Vertex region routing** — different publishers can use
  different regions in a single provider instance.

### Fixed

- **Streaming truncation** — removed the 90s `http.Client.Timeout` that
  silently killed long SSE streams mid-flight; only connect/headers are
  bounded now, body uses request context.
- **Cloudflare 520–527 retryable** — origin-unreachable status codes now
  surface as retryable errors.
- **Bedrock inference-profile prefix** + merging of consecutive tool
  results to satisfy Anthropic API ordering on Bedrock.
- **Vertex global location** uses the bare hostname (no region prefix).
- **Kilocode API path** corrected to `/api/openrouter/chat/completions`;
  catalog entries use canonical `kilocode/` prefix.
- **Z.AI catalog promotion** — Z.AI models surface under the `z-ai/`
  prefix instead of `openrouter/`.

## [v0.9.3] — 2026-04-17

Uniform model naming and Azure/Bedrock/Vertex provider rollout.

### Added

- **Azure OpenAI, AWS Bedrock, GCP Vertex providers** with dynamic LiteLLM
  pricing for Vertex + Bedrock and Vertex catalog discovery via the
  v1beta1 publishers endpoint.
- **OpenRouter uniform naming** — model IDs are prefixed with
  `openrouter/` so display + routing match across the catalog.

### Fixed

- **Streaming context cancel** — defer context cancel until after the
  response body is fully read; eliminates spurious "context canceled"
  errors at the tail of streams.
- **Vertex OAuth refresh** uses `context.Background` so a cancelled
  per-request context cannot kill the shared token client.
- **Vertex catalog refresh** runs on an independent 3-minute context.
- **Vertex Gemini pricing** falls back to the generic `gemini/` namespace
  when a publisher-scoped entry is missing.
- **LiteLLM pricing** matches version-stripped model IDs.

## [v0.9.2] — 2026-04-13

Large feature release bundling the **Phase 1–5 runtime extensibility work**
that was merged since v0.9.1. Adds retry, hooks, memory, permissions,
skills, plugins, feature flags, cron, worktrees, and a shared agent
orchestrator — aligning buji's runtime surface with the broader bc2
feature set.

### Added

- **Retry with exponential backoff** (`shared/llm/retry.go`) — `WithRetry()`
  wraps any `Provider` with jittered exponential backoff (100ms floor) and
  automatic 529-overload fallback to a secondary model.
- **Lifecycle hooks** (`shared/hooks/`) — `PreToolUse` / `PostToolUse` hooks
  fire around every tool dispatch. Exit code 2 blocks the operation;
  per-hook context timeouts are enforced; `cmd.exe /c` is used on Windows.
  Tool-name normalization maps bc2 names to buji names.
- **Cross-session project memory** (`shared/memory/`) — Markdown files with
  YAML frontmatter under `~/.bujicoder/projects/<hash>/memory/`, injected
  into the system prompt after `SharedMemory`.
- **Cache token cost tracking** — `UsageInfo` now records
  `CacheReadTokens` / `CacheWriteTokens`; `ModelPricing` tracks per-token
  cache rates; new `CalculateCostCentsWithCache()` helper.
- **Permission system** (`shared/permissions/`) — 6-mode checker
  (`default`, `bypass`, `plan`, `dontAsk`, `acceptEdits`, `auto`) with
  dangerous-command/path detection. Deny rules override allow rules.
- **Layered settings hierarchy** (`shared/settings/`) — 4-layer priority
  chain (`managed > user > local > project`) under `~/.bujicoder/`, with
  `Get` / `Set` / `Reload` and JSON persistence.
- **Non-interactive mode** — `buji -p "prompt"` runs a single prompt
  through the agent runtime with no TUI. Delta text streams to stdout;
  tool calls stream to stderr (verbose). Enables scripting and CI usage.
- **Skills system** (`shared/skills/`) — Markdown-based custom slash
  commands loaded from `~/.bujicoder/skills/` (user) and
  `.bujicoder/skills/` (project). YAML frontmatter carries `name`,
  `description`, `when-to-use`, `allowed-tools`. `AllowedTools` is
  enforced via `FilterTools()` intersection. Both single-file and
  directory (`SKILL.md`) skills are supported.
- **Plugin system** (`shared/plugins/`) — Plugin directories with a
  `plugin.json` manifest, loaded from `~/.bujicoder/plugins/` and
  `.bujicoder/plugins/`. Commands are discovered from `commands/*.md`;
  hooks and MCP servers are declared in the manifest. Plugins can be
  enabled/disabled individually.
- **Feature flags** (`shared/features/`) — 23 named flags across four
  categories (agent, ui, tool, rollout). Toggle via
  `BUJI_FEATURE_<NAME>=true` env vars or programmatically. Includes a
  `GUI_MODE` flag reserved for the upcoming Wails GUI.
- **Cron scheduler** (`shared/cron/`) — Real background scheduler goroutine
  that checks every 30s for due jobs. Jobs persist to
  `~/.bujicoder/cron.json` (Create / Delete / List API), enforce a
  1-minute minimum interval, have a 5-minute execution timeout, and track
  last-run time + last error per job. Windows `cmd.exe` supported.
- **Git worktrees** (`shared/worktree/`) — `Enter` / `Exit` helpers for
  isolated git worktrees. Auto-generated branch names, cleanup on exit
  when there are no uncommitted changes, `ListActive` for enumeration,
  and `HasChanges` to check for uncommitted modifications. Worktrees
  live in `.buji-worktrees/` beside the repo.
- **`AgentOrchestrator`** (`cli/app/orchestrator.go`) — Wraps the full
  runtime (agent registry, LLM providers, tool registry with
  ask_user/approval callbacks, MCP servers, hook manager, memory store,
  cost-mode resolver) into a single reusable unit. `RunPrompt()` and
  `BuildRunConfig()` give TUI and the future GUI a shared execution
  path. `noninteractive.go` was refactored onto the orchestrator.
- **`UserError` + `ClassifyError`** — Provider errors are classified
  (quota, auth, rate limit, network, unknown) and surfaced as
  human-readable `UserError` messages instead of raw HTTP/SDK errors.
- **z-ai model catalog refresh** — All 7 z-ai models are now listed in
  the catalog with correct source tagging.

### Changed

- **Sub-agent context inheritance** (`spawn.go`) — Sub-agents now inherit
  the parent's `ContextCache`, `HookManager`, and memory store instead
  of starting fresh. Fixes missing-context bugs when sub-agents were
  spawned mid-run.
- **Error messages (OSS)** — Removed admin/enterprise references from
  OSS error messages so open-source users don't see gateway-only guidance.
- **Roadmap doc** — `buji_v3.md` trimmed to list only pending / partial
  features; shipped items moved out.
- **Landing page** — `community.bujicoder.com` terminal demo now shows
  `v0.9.2` in the install output.

### Fixed

- **z-ai source tag persistence** — Source tags no longer get wiped when
  the model catalog auto-refreshes.

[v0.9.2]: https://github.com/TechnoAllianceAE/bujicoder/releases/tag/v0.9.2
[v0.9.1]: https://github.com/TechnoAllianceAE/bujicoder/releases/tag/v0.9.1
