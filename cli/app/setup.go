package app

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/google/uuid"

	agentdata "github.com/TechnoAllianceAE/bujicoder/agents"
	cliconfig "github.com/TechnoAllianceAE/bujicoder/cli/config"
	"github.com/TechnoAllianceAE/bujicoder/shared/costmode"
)

// Setup step constants.
const (
	setupStepModeSelect  = 0  // Quick vs Advanced
	setupStepQuickKey    = 1  // Quick: OpenRouter API key
	setupStepAdvProvider = 10 // Advanced: provider picker
	setupStepAdvKey      = 11 // Advanced: API key entry
	setupStepAdvFetching = 12 // Advanced: fetching models (spinner)
	setupStepAdvModels   = 13 // Advanced: model selection per mode/role
)

// setupProviderInfo describes a provider choice in the wizard.
type setupProviderInfo struct {
	key    string // internal key (matches APIKeysConfig field names)
	label  string // display name
	desc   string // short description
	signUp string // URL for obtaining an API key
}

var setupProviders = []setupProviderInfo{
	{"openrouter", "OpenRouter", "100+ models, recommended", "openrouter.ai/keys"},
	{"groq", "Groq", "Ultra-fast inference", "console.groq.com"},
	{"cerebras", "Cerebras", "Wafer-scale inference", "cloud.cerebras.ai"},
	{"together", "Together AI", "Open-source models", "api.together.ai"},
	{"openai", "OpenAI", "GPT-4o and more", "platform.openai.com/api-keys"},
	{"anthropic", "Anthropic", "Claude models", "console.anthropic.com"},
	{"ollama", "Ollama", "Local or remote LLMs", "Default: http://localhost:11434"},
	{"llamacpp", "Llama.cpp", "Local LLMs (OpenAI compat)", "Default: http://localhost:8080"},
}

var setupModeNames = [3]string{"normal", "heavy", "max"}
var setupRoleDescs = [3]string{"Primary model", "File explorer (lightweight)", "Sub-agent default"}

// modelsFetchedMsg is the Bubble Tea message returned after fetching provider models.
type modelsFetchedMsg struct {
	models []string
	err    error
}

// --- Key handling ---

func (m Model) handleSetupKeys(msg tea.KeyMsg) (Model, tea.Cmd) {
	key := msg.String()

	switch m.setupStep {
	case setupStepModeSelect:
		switch key {
		case "up":
			if m.setupMode > 0 {
				m.setupMode--
			} else {
				m.setupMode = 1
			}
		case "down":
			if m.setupMode < 1 {
				m.setupMode++
			} else {
				m.setupMode = 0
			}
		case "enter":
			if m.setupMode == 0 {
				m.setupStep = setupStepQuickKey
				m.setupAPIKey = ""
			} else {
				m.setupStep = setupStepAdvProvider
				m.setupProvider = 0
			}
		}
		return m, nil

	case setupStepQuickKey:
		return m.handleKeyEntry(key, setupStepModeSelect, func(apiKey string) (Model, tea.Cmd) {
			return m.completeSetup("openrouter", apiKey, nil)
		})

	case setupStepAdvProvider:
		maxIdx := len(setupProviders) - 1
		switch key {
		case "up":
			if m.setupProvider > 0 {
				m.setupProvider--
			} else {
				m.setupProvider = maxIdx
			}
		case "down":
			if m.setupProvider < maxIdx {
				m.setupProvider++
			} else {
				m.setupProvider = 0
			}
		case "enter":
			m.setupStep = setupStepAdvKey
			m.setupAPIKey = ""
		case "backspace":
			m.setupStep = setupStepModeSelect
		}
		return m, nil

	case setupStepAdvKey:
		provider := setupProviders[m.setupProvider].key
		isLocal := provider == "llamacpp" || provider == "ollama"
		if isLocal && key == "enter" {
			// Allow empty input — fetchProviderModels fills in the default URL.
			apiKey := strings.TrimSpace(m.setupAPIKey)
			m.setupAPIKey = apiKey
			m.setupStep = setupStepAdvFetching
			m.setupFetching = true
			m.setupFetchErr = ""
			return m, tea.Batch(
				fetchProviderModelsCmd(provider, apiKey),
				tickCmd(),
			)
		}
		return m.handleKeyEntry(key, setupStepAdvProvider, func(apiKey string) (Model, tea.Cmd) {
			m.setupAPIKey = apiKey
			m.setupStep = setupStepAdvFetching
			m.setupFetching = true
			m.setupFetchErr = ""
			return m, tea.Batch(
				fetchProviderModelsCmd(provider, apiKey),
				tickCmd(),
			)
		})

	case setupStepAdvFetching:
		if m.setupFetchErr != "" {
			switch key {
			case "enter":
				// Retry
				m.setupFetchErr = ""
				m.setupFetching = true
				provider := setupProviders[m.setupProvider].key
				return m, fetchProviderModelsCmd(provider, m.setupAPIKey)
			case "s":
				// Skip — use defaults
				return m.completeSetup(setupProviders[m.setupProvider].key, m.setupAPIKey, nil)
			case "backspace":
				m.setupStep = setupStepAdvKey
				m.setupFetchErr = ""
				m.setupFetching = false
			}
		}
		return m, nil

	case setupStepAdvModels:
		return m.handleModelSelection(key)
	}

	return m, nil
}

// handleKeyEntry handles character input for API key entry steps.
func (m Model) handleKeyEntry(key string, backStep int, onSubmit func(string) (Model, tea.Cmd)) (Model, tea.Cmd) {
	switch key {
	case "backspace":
		if len(m.setupAPIKey) > 0 {
			m.setupAPIKey = m.setupAPIKey[:len(m.setupAPIKey)-1]
		} else {
			m.setupStep = backStep
		}
	case "enter":
		apiKey := strings.TrimSpace(m.setupAPIKey)
		if apiKey == "" {
			return m, nil
		}
		return onSubmit(apiKey)
	default:
		if len(key) == 1 {
			m.setupAPIKey += key
		}
	}
	return m, nil
}

// handleModelSelection handles arrow/enter/backspace in the model picker.
func (m Model) handleModelSelection(key string) (Model, tea.Cmd) {
	maxIdx := len(m.setupModels) - 1
	if maxIdx < 0 {
		return m, nil
	}

	switch key {
	case "up":
		if m.setupModelIdx > 0 {
			m.setupModelIdx--
			m.adjustScroll()
		}
	case "down":
		if m.setupModelIdx < maxIdx {
			m.setupModelIdx++
			m.adjustScroll()
		}
	case "enter", "tab":
		// Save selection
		m.setupSelections[m.setupModeStep][m.setupRoleStep] = m.setupModels[m.setupModelIdx]
		// Advance to next role/mode
		m.setupRoleStep++
		if m.setupRoleStep > 2 {
			m.setupRoleStep = 0
			m.setupModeStep++
		}
		if m.setupModeStep > 2 {
			// All done — complete advanced setup
			return m.completeSetup(
				setupProviders[m.setupProvider].key,
				m.setupAPIKey,
				&m.setupSelections,
			)
		}
		// Pre-select default for next role
		m.preselectDefault()
	case "backspace":
		// Go back one step
		m.setupRoleStep--
		if m.setupRoleStep < 0 {
			m.setupRoleStep = 2
			m.setupModeStep--
		}
		if m.setupModeStep < 0 {
			m.setupModeStep = 0
			m.setupRoleStep = 0
			m.setupStep = setupStepAdvFetching
			m.setupFetchErr = ""
			m.setupFetching = false
			return m, nil
		}
		m.preselectDefault()
	}
	return m, nil
}

// adjustScroll ensures the selected model is within the visible window.
func (m *Model) adjustScroll() {
	visible := m.modelListVisible()
	if m.setupModelIdx < m.setupScrollOff {
		m.setupScrollOff = m.setupModelIdx
	}
	if m.setupModelIdx >= m.setupScrollOff+visible {
		m.setupScrollOff = m.setupModelIdx - visible + 1
	}
}

// modelListVisible returns how many model rows are visible.
func (m *Model) modelListVisible() int {
	vis := 10
	if m.height > 0 {
		avail := m.height - 12
		if avail > 0 && avail < vis {
			vis = avail
		}
	}
	if vis > len(m.setupModels) {
		vis = len(m.setupModels)
	}
	if vis < 1 {
		vis = 1
	}
	return vis
}

// preselectDefault sets setupModelIdx to the default model for the current mode/role.
func (m *Model) preselectDefault() {
	// If we already have a selection for this slot, use it
	if sel := m.setupSelections[m.setupModeStep][m.setupRoleStep]; sel != "" {
		for i, id := range m.setupModels {
			if id == sel {
				m.setupModelIdx = i
				m.adjustScroll()
				return
			}
		}
	}
	// Otherwise pick first model
	m.setupModelIdx = 0
	m.setupScrollOff = 0
}

// --- Rendering ---

func (m Model) renderSetupView() string {
	var b strings.Builder

	b.WriteString("\n")
	b.WriteString(bannerStyle.Render(bujicoderBanner) + "\n\n")

	sep := dimStyle.Render("  " + strings.Repeat("-", 44))

	switch m.setupStep {
	case setupStepModeSelect:
		b.WriteString("  " + titleStyle.Render("Welcome to BujiCoder") + "\n")
		b.WriteString(sep + "\n\n")
		b.WriteString(descStyle.Render("  Choose setup mode:") + "\n\n")

		opts := []struct{ label, desc string }{
			{"Quick Setup", "Just enter an OpenRouter API key and start coding"},
			{"Advanced Setup", "Choose provider, configure models for each mode"},
		}
		for i, o := range opts {
			if i == m.setupMode {
				b.WriteString(promptStyle.Render("  > ") + cmdStyle.Render(o.label) + "    " + descStyle.Render(o.desc) + "\n")
			} else {
				b.WriteString(dimStyle.Render("    "+o.label+"    "+o.desc) + "\n")
			}
		}
		b.WriteString("\n")
		b.WriteString(dimStyle.Render("  Up/Down: navigate . Enter: select . Ctrl+C: quit") + "\n")

	case setupStepQuickKey:
		b.WriteString("  " + sectionStyle.Render("Quick Setup — OpenRouter") + "\n")
		b.WriteString(sep + "\n\n")
		b.WriteString(descStyle.Render("  Enter your OpenRouter API key to get started.") + "\n")
		b.WriteString(descStyle.Render("  Get one at: ") + cmdStyle.Render("openrouter.ai/keys") + "\n\n")
		b.WriteString(promptStyle.Render("  API Key:  ") + m.setupAPIKey + dimStyle.Render("|") + "\n\n")
		configPath := filepath.Join(cliconfig.Dir(), "bujicoder.yaml")
		b.WriteString(descStyle.Render(fmt.Sprintf("  You can customize models later by editing:\n  %s", configPath)) + "\n\n")
		b.WriteString(dimStyle.Render("  Enter: save and start . Backspace: go back . Ctrl+C: quit") + "\n")

	case setupStepAdvProvider:
		b.WriteString("  " + sectionStyle.Render("Advanced Setup — Choose Provider") + "\n")
		b.WriteString(sep + "\n\n")
		for i, p := range setupProviders {
			if i == m.setupProvider {
				b.WriteString(promptStyle.Render("  > ") + cmdStyle.Render(p.label) + "    " + descStyle.Render(p.desc) + "\n")
			} else {
				b.WriteString(dimStyle.Render("    "+p.label+"    "+p.desc) + "\n")
			}
		}
		b.WriteString("\n")
		b.WriteString(dimStyle.Render("  Up/Down: navigate . Enter: select . Backspace: back . Ctrl+C: quit") + "\n")

	case setupStepAdvKey:
		chosen := setupProviders[m.setupProvider]
		isLocal := chosen.key == "llamacpp" || chosen.key == "ollama"
		if isLocal {
			b.WriteString("  " + sectionStyle.Render("Server URL Setup") + "\n")
			b.WriteString(sep + "\n\n")
			b.WriteString(descStyle.Render(fmt.Sprintf("  Enter your %s server URL (or press Enter for default).", chosen.label)) + "\n")
			b.WriteString(descStyle.Render("  Info: ") + cmdStyle.Render(chosen.signUp) + "\n\n")
			b.WriteString(promptStyle.Render(fmt.Sprintf("  %s URL:  ", chosen.label)) + m.setupAPIKey + dimStyle.Render("|") + "\n\n")
			b.WriteString(dimStyle.Render("  Enter: continue . Backspace: go back . Ctrl+C: quit") + "\n")
		} else {
			b.WriteString("  " + sectionStyle.Render("API Key Setup") + "\n")
			b.WriteString(sep + "\n\n")
			b.WriteString(descStyle.Render(fmt.Sprintf("  Enter your %s API key.", chosen.label)) + "\n")
			b.WriteString(descStyle.Render("  Get one at: ") + cmdStyle.Render(chosen.signUp) + "\n\n")
			b.WriteString(promptStyle.Render(fmt.Sprintf("  %s API Key:  ", chosen.label)) + m.setupAPIKey + dimStyle.Render("|") + "\n\n")
			b.WriteString(dimStyle.Render("  Enter: continue . Backspace: go back . Ctrl+C: quit") + "\n")
		}

	case setupStepAdvFetching:
		if m.setupFetchErr != "" {
			b.WriteString("  " + errorStyle.Render("Failed to fetch models") + "\n")
			b.WriteString(sep + "\n\n")
			b.WriteString(descStyle.Render("  "+m.setupFetchErr) + "\n\n")
			b.WriteString(dimStyle.Render("  Enter: retry . s: skip (use defaults) . Backspace: go back") + "\n")
		} else {
			frame := spinnerFrames[m.spinnerFrame%len(spinnerFrames)]
			b.WriteString("  " + sectionStyle.Render("Fetching Models") + "\n")
			b.WriteString(sep + "\n\n")
			b.WriteString(promptStyle.Render("  "+frame+" ") + descStyle.Render("Fetching available models from "+setupProviders[m.setupProvider].label+"...") + "\n")
		}

	case setupStepAdvModels:
		stepNum := m.setupModeStep*3 + m.setupRoleStep + 1
		modeName := setupModeNames[m.setupModeStep]
		roleDesc := setupRoleDescs[m.setupRoleStep]

		b.WriteString("  " + sectionStyle.Render(fmt.Sprintf("Model Selection — %s mode", modeName)) + "\n")
		b.WriteString(sep + "\n\n")
		b.WriteString(descStyle.Render(fmt.Sprintf("  Select model for: %s  [%d/9]", roleDesc, stepNum)) + "\n\n")

		visible := m.modelListVisible()
		above := m.setupScrollOff
		below := len(m.setupModels) - m.setupScrollOff - visible

		if above > 0 {
			b.WriteString(dimStyle.Render(fmt.Sprintf("  ^ %d more", above)) + "\n")
		}

		end := m.setupScrollOff + visible
		if end > len(m.setupModels) {
			end = len(m.setupModels)
		}
		for i := m.setupScrollOff; i < end; i++ {
			id := m.setupModels[i]
			if i == m.setupModelIdx {
				b.WriteString(promptStyle.Render("  > ") + cmdStyle.Render(id) + "\n")
			} else {
				b.WriteString(dimStyle.Render("    "+id) + "\n")
			}
		}

		if below > 0 {
			b.WriteString(dimStyle.Render(fmt.Sprintf("  v %d more", below)) + "\n")
		}

		b.WriteString("\n")
		b.WriteString(dimStyle.Render("  Up/Down: navigate . Enter: select . Backspace: back") + "\n")
	}

	return b.String()
}

// --- Model fetching ---

func fetchProviderModelsCmd(provider, apiKey string) tea.Cmd {
	return func() tea.Msg {
		models, err := fetchProviderModels(provider, apiKey)
		return modelsFetchedMsg{models: models, err: err}
	}
}

func fetchProviderModels(provider, apiKey string) ([]string, error) {
	type providerEndpoint struct {
		url     string
		authKey string // header name
		authVal string // header value format
		extra   map[string]string
	}

	ep := providerEndpoint{authKey: "Authorization", authVal: "Bearer " + apiKey}
	switch provider {
	case "openrouter":
		ep.url = "https://openrouter.ai/api/v1/models"
	case "groq":
		ep.url = "https://api.groq.com/openai/v1/models"
	case "cerebras":
		ep.url = "https://api.cerebras.ai/v1/models"
	case "together":
		ep.url = "https://api.together.xyz/v1/models"
	case "openai":
		ep.url = "https://api.openai.com/v1/models"
	case "anthropic":
		ep.url = "https://api.anthropic.com/v1/models"
		ep.authKey = "x-api-key"
		ep.authVal = apiKey
		ep.extra = map[string]string{"anthropic-version": "2023-06-01"}
	case "ollama":
		if apiKey == "" {
			apiKey = "http://localhost:11434"
		}
		ep.url = strings.TrimRight(apiKey, "/") + "/api/tags"
	case "llamacpp":
		if apiKey == "" {
			apiKey = "http://localhost:8080"
		}
		ep.url = strings.TrimRight(apiKey, "/") + "/v1/models"
	default:
		return nil, fmt.Errorf("unknown provider: %s", provider)
	}

	client := &http.Client{Timeout: 15 * time.Second}
	req, err := http.NewRequest(http.MethodGet, ep.url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set(ep.authKey, ep.authVal)
	for k, v := range ep.extra {
		req.Header.Set(k, v)
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == 401 || resp.StatusCode == 403 {
		return nil, fmt.Errorf("invalid API key (HTTP %d)", resp.StatusCode)
	}
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("API returned HTTP %d", resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 2*1024*1024))
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	var result struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("parse response: %w", err)
	}

	models := make([]string, 0, len(result.Data))
	for _, m := range result.Data {
		if m.ID != "" {
			models = append(models, m.ID)
		}
	}

	// Ollama uses a different JSON structure for tags: {"models": [{"name": "..."}]}
	if provider == "ollama" && len(models) == 0 {
		var ollamaResult struct {
			Models []struct {
				Name string `json:"name"`
			} `json:"models"`
		}
		_ = json.Unmarshal(body, &ollamaResult)
		for _, m := range ollamaResult.Models {
			if m.Name != "" {
				models = append(models, m.Name)
			}
		}
	}
	
	if len(models) == 0 {
		return nil, fmt.Errorf("no models returned by %s", provider)
	}

	// Local providers need the provider/ prefix for model routing.
	if provider == "llamacpp" || provider == "ollama" {
		for i, m := range models {
			if !strings.Contains(m, "/") {
				models[i] = provider + "/" + m
			}
		}
	}

	sort.Strings(models)
	return models, nil
}

// handleModelsFetched processes the result of an async model fetch.
func (m Model) handleModelsFetched(msg modelsFetchedMsg) (Model, tea.Cmd) {
	m.setupFetching = false
	if msg.err != nil {
		m.setupFetchErr = msg.err.Error()
		return m, nil
	}

	m.setupModels = msg.models
	m.setupStep = setupStepAdvModels
	m.setupModeStep = 0
	m.setupRoleStep = 0
	m.setupModelIdx = 0
	m.setupScrollOff = 0
	m.setupSelections = [3][3]string{}
	m.preselectDefault()
	return m, nil
}

// --- Setup completion ---

// completeSetup finalises the wizard and transitions to chat.
// If selections is nil, defaults are used for model mappings.
func (m Model) completeSetup(providerKey, apiKey string, selections *[3][3]string) (Model, tea.Cmd) {
	// Extract agents
	targetDir := cliconfig.ExeDir()
	agentsDir := filepath.Join(targetDir, "agents")
	if err := extractDefaultAgents(agentsDir); err != nil {
		agentsDir = filepath.Join(cliconfig.Dir(), "agents")
		_ = extractDefaultAgents(agentsDir)
	}

	var ucfg *cliconfig.UnifiedConfig
	if selections != nil {
		// Advanced: build config from user selections
		ucfg = &cliconfig.UnifiedConfig{
			Mode:     "local",
			CostMode: "normal",
			Modes:    make(map[string]cliconfig.UnifiedModeMapping),
		}
		for i, name := range setupModeNames {
			ucfg.Modes[name] = cliconfig.UnifiedModeMapping{
				Main:         selections[i][0],
				FileExplorer: selections[i][1],
				SubAgent:     selections[i][2],
			}
		}
	} else {
		// Quick / skip: use defaults for the provider
		ucfg = cliconfig.DefaultUnifiedConfigForProvider(providerKey, apiKey)
	}

	// Set the API key
	setProviderAPIKey(ucfg, providerKey, apiKey)
	ucfg.AgentsDir = agentsDir

	configPath, err := cliconfig.SaveUnifiedConfig(ucfg)
	if err != nil {
		m.state = StateChat
		m.messages = append(m.messages, ChatMessage{
			Role:    "assistant",
			Content: fmt.Sprintf("Failed to save config: %v", err),
		})
		return m, nil
	}

	m.state = StateChat
	m.localCfg = ucfg.ToLegacyConfig()
	m.unifiedCfg = ucfg
	m.localStore = openLocalStore(m.log)
	m.conversationID = uuid.NewString()
	m.welcomeCollapsed = true
	m.costMode = costmode.ModeNormal
	m.messages = append(m.messages, ChatMessage{
		Role: "assistant",
		Content: fmt.Sprintf("Config saved to %s\n\nTo customize models or add more API keys, edit:\n  %s",
			configPath, configPath),
	})

	return m, tea.Batch(initLocalRuntimeFromConfig(ucfg), checkForUpdateCmd(), tickCmd())
}

// setProviderAPIKey sets the correct API key field on a UnifiedConfig.
func setProviderAPIKey(cfg *cliconfig.UnifiedConfig, provider, key string) {
	switch provider {
	case "openrouter":
		cfg.APIKeys.OpenRouter = key
	case "groq":
		cfg.APIKeys.Groq = key
	case "cerebras":
		cfg.APIKeys.Cerebras = key
	case "together":
		cfg.APIKeys.Together = key
	case "openai":
		cfg.APIKeys.OpenAI = key
	case "anthropic":
		cfg.APIKeys.Anthropic = key
	case "ollama":
		cfg.APIKeys.OllamaURL = key
	case "llamacpp":
		cfg.APIKeys.LlamacppURL = key
	}
}

// extractDefaultAgents extracts embedded agent YAMLs to a target directory.
func extractDefaultAgents(targetDir string) error {
	if err := os.MkdirAll(targetDir, 0o755); err != nil {
		return err
	}
	entries, err := agentdata.FS.ReadDir(".")
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		data, err := agentdata.FS.ReadFile(entry.Name())
		if err != nil {
			continue
		}
		_ = os.WriteFile(filepath.Join(targetDir, entry.Name()), data, 0o644)
	}
	return nil
}
