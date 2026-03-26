# v0.8.2

## Bug Fixes

- **fix(setup): store default URL for Ollama/Llama.cpp** — when the user pressed Enter with empty input to accept the default server URL, the config saved `api_keys: {}` with no URL. The Ollama/Llama.cpp provider was never registered, causing "No LLM providers configured" on every message. Default URLs (`http://localhost:11434` for Ollama, `http://localhost:8080` for Llama.cpp) are now persisted automatically.

- **fix(setup): always prefix Ollama model names with `ollama/`** — Ollama model names can contain `/` as a namespace separator (e.g. `qooba/qwen3-coder-30b-a3b-instruct:q3_k_m`). The router mistook `qooba` for a provider name. Model names are now always prefixed (e.g. `ollama/qooba/qwen3-coder-30b-a3b-instruct:q3_k_m`) so the router splits correctly.

## Upgrade

```bash
curl -fsSL https://bujicoder.com/install.sh | bash
```

After upgrading, delete the old config and re-run setup:

```bash
rm ~/.bujicoder/bujicoder.yaml
buji
```

**Full Changelog**: https://github.com/TechnoAllianceAE/bujicoder/compare/v0.8.1...v0.8.2
