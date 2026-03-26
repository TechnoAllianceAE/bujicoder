# v0.8.1

## Bug Fixes

- **fix(installer):** resolve `unbound variable` error in install.sh caused by `$tmp` escaping out of scope under `set -u` ([scripts/install.sh](scripts/install.sh), [docs/install.sh](docs/install.sh))

- **fix(llm): local model routing for Llama.cpp and Ollama** — model names from local providers (e.g. `Qwen3-Coder-Next-Q4_K_M.gguf`) were stored without the required `provider/` prefix, causing `invalid model format: expected 'provider/model'` at runtime. Default configs and the model fetcher now correctly prefix local models (e.g. `llamacpp/Qwen3-Coder-Next-Q4_K_M.gguf`).

- **fix(streaming):** accumulate streaming tool call arguments across chunks instead of overwriting on each delta.

- **fix(llm):** omit `content` field entirely for assistant messages with `tool_calls`, fixing compatibility with providers that reject `null` or empty content alongside tool calls.

## Improvements

- **feat(catalog):** add tool support, description, and knowledge cutoff fields to the model catalog.

- **fix(setup): improved UX for local LLM providers** — the setup wizard now shows "Base URL" instead of "API Key" for Llama.cpp and Ollama, and pressing Enter with no input uses the default server URL (`localhost:8080` / `localhost:11434`).

## Upgrade

```bash
curl -fsSL https://bujicoder.com/install.sh | bash
```

Or if you have an existing install:

```bash
buji update
```
