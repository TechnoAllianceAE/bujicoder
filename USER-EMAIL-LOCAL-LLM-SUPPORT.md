# Email: BujiCoder — Local LLM Support (Ollama & llama.cpp)

---

**Subject:** 🚀 BujiCoder now supports local LLMs! Use Ollama & llama.cpp for free, offline coding.

---

Dear BujiCoder Community,

We've just released an exciting update: **local LLM support**. You can now run BujiCoder with Ollama or llama.cpp completely offline and for free.

---

## What's New?

### 🏠 Local LLM Support

BujiCoder now works with **locally-hosted LLMs**:

- **Ollama** — Easy one-click setup, many models available
- **llama.cpp** — Lightweight, runs on older hardware

**Why this matters:**
- ✅ **No API keys needed** — Everything runs locally
- ✅ **Completely free** — No subscription or cost
- ✅ **100% private** — Your code never leaves your machine
- ✅ **Works offline** — Perfect for trains, airplanes, restricted networks

### 📋 Setup Wizard Improvements

The first-run setup wizard now makes it easy to configure local models:

```bash
$ buji

Welcome to BujiCoder!

Select your LLM provider:
  1) OpenAI
  2) Anthropic
  3) Google Gemini
  4) OpenRouter
  5) Ollama (Local)
  6) llama.cpp (Local)
  7) Skip for now

> 5
```

Just select "Ollama" or "llama.cpp" and follow the prompts!

---

## How to Get Started

### Step 1: Install Your Local LLM

**Ollama (recommended for beginners):**
```bash
# macOS
brew install ollama

# Linux
curl -fsSL https://ollama.com/install.sh | sh

# Or download: https://ollama.com/download
```

**llama.cpp (lightweight, fewer resources):**
```bash
# Install from: https://github.com/ggerganov/llama.cpp
```

### Step 2: Pull a Model

```bash
# With Ollama
ollama pull mistral             # Fast, good quality
ollama pull llama2:7b           # General purpose
ollama pull neural-chat         # Optimized for chat

# Start the server
ollama serve
```

### Step 3: Run BujiCoder

```bash
buji

# When prompted, select "Ollama (Local)"
# Enter: http://localhost:11434
```

### Step 4: Code!

Start coding with your local LLM. No API key, no internet needed (after initial model download).

---

## Recommended Models

| Model | Size | Best For | Command |
|-------|------|----------|---------|
| **mistral:7b** | 4GB | Fast, good quality | `ollama pull mistral` |
| **neural-chat:7b** | 4GB | Chat & coding | `ollama pull neural-chat` |
| **llama2:7b** | 4GB | General purpose | `ollama pull llama2:7b` |
| **openhermes:7b** | 4GB | Creative writing | `ollama pull openhermes` |
| **dolphin-mixtral:8x7b** | 26GB | High quality | `ollama pull dolphin-mixtral` |

**Start with mistral** — it's fast, available, and great for coding.

---

## Use Cases

### ✅ Perfect For Local LLMs

- Writing code (Python, JS, Go, etc.)
- Code explanations
- Refactoring suggestions
- Bug analysis
- Simple Q&A

### ⚠️ Better with Cloud APIs

- Complex reasoning
- Very long documents (4K+ tokens)
- Multiple tool calls
- Heavy analysis

**Tip:** Use local LLMs for quick tasks, cloud APIs for complex work. Mix & match as needed!

---

## Performance & Requirements

**Minimum:**
- 4GB RAM (for 7B models)
- 5-10GB disk (for model download)
- Reasonable CPU (M1/M2 Mac, modern Intel/AMD)

**Recommended:**
- 8GB+ RAM (for faster inference)
- 20GB+ disk (for multiple models)
- GPU optional (makes it 10x faster, but not required)

**Speed (on MacBook Pro M1):**
- First response: ~2-3 seconds
- Follow-up: ~1-2 seconds per message

---

## Error Handling & Improvements

We've also improved error handling across all providers:

✅ **Structured error messages** — Clear, actionable feedback
✅ **Rate limit handling** — Better retry logic
✅ **Unified config** — Easier environment variable setup

---

## FAQ

**Q: Can I use multiple LLMs at once?**
A: Yes! You can switch providers anytime. Just run `buji` and select a different provider, or change env vars.

**Q: Will my code be private?**
A: Completely. With local LLMs, everything stays on your machine.

**Q: Can I use this offline?**
A: Yes! After downloading the model, you don't need internet. Perfect for travel, restricted networks, etc.

**Q: Which is better — Ollama or llama.cpp?**
A: Ollama is easier for beginners. llama.cpp is more lightweight. Both work great with BujiCoder.

**Q: Can I still use cloud APIs (OpenAI, Anthropic)?**
A: Absolutely! Nothing changed. You can mix local and cloud providers however you want.

---

## What's Next?

We're working on:
- Better model auto-detection
- Integration with other local LLM frameworks (LocalAI, Text Generation WebUI)
- Performance optimizations
- More curated model recommendations

---

## Feedback & Support

- 🐛 **Report issues:** https://github.com/TechnoAllianceAE/bujicoder/issues
- 💬 **Discussions:** https://github.com/TechnoAllianceAE/bujicoder/discussions
- 📖 **Docs:** [README.md](./README.md)

---

## Update

**macOS/Linux:**
```bash
curl -fsSL https://bujicoder.com/install.sh | sh
```

**Or manually:**
```bash
buji update
```

---

**Happy coding with local LLMs!** 🚀

The BujiCoder Team

P.S. If you love open-source and local-first tools, you'll love this. Tell us what you build! 💚

---

### Quick Links
- 📥 **Download:** https://github.com/TechnoAllianceAE/bujicoder/releases
- 🔗 **GitHub:** https://github.com/TechnoAllianceAE/bujicoder
- 🦙 **Ollama:** https://ollama.com
- 🔤 **llama.cpp:** https://github.com/ggerganov/llama.cpp
- 💬 **Community:** https://github.com/TechnoAllianceAE/bujicoder/discussions
