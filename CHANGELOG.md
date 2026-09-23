# Changelog

All notable changes to BujiCoder are documented here. This project follows
[Semantic Versioning](https://semver.org/) and [Keep a Changelog](https://keepachangelog.com/en/1.1.0/)
conventions.

## [v0.10.2] — 2026-09-23

v0.10.1 was documented but never tagged. Its fixes ship in this release.

### Added

- **`APIKey()` on `OpenRouterProvider` and `OpenCodeProvider`**
  (`shared/llm/openrouter.go`, `shared/llm/opencode.go`), matching the
  existing `AnthropicProvider.APIKey()`. This lets callers reuse a
  configured provider key for other endpoints on the same account.
  BujiCoder Enterprise v1.8.0 uses it to call `/v1/systemone` (the TypeSafe
  Jev decision model) for agent routing without a separate key.

## [v0.10.1] — 2026-09-04

Bugfix release: two data-loss-adjacent bugs in server-side config persistence.

### Fixed

- **`model_config.yaml` writes failed on bind-mounted deployments**
  (`shared/costmode/costmode.go`) — the atomic temp-file-then-rename write
  path fails with `EBUSY` when the target is a Docker bind-mounted file (a
  mount point, not a plain file), so the admin panel's "Save Changes" on
  the routing page errored with `rename ... device or resource busy` on
  every save. `saveModelConfig` now falls back to an in-place
  `os.WriteFile` when rename returns `EBUSY`/`EXDEV`.
- **Admin-registered custom OpenAI-compatible providers vanished from the
  model catalog after the next refresh** (`shared/llm/catalog.go`) —
  `fetchFromAPI` rebuilds the catalog's model map from scratch on every
  auto-refresh (every 6h) and manual "Refresh Models", but only
  re-fetched the aggregator providers that have a dedicated `Set*Key`;
  custom providers merged via `MergeOpenAICompatModels` had no such
  re-fetch path and were silently dropped on the next rebuild. The
  `(provider, baseURL, apiKey)` spec is now remembered and replayed on
  every refresh.

## [v0.10.0] — 2026-08-18

Production-hardening release: a full audit of every package for concurrency,
resource-leak, security, and data-integrity bugs, all dependencies upgraded,
and the release pipeline now publishes checksummed, reproducible binaries with
verified self-updates.

### Security

- **Self-update integrity verification** (`shared/selfupdate/`) — the running
  binary was previously replaced with unverified network bytes. Updates are
  now validated against a `checksums.txt` SHA-256 manifest
  (`go-selfupdate.ChecksumValidator`) and fail closed: a missing manifest or
  hash mismatch aborts the update before anything is written.
- **Workspace path containment** (`shared/tools/`) — `write_file`,
  `str_replace`, `read_files`, and patch targets now resolve symlinks on the
  longest existing ancestor before the boundary check, closing an escape via a
  symlinked parent directory. Permission-restricted paths are checked against
  the resolved target, not just the spelling used in the request.
- **Plan-mode enforcement** (`shared/agentruntime/`) — plan mode was
  advisory-only (the runtime never set it). It is now an authoritative flag on
  `RunConfig`, propagated to sub-agents, and enforced before any write. Shell
  commands in plan mode are parsed for redirections, `$(...)`, backticks, and
  backgrounding; a `.md`-suffix path that resolves to a non-markdown target is
  rejected.
- **Gemini API key no longer in the URL** (`shared/llm/gemini.go`) — the key
  moved to the `x-goog-api-key` header; previously any network error printed
  the full URL including the key into logs and the TUI.
- **Vertex credentials validated on load** (`shared/llm/vertex.go`) — replaced
  deprecated `google.CredentialsFromJSON` with the validating variant and a
  declared-type allowlist; unexpected credential documents are rejected.
- **Tool name collisions** (`shared/tools.Registry.Register`) — registering a
  duplicate tool name no longer silently shadows the built-in; MCP tools get a
  deterministic `<server>_<tool>` fallback name.

### Fixed

- **Streaming goroutine leaks** — every provider stream send is
  context-aware, and the agent runtime drains the event channel on early
  exit, so an abandoned stream no longer pins the goroutine, response body,
  and socket for the life of the process.
- **Provider retry logic** (`shared/llm/retry.go`) — retryability is now
  decided by structured error types instead of substring matching, `Retry-After`
  is honored (clamped), fallback-model switches no longer mutate the caller's
  request, and 408/529 are classified retryable.
- **History compression was dead code** (`shared/agentruntime/state.go`) —
  tool results were appended as `tool_result` parts but compression only
  truncated `text` parts, so giant tool outputs stayed verbatim until the
  model rejected the request. Truncation is now rune-safe, copy-on-write, and
  no longer corrupts the caller's conversation record.
- **Compaction could orphan tool results** (`shared/agentruntime/compact.go`)
  — the split boundary could summarize away an assistant `tool_call` while
  keeping its `tool_result`, which the Anthropic API rejects with a 400. The
  boundary now advances past leading tool messages.
- **Loop guard defeated by argument reformatting** — the identical-call guard
  hashes a canonicalized (sorted-key) form of the arguments.
- **Malformed config destroyed API keys** (`cli/config/`) — a YAML typo made
  the loader report "no config found", sending the user into first-run setup,
  which overwrote the file. Parse failures now surface an error naming the
  file; saves are atomic (temp + rename, 0600).
- **JSON→bbolt migration was re-run on every start** (`shared/store/migrate.go`)
  and could wipe messages appended since the last run; it is now marked
  durable and idempotent.
- **Search index could reference rolled-back transactions**
  (`shared/store/store.go`) — Bleve indexing is staged and applied only after
  the bbolt transaction commits.
- **Snapshot revert silently skipped unicode filenames** (`shared/snapshot/`)
  — git's quoted `ls-tree` output was fed to `git show`; switching to NUL
  delimiting fixes reverting repos with non-ASCII paths.
- **Verbose logging data race** (`cli/app/`) — parallel sub-agents wrote a
  shared map from multiple goroutines (fatal `concurrent map writes`); the
  sink is now mutex-guarded.
- **Sub-agent spawn concurrency** — `spawn_agents` fan-out now shares the
  coordinator's semaphore (previously unbounded) and parent event callbacks
  are serialized.
- Hook execution is context-aware and stops promptly on cancellation
  (`shared/hooks/`). MCP and LSP child processes are killed and reaped by
  process group, so wrapper-spawned grandchildren no longer survive shutdown.
- Terminal commands run with a hard timeout, bounded (1 MiB) output, and
  process-group kill on cancellation.

### Changed

- All direct dependencies upgraded to latest (bleve v2.6.0, glamour v1.0.0,
  MCP go-sdk v1.7.0, zerolog v1.35.1, bbolt v1.5.0, go-selfupdate v1.6.0,
  x/net v0.58.0, …) with the transitive graph resolved by MVS. Toolchain
  pinned to Go 1.26.6 (stdlib security fixes).
- Release builds are now CGO-free, `-trimpath`-reproducible, and published
  with a `checksums.txt` SHA-256 manifest (required for verified self-update).
  **Note:** self-update now fails closed — releases without `checksums.txt`
  are rejected rather than installed unverified.

### Added

- Test suites for previously untested packages: `cli/config`,
  `cli/localstore`, `cli/app`, `shared/mcp`, `shared/errutil`,
  `shared/selfupdate`, plus targeted regression tests across all audited
  packages. CI now runs `go vet`, golangci-lint v2 (new `.golangci.yml`),
  and a govulncheck gate with a single reviewed exception
  (GO-2026-5932: `x/crypto/openpgp` is unconditionally linked by
  go-selfupdate and is not on our executed path).

## [v0.9.5] — 2026-06-02

Custom OpenAI-compatible endpoints; OpenCode Zen provider wiring; shared
pooled HTTP transports with HTTP/1.1 forced for streaming; Anthropic
tool-result message coalescing; DeepSeek reasoning preservation; model
discovery for generic OpenAI-compatible providers.

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

[v0.10.1]: https://github.com/TechnoAllianceAE/bujicoder/releases/tag/v0.10.1
[v0.10.0]: https://github.com/TechnoAllianceAE/bujicoder/releases/tag/v0.10.0
[v0.9.5]: https://github.com/TechnoAllianceAE/bujicoder/releases/tag/v0.9.5
[v0.9.4]: https://github.com/TechnoAllianceAE/bujicoder/releases/tag/v0.9.4
[v0.9.3]: https://github.com/TechnoAllianceAE/bujicoder/releases/tag/v0.9.3
[v0.9.2]: https://github.com/TechnoAllianceAE/bujicoder/releases/tag/v0.9.2
[v0.9.1]: https://github.com/TechnoAllianceAE/bujicoder/releases/tag/v0.9.1
