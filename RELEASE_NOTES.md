# v0.9.0

## New Features

- **feat: paste support in TUI** — You can now paste text (error output, code snippets, etc.) directly into the chat input. Previously, multi-character paste was silently rejected. Newlines in pasted text are collapsed to spaces for single-line input.

- **feat: configurable LLM request timeout** — Added `request_timeout` config option (in seconds) to `~/.bujicoder/bujicoder.yaml`. Defaults to 90 seconds. Useful for slower local models via Ollama or llama.cpp that need more time to respond.

  ```yaml
  request_timeout: 300  # 5 minutes for local LLMs
  ```

- **feat: /verbose session logging** — New `/verbose` slash command toggles detailed session logging. All communications between the orchestrator and agents/sub-agents are written to a timestamped log file in `~/.bujicoder/logs/`. Captures user messages, LLM output, tool calls with full args, tool results, sub-agent spawns, context compaction, errors, and session summaries. Toggle off to see the log path.

- **feat: code intelligence (symbols tool)** — The `symbols` tool is now exposed to the base, researcher, reviewer, and planner agents. Agents can query structured code symbols (functions, classes, types, methods) via AST-based analysis for Go and regex-based extraction for Python, TypeScript, and Rust.

## Bug Fixes

- **fix: TUI viewport flickering and output vanishing** — Footer height calculation was mismatched with actual render output during streaming, causing content to disappear. Fixed by correcting `calcFooterHeight()` to return consistent values.

- **fix: keyboard input dropping** — The TUI silently rejected some key events due to an overly aggressive string-length filter. Replaced with a proper `KeyType` check (`KeyRunes`/`KeySpace`) so only unrecognized special keys are filtered.

- **fix: viewport update debouncing** — `SetContent()` was called on every streaming chunk, causing flickering during fast output. Now uses a dirty flag flushed on the 100ms tick cycle. Non-streaming updates remain immediate.

- **fix: viewport init flash** — The viewport was created empty, causing a blank frame on startup. Now gets initial content immediately on creation.

- **fix: race condition in context cache** — `Get()` used a read lock for the staleness check then released it before refreshing, allowing concurrent goroutines to refresh the same entry. Now uses double-checked locking under a write lock.

- **fix: FNV-1a hash for loop detection** — Replaced simple polynomial hash (`h*31+c`) with FNV-1a for better collision resistance in identical tool call detection.

- **fix: file path validation** — `safePath()` now rejects empty paths, null bytes (OS bypass vector), and paths exceeding 4096 characters.

- **fix: web search context cancellation** — Returns `ctx.Err()` directly when cancelled, instead of wrapping it in a generic error that hides the cancellation signal.

## Upgrade

```bash
curl -fsSL https://community.bujicoder.com/install.sh | bash
```

**Full Changelog**: https://github.com/TechnoAllianceAE/bujicoder/compare/v0.8.4...v0.9.0

---

# v0.8.4

## SDK Improvements

- **feat: static cost registry with 80+ models** — Hardcoded pricing for 8 providers (Anthropic, OpenAI, Google, xAI, Meta, DeepSeek, Mistral, Qwen). The pricing service now loads a static baseline at startup before fetching from APIs, so cost tracking works even when OpenRouter is unreachable.

- **feat: resilient pricing startup** — The gateway/CLI no longer fails if the initial OpenRouter API fetch fails. Static prices serve as fallback until the next successful refresh. API prices overlay the static baseline (merge, don't replace).

- **feat: pricing accessors** — New `ModelCount()` and `GetPricing()` methods on PricingService for monitoring and dashboard integrations.

## Upgrade

```bash
curl -fsSL https://community.bujicoder.com/install.sh | bash
```

**Full Changelog**: https://github.com/TechnoAllianceAE/bujicoder/compare/v0.8.3...v0.8.4

---

# v0.8.3

## Bug Fixes

- **fix: use routed model name when calling LLM providers** — the `ollama/` prefix was being sent to the Ollama API in the model field (e.g. `ollama/qooba/qwen3-coder`), causing 404 errors. The router now correctly strips the provider prefix before sending to the provider.

- **fix(setup): store default URL for Ollama/Llama.cpp** — pressing Enter with empty input to accept the default server URL saved an empty `api_keys: {}`, causing "No LLM providers configured" errors.

- **fix(setup): always prefix Ollama model names** — Ollama model names with namespaces (e.g. `qooba/qwen3-coder`) were mistakenly treated as already having a provider prefix. All local models are now always prefixed.

- **fix: graceful fallback for models without tool support** — some local/quantized models don't support function calling and reject requests with `"does not support tools"`. BujiCoder now automatically retries without tools so the model can still chat, even if it can't use tools like file editing or code search.

## Docs

- All install URLs now point to `https://community.bujicoder.com/install.sh` instead of raw GitHub links.
- Installation docs updated with local LLM setup guide (Ollama + Llama.cpp).
- Landing page updated: Llama.cpp added to providers grid, version corrected.

## Important: Local Model Tool Support

Not all local models support tool/function calling. Models **with** tool support (recommended for full BujiCoder functionality):
- `llama3.1:8b` / `llama3.1:70b`
- `qwen2.5-coder:7b` / `qwen2.5-coder:32b`
- `mistral:7b`
- `qwen3:8b`

Models **without** tool support will still work for conversation, but cannot read files, edit code, run commands, or use any agent tools.

## Upgrade

```bash
curl -fsSL https://community.bujicoder.com/install.sh | bash
rm ~/.bujicoder/bujicoder.yaml
buji
```

**Full Changelog**: https://github.com/TechnoAllianceAE/bujicoder/compare/v0.8.2...v0.8.3
