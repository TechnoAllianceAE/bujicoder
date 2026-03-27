# v0.8.4

## SDK Improvements

- **feat: static cost registry with 80+ models** — Hardcoded pricing for 8 providers (Anthropic, OpenAI, Google, xAI, Meta, DeepSeek, Mistral, Qwen). The pricing service now loads a static baseline at startup before fetching from APIs, so cost tracking works even when OpenRouter is unreachable.

- **feat: resilient pricing startup** — The gateway/CLI no longer fails if the initial OpenRouter API fetch fails. Static prices serve as fallback until the next successful refresh. API prices overlay the static baseline (merge, don't replace).

- **feat: pricing accessors** — New `ModelCount()` and `GetPricing()` methods on PricingService for monitoring and dashboard integrations.

## Upgrade

```bash
curl -fsSL https://bujicoder.com/install.sh | bash
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

- All install URLs now point to `https://bujicoder.com/install.sh` instead of raw GitHub links.
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
curl -fsSL https://bujicoder.com/install.sh | bash
rm ~/.bujicoder/bujicoder.yaml
buji
```

**Full Changelog**: https://github.com/TechnoAllianceAE/bujicoder/compare/v0.8.2...v0.8.3
