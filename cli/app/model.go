// Package app provides the Bubble Tea TUI models for the BujiCoder CLI.
package app

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/glamour"
	glamourstyles "github.com/charmbracelet/glamour/styles"
	"github.com/charmbracelet/lipgloss"
	"github.com/google/uuid"
	"github.com/rs/zerolog"

	agentdata "github.com/TechnoAllianceAE/bujicoder/agents"
	"github.com/TechnoAllianceAE/bujicoder/shared/logging"
	cliconfig "github.com/TechnoAllianceAE/bujicoder/cli/config"
	"github.com/TechnoAllianceAE/bujicoder/shared/store"
	"github.com/TechnoAllianceAE/bujicoder/shared/agent"
	"github.com/TechnoAllianceAE/bujicoder/shared/agentruntime"
	"github.com/TechnoAllianceAE/bujicoder/shared/costmode"
	"github.com/TechnoAllianceAE/bujicoder/shared/llm"
	"github.com/TechnoAllianceAE/bujicoder/shared/mcp"
	"github.com/TechnoAllianceAE/bujicoder/shared/tools"
)

// State represents the current TUI state.
type State int

const (
	StateChat State = iota
	StateHistory
	StateSetup // First-run: provider selection
)

const bujicoderBanner = `
 ██████  ██    ██      ██ ██  ██████  ██████  ██████  ███████ ██████
 ██   ██ ██    ██      ██ ██ ██      ██    ██ ██   ██ ██      ██   ██
 ██████  ██    ██      ██ ██ ██      ██    ██ ██   ██ █████   ██████
 ██   ██ ██    ██ ██   ██ ██ ██      ██    ██ ██   ██ ██      ██   ██
 ██████   ██████   █████  ██  ██████  ██████  ██████  ███████ ██   ██`

var spinnerFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

// slashCommands is the canonical list of slash commands for autocomplete.
var slashCommands = []struct{ cmd, desc string }{
	{"/new", "Start a new conversation"},
	{"/mode", "Switch mode (normal · heavy · max · plan)"},
	{"/history", "Browse and resume conversations"},
	{"/search", "Search conversation history"},
	{"/copy", "Copy last response to clipboard"},
	{"/about", "Show version and system info"},
	{"/init", "Analyse project docs and explain codebase"},
	{"/mcp", "Manage MCP servers (list · add · remove)"},
	{"/models", "List available models and mode mappings"},
	{"/refresh", "Refresh model-agent assignments"},
	{"/update", "Check for updates"},
	{"/goal", "Auto-decompose a goal into tasks and execute with specialist agents"},
	{"/memory", "View project memory (BUJI.md)"},
	{"/verbose", "Toggle verbose session log (saved to .bujicoder/logs/)"},
	{"/help", "Show help and keyboard shortcuts"},
	{"/quit", "Exit BujiCoder"},
	{"/exit", "Exit BujiCoder"},
}

// modeOptions defines the available modes for the /mode picker.
var modeOptions = []struct{ name, desc string }{
	{"normal", "Balanced speed and quality"},
	{"heavy", "Higher quality, slower responses"},
	{"max", "Maximum quality for complex tasks"},
	{"plan", "Read-only analysis and documentation"},
}

// Styles
var (
	titleStyle    = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#7C3AED"))
	bannerStyle   = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#7C3AED"))
	userStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("#06B6D4")).Bold(true)
	assistStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("#A78BFA"))
	dimStyle      = lipgloss.NewStyle().Foreground(lipgloss.Color("#6B7280"))
	promptStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("#10B981")).Bold(true)
	errorStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("#EF4444"))
	toolStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("#F59E0B")).Bold(true)
	stepStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("#8B5CF6"))
	successStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("#10B981"))
	timeStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("#6B7280"))
	resultStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("#6B7280")).Italic(true)
	updateStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("#F59E0B")).Bold(true)
	sectionStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("#F59E0B")).Bold(true)
	cmdStyle      = lipgloss.NewStyle().Foreground(lipgloss.Color("#06B6D4"))
	descStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("#9CA3AF"))
	tipStyle      = lipgloss.NewStyle().Foreground(lipgloss.Color("#A78BFA"))
	inputBoxStyle = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(lipgloss.Color("#4B5563")).
			Padding(0, 1)
)

// ---------------------------------------------------------------------------
// Activity log -- tracks tool calls, results, and steps during a response
// ---------------------------------------------------------------------------

type activityKind int

const (
	actToolCall activityKind = iota
	actToolResult
	actStepStart
	actStatus
)

// agentLiveState tracks the live status of a single active sub-agent.
type agentLiveState struct {
	currentTool string // e.g. "Reading", "Searching"
	currentArgs string // e.g. "main.go"
	lastStatus  string // last status message text
	step        int
	done        bool
	spawnOrder  int // for deterministic column ordering
}

type activityEntry struct {
	Kind       activityKind
	AgentID    string
	ToolName   string
	ToolCallID string
	Args       string // human-readable parsed args
	Result     string // truncated result summary
	IsError    bool
	Step       int
	Timestamp  time.Time
}

// ChatMessage represents a chat message in the TUI.
type ChatMessage struct {
	Role            string
	Content         string
	RenderedContent string          // cached glamour-rendered markdown (empty = not rendered yet)
	Activities      []activityEntry // activity log associated with this response
	Elapsed         time.Duration   // total elapsed time for this response
	Steps           int             // total steps in this response
	InputTokens     int             // total input tokens used
	OutputTokens    int             // total output tokens used
	CostCents       int64           // total cost in cents
}

// ConversationMessage is a simplified message type for resume results.
type ConversationMessage struct {
	ID          string
	Role        string
	Content     string
	SequenceNum int
	CreatedAt   string
}

// Model is the top-level Bubble Tea model.
type Model struct {
	version         string
	commit          string
	buildTime       string
	state           State
	width           int
	height          int
	input           string
	cursorPos       int // cursor position within input (0 = before first char)
	messages        []ChatMessage
	streaming       bool
	streamBuf       string
	subAgentStreams  map[string]string // buffers stream string per sub-agent ID
	streamCh        chan tea.Msg
	err             error
	costMode        costmode.Mode
	planMode        bool
	permissions     *tools.ProjectPermissions
	conversationID  string

	// Verbose streaming state
	activities   []activityEntry
	currentStep  int
	startTime    time.Time
	spinnerFrame int
	totalSteps   int
	lastActivity string // current activity description for spinner line
	inputTokens  int
	outputTokens int
	costCents    int64
	liveAgents   map[string]*agentLiveState // per-agent live state for column view
	spawnCounter int                        // monotonic counter for spawn order

	// Viewport for scrollable content
	viewport viewport.Model
	ready    bool // true after first WindowSizeMsg initializes viewport

	// Markdown renderer
	mdRenderer *glamour.TermRenderer

	// Update notification (set by background check)
	updateVersion string

	// History browser state
	historyItems  []store.ConversationSummary
	historyCursor int
	historyOffset int

	// Welcome screen collapse state
	welcomeCollapsed bool // true = sections collapsed, false = expanded

	// Local mode config
	localCfg   *cliconfig.Config        // cached config for local mode helpers
	unifiedCfg *cliconfig.UnifiedConfig // unified YAML config
	localStore *store.Store        // local conversation persistence

	// Setup wizard state
	setupStep       int            // setup step constant (see setup.go)
	setupAPIKey     string         // API key being entered
	setupMode       int            // 0=quick, 1=advanced
	setupProvider   int            // advanced provider index (0-5)
	setupModels     []string       // fetched model IDs
	setupModelIdx   int            // current selection in model list
	setupModeStep   int            // cost mode (0=normal, 1=heavy, 2=max)
	setupRoleStep   int            // role (0=main, 1=file_explorer, 2=sub_agent)
	setupSelections [3][3]string   // [mode][role] = model ID
	setupFetchErr   string         // error from model fetch
	setupFetching   bool           // loading spinner
	setupScrollOff  int            // scroll offset for model list

	// Structured logger (writes JSON to ~/.bujicoder/logs/)
	log zerolog.Logger

	// Local agent runtime (CLI-side tool execution)
	agentRuntime    *agentruntime.Runtime
	agentRegistry   *agent.Registry
	toolRegistry    *tools.Registry
	mcpManager      *mcp.Manager
	llmRegistry     *llm.Registry
	modelResolver   *costmode.Resolver
	runtimeReady    bool        // true after agent defs fetched and runtime initialized
	askQuestionCh   chan string // sends user question from ask_user tool to TUI
	askAnswerCh     chan string // sends user answer back to ask_user tool
	pendingQuestion string      // question displayed to user during ask_user

	approvalCmdCh      chan string // tool sends "command\nreason" to TUI
	approvalRespCh     chan bool   // TUI sends decision back to tool
	pendingApproval    string      // formatted prompt text shown to user
	pendingApprovalCmd string      // raw command for display

	// Slash command autocomplete state
	acMatches []int // indices into slashCommands for current matches
	acCursor  int   // selected item in acMatches
	acVisible bool  // whether autocomplete dropdown is shown

	// Mode picker state (shown when user types /mode without argument)
	modePickerVisible bool
	modePickerCursor  int

	// Prompt history (Up/Down to recall previous inputs)
	promptHistory []string // past user inputs, oldest first
	historyIdx    int      // -1 = not browsing; 0..len-1 = browsing
	historySaved  string   // input saved when user starts browsing

	// Verbose session logging (/verbose toggle)
	verboseEnabled  bool              // whether verbose logging is active
	verboseFile     *os.File          // open log file handle (nil when disabled)
	verboseDeltaBuf map[string]string // per-agent buffered LLM text, flushed at boundaries

	// Viewport rendering debounce
	viewportDirty bool // true when content changed and viewport needs rebuild
}

// NewModel creates the initial TUI model.
func NewModel(version, commit, buildTime string, verbose bool) Model {
	mdRenderer, _ := newMarkdownRenderer(80)

	// Try unified config first.
	ucfg := cliconfig.LoadUnifiedConfig()

	// Initialize logger early — available even during setup.
	log := logging.New(logging.Config{Verbose: verbose})

	// No config found -> first-run setup.
	if ucfg == nil {
		return Model{
			version:        version,
			commit:         commit,
			buildTime:      buildTime,
			state:          StateSetup,
			messages:       []ChatMessage{},
			costMode:       costmode.ModeNormal,
			conversationID: uuid.NewString(),
			mdRenderer:     mdRenderer,
			historyIdx:     -1,
			log:            log,
		}
	}

	// Parse cost mode.
	mode := costmode.ModeNormal
	planMode := false
	if ucfg.CostMode != "" {
		if ucfg.CostMode == "plan" {
			planMode = true
			mode = costmode.ModeNormal
		} else {
			mode = costmode.ParseMode(ucfg.CostMode)
		}
	}

	legacyCfg := ucfg.ToLegacyConfig()

	return Model{
		version:          version,
		commit:           commit,
		buildTime:        buildTime,
		state:            StateChat,
		messages:         []ChatMessage{},
		costMode:         mode,
		planMode:         planMode,
		conversationID:   uuid.NewString(),
		mdRenderer:       mdRenderer,
		localCfg:         legacyCfg,
		unifiedCfg:       ucfg,
		localStore:       openLocalStore(log),
		welcomeCollapsed: true,
		historyIdx:       -1,
		log:              log,
	}
}

// ---------------------------------------------------------------------------
// Verbose session logging
// ---------------------------------------------------------------------------

// verboseLog writes a formatted line to the verbose log file (if enabled).
func (m *Model) verboseLog(format string, args ...any) {
	if !m.verboseEnabled || m.verboseFile == nil {
		return
	}
	ts := time.Now().Format("15:04:05.000")
	line := fmt.Sprintf("[%s] %s\n", ts, fmt.Sprintf(format, args...))
	m.verboseFile.WriteString(line)
}

// flushVerboseDeltas writes any buffered LLM text for the given agent and clears the buffer.
func (m *Model) flushVerboseDeltas(agent string) {
	if m.verboseDeltaBuf == nil {
		return
	}
	if buf := m.verboseDeltaBuf[agent]; len(strings.TrimSpace(buf)) > 0 {
		m.verboseLog("[agent:%s] LLM ▸ %s", agent, strings.TrimSpace(buf))
		delete(m.verboseDeltaBuf, agent)
	}
}

// verboseAgentTag returns a log prefix like "[agent:researcher model:openai/gpt-4o]".
func verboseAgentTag(agent, model string) string {
	if model != "" {
		return fmt.Sprintf("[agent:%s model:%s]", agent, model)
	}
	return fmt.Sprintf("[agent:%s]", agent)
}

// verboseLogEvent logs an agent runtime event with context.
func (m *Model) verboseLogEvent(ev agentruntime.Event) {
	if !m.verboseEnabled || m.verboseFile == nil {
		return
	}
	agent := ev.AgentID
	if agent == "" {
		agent = "base"
	}
	tag := verboseAgentTag(agent, ev.Model)
	switch ev.Type {
	case agentruntime.EventStepStart:
		m.verboseLog("──── STEP %d %s ────", ev.StepNumber, tag)
	case agentruntime.EventStepEnd:
		m.flushVerboseDeltas(agent)
		m.verboseLog("──── STEP %d END %s ────", ev.StepNumber, tag)
	case agentruntime.EventDelta:
		// Accumulate text deltas — flushed at step end, tool call, or completion boundaries.
		if m.verboseDeltaBuf == nil {
			m.verboseDeltaBuf = make(map[string]string)
		}
		m.verboseDeltaBuf[agent] += ev.Text
	case agentruntime.EventToolCall:
		m.flushVerboseDeltas(agent)
		m.verboseLog("%s TOOL CALL ▸ %s (id:%s)\n         args: %s", tag, ev.ToolName, ev.ToolCallID, ev.ArgsJSON)
	case agentruntime.EventToolResult:
		result := ev.Text
		if len(result) > 2000 {
			result = result[:2000] + "... (truncated)"
		}
		errTag := ""
		if ev.IsError {
			errTag = " [ERROR]"
		}
		m.verboseLog("%s TOOL RESULT%s ▸ %s (id:%s)\n         %s", tag, errTag, ev.ToolName, ev.ToolCallID, result)
	case agentruntime.EventStatus:
		m.verboseLog("%s STATUS ▸ %s", tag, ev.Text)
	case agentruntime.EventComplete:
		m.flushVerboseDeltas(agent)
		if ev.Usage != nil {
			m.verboseLog("%s COMPLETE ▸ input_tokens=%d output_tokens=%d cost=%.4f¢", tag, ev.Usage.InputTokens, ev.Usage.OutputTokens, float64(ev.Usage.CostCents))
		} else {
			m.verboseLog("%s COMPLETE", tag)
		}
	case agentruntime.EventCompact:
		m.verboseLog("%s CONTEXT COMPACTED ▸ %s", tag, ev.Text)
	case agentruntime.EventError:
		m.flushVerboseDeltas(agent)
		m.verboseLog("%s ERROR ▸ %s", tag, ev.Text)
	}
}

// startVerboseLog creates a new timestamped log file in .bujicoder/logs/.
func (m *Model) startVerboseLog() error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	logsDir := filepath.Join(home, ".bujicoder", "logs")
	if err := os.MkdirAll(logsDir, 0o755); err != nil {
		return err
	}
	filename := fmt.Sprintf("session_%s.log", time.Now().Format("2006-01-02_15-04-05"))
	f, err := os.Create(filepath.Join(logsDir, filename))
	if err != nil {
		return err
	}
	m.verboseFile = f
	m.verboseEnabled = true
	// Write header.
	m.verboseLog("═══════════════════════════════════════════════════════════════")
	m.verboseLog("BujiCoder Verbose Session Log")
	m.verboseLog("Started: %s", time.Now().Format(time.RFC3339))
	m.verboseLog("Cost mode: %s", string(m.costMode))
	cwd, _ := os.Getwd()
	m.verboseLog("Working dir: %s", cwd)
	m.verboseLog("═══════════════════════════════════════════════════════════════")
	return nil
}

// stopVerboseLog closes the current verbose log file.
func (m *Model) stopVerboseLog() {
	if m.verboseFile != nil {
		m.verboseLog("═══════════════════════════════════════════════════════════════")
		m.verboseLog("Session ended: %s", time.Now().Format(time.RFC3339))
		m.verboseLog("═══════════════════════════════════════════════════════════════")
		m.verboseFile.Close()
		m.verboseFile = nil
	}
	m.verboseEnabled = false
}

// ---------------------------------------------------------------------------
// Tea messages
// ---------------------------------------------------------------------------

type streamChunkMsg struct {
	text    string
	agentID string
}

type streamDoneMsg struct {
	err error
}

type toolCallMsg struct {
	toolCallID string
	toolName   string
	argsJSON   string
	agentID    string
}

type toolResultMsg struct {
	toolCallID string
	toolName   string
	text       string
	isError    bool
	agentID    string
}

type stepStartMsg struct {
	step    int
	agentID string
}

type stepEndMsg struct {
	step    int
	agentID string
}

type statusMsg struct {
	agentID string
	text    string
}

type completeMsg struct {
	text         string
	inputTokens  int
	outputTokens int
	costCents    int64
}

type tickMsg time.Time

type historyResultMsg struct {
	conversations []store.ConversationSummary
	err           error
}

type resumeResultMsg struct {
	conversationID string
	messages       []ConversationMessage
	err            error
}

type searchResultMsg struct {
	results []store.SearchResult
	err     error
}

type updateCheckResultMsg struct {
	version string
}

type clipboardResultMsg struct{ err error }

type runtimeInitMsg struct {
	agentRegistry *agent.Registry
	modelResolver *costmode.Resolver
	err           error
}

type askUserQuestionMsg struct {
	question string
}

type approvalRequestMsg struct {
	command string
	reason  string
}

// ---------------------------------------------------------------------------
// Tool display helpers
// ---------------------------------------------------------------------------

func toolDisplayName(name string) string {
	switch name {
	case "read_files":
		return "Reading"
	case "write_file":
		return "Writing"
	case "str_replace":
		return "Editing"
	case "code_search":
		return "Searching"
	case "run_terminal_command":
		return "Running"
	case "list_directory":
		return "Listing"
	default:
		return "Using " + name
	}
}

func shortAgentName(agentID string) string {
	agentID = strings.TrimSpace(agentID)
	if agentID == "" {
		return ""
	}
	if strings.Contains(agentID, "/") {
		parts := strings.Split(agentID, "/")
		return parts[len(parts)-1]
	}
	return agentID
}

func agentPrefix(agentID string) string {
	name := shortAgentName(agentID)
	if name == "" {
		return ""
	}
	return "[" + name + "]"
}

func parseToolArgs(toolName, argsJSON string) string {
	if argsJSON == "" {
		return ""
	}

	var args map[string]json.RawMessage
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return ""
	}

	switch toolName {
	case "read_files":
		var paths []string
		if raw, ok := args["paths"]; ok {
			_ = json.Unmarshal(raw, &paths)
		}
		if len(paths) > 0 {
			if len(paths) <= 3 {
				return strings.Join(paths, ", ")
			}
			return fmt.Sprintf("%s, %s, +%d more", paths[0], paths[1], len(paths)-2)
		}

	case "write_file":
		var path string
		if raw, ok := args["path"]; ok {
			_ = json.Unmarshal(raw, &path)
		}
		if path != "" {
			return path
		}

	case "str_replace":
		var path string
		if raw, ok := args["path"]; ok {
			_ = json.Unmarshal(raw, &path)
		}
		if path != "" {
			return path
		}

	case "code_search":
		var pattern, glob string
		if raw, ok := args["pattern"]; ok {
			_ = json.Unmarshal(raw, &pattern)
		}
		if raw, ok := args["glob"]; ok {
			_ = json.Unmarshal(raw, &glob)
		}
		if pattern != "" {
			if glob != "" {
				return fmt.Sprintf("\"%s\" in %s", pattern, glob)
			}
			return fmt.Sprintf("\"%s\"", pattern)
		}

	case "run_terminal_command":
		var command string
		if raw, ok := args["command"]; ok {
			_ = json.Unmarshal(raw, &command)
		}
		if command != "" {
			if len(command) > 60 {
				command = command[:60] + "..."
			}
			return command
		}

	case "list_directory":
		var path string
		if raw, ok := args["path"]; ok {
			_ = json.Unmarshal(raw, &path)
		}
		if path != "" {
			return path
		}
	}

	return ""
}

func truncateResult(text string, isError bool) string {
	if text == "" {
		return "done"
	}
	if isError {
		if len(text) > 80 {
			return text[:80] + "..."
		}
		return text
	}
	lines := strings.Count(text, "\n") + 1
	if lines > 1 {
		return fmt.Sprintf("%d lines", lines)
	}
	if len(text) > 80 {
		return text[:80] + "..."
	}
	return text
}

func formatElapsed(d time.Duration) string {
	if d < time.Second {
		return fmt.Sprintf("%dms", d.Milliseconds())
	}
	return fmt.Sprintf("%.1fs", d.Seconds())
}

// newMarkdownRenderer creates a glamour TermRenderer with the dark theme,
// dracula syntax highlighting for code blocks, and the given word-wrap width.
func newMarkdownRenderer(width int) (*glamour.TermRenderer, error) {
	if width <= 0 {
		width = 80
	}
	wrapWidth := width - 8
	if wrapWidth < 40 {
		wrapWidth = 40
	}

	style := glamourstyles.DarkStyleConfig
	style.CodeBlock.Theme = "dracula"
	style.CodeBlock.Chroma = nil

	return glamour.NewTermRenderer(
		glamour.WithStyles(style),
		glamour.WithWordWrap(wrapWidth),
	)
}

// renderMarkdown renders markdown content to styled terminal output.
func renderMarkdown(r *glamour.TermRenderer, content string) string {
	if r == nil || content == "" {
		return content
	}
	rendered, err := r.Render(content)
	if err != nil {
		return content
	}
	return strings.TrimRight(rendered, "\n")
}

// ---------------------------------------------------------------------------
// Commands
// ---------------------------------------------------------------------------

func tickCmd() tea.Cmd {
	return tea.Tick(100*time.Millisecond, func(t time.Time) tea.Msg {
		return tickMsg(t)
	})
}

// imageExtensions lists file extensions that should be treated as image attachments.
var imageExtensions = map[string]string{
	".png":  "image/png",
	".jpg":  "image/jpeg",
	".jpeg": "image/jpeg",
	".gif":  "image/gif",
	".webp": "image/webp",
}

// extractImageParts scans user input for @path references to image files,
// reads and base64-encodes them, and returns the cleaned text + image content parts.
func extractImageParts(input string) (cleanedText string, imageParts []llm.ContentPart) {
	words := strings.Fields(input)
	var cleanedWords []string

	for _, word := range words {
		if !strings.HasPrefix(word, "@") || len(word) < 3 {
			cleanedWords = append(cleanedWords, word)
			continue
		}

		filePath := strings.TrimPrefix(word, "@")
		ext := strings.ToLower(filepath.Ext(filePath))
		mediaType, isImage := imageExtensions[ext]
		if !isImage {
			cleanedWords = append(cleanedWords, word)
			continue
		}

		if !filepath.IsAbs(filePath) {
			cwd, _ := os.Getwd()
			filePath = filepath.Join(cwd, filePath)
		}

		data, err := os.ReadFile(filePath)
		if err != nil {
			cleanedWords = append(cleanedWords, word)
			continue
		}

		const maxImageSize = 20 * 1024 * 1024
		if len(data) > maxImageSize {
			cleanedWords = append(cleanedWords, fmt.Sprintf("[%s: too large, max 20MB]", filepath.Base(filePath)))
			continue
		}

		encoded := base64.StdEncoding.EncodeToString(data)
		dataURI := fmt.Sprintf("data:%s;base64,%s", mediaType, encoded)

		imageParts = append(imageParts, llm.ContentPart{
			Type: "image_url",
			ImageURL: &llm.ImageURL{
				URL:       dataURI,
				MediaType: mediaType,
			},
		})

		cleanedWords = append(cleanedWords, fmt.Sprintf("[attached: %s]", filepath.Base(filePath)))
	}

	cleanedText = strings.Join(cleanedWords, " ")
	return
}

// sendMessageLocal runs the agent loop locally using the CLI-side runtime.
// The optional eventLog callback is called for every agent event (used by /verbose).
func sendMessageLocal(
	rt *agentruntime.Runtime,
	agentReg *agent.Registry,
	resolver *costmode.Resolver,
	messages []ChatMessage,
	ch chan tea.Msg,
	mode costmode.Mode,
	planMode bool,
	convID string,
	eventLog ...func(agentruntime.Event),
) tea.Cmd {
	return func() tea.Msg {
		ctx := context.Background()

		// Build message history as llm.Message slice.
		var history []llm.Message
		var currentMsg string
		for i, m := range messages {
			if i < len(messages)-1 {
				history = append(history, llm.Message{
					Role:    m.Role,
					Content: []llm.ContentPart{{Type: "text", Text: m.Content}},
				})
			} else {
				currentMsg = m.Content
			}
		}

		// Extract image attachments from @path references in user input.
		cleanedMsg, userImages := extractImageParts(currentMsg)
		currentMsg = cleanedMsg

		if planMode {
			currentMsg = "[PLAN MODE] You are in documentation-only mode. Do NOT modify any source code files. " +
				"You may: READ any files for understanding, CREATE or MODIFY only .md files, analyze code, write plans and documentation. " +
				"Do not use write_file or str_replace on non-.md files.\n\n" + currentMsg
		}

		agentDef, ok := agentReg.Get("base")
		if !ok {
			close(ch)
			return streamDoneMsg{err: fmt.Errorf("base agent not found in registry")}
		}

		// Apply cost mode.
		if mode != "" && resolver != nil {
			agentDef = agentDef.WithCostMode(mode, resolver)
		}

		cwd, _ := os.Getwd()

		runCfg := agentruntime.RunConfig{
			AgentDef:      agentDef,
			UserMessage:   currentMsg,
			UserImages:    userImages,
			History:       history,
			ProjectRoot:   cwd,
			CostMode:      mode,
			ModelResolver: resolver,
			OnEvent: func(ev agentruntime.Event) {
				if len(eventLog) > 0 && eventLog[0] != nil {
					eventLog[0](ev)
				}
				switch ev.Type {
				case agentruntime.EventDelta:
					ch <- streamChunkMsg{text: ev.Text, agentID: ev.AgentID}
				case agentruntime.EventToolCall:
					ch <- toolCallMsg{
						toolCallID: ev.ToolCallID,
						toolName:   ev.ToolName,
						argsJSON:   ev.ArgsJSON,
						agentID:    ev.AgentID,
					}
				case agentruntime.EventToolResult:
					ch <- toolResultMsg{
						toolCallID: ev.ToolCallID,
						toolName:   ev.ToolName,
						text:       ev.Text,
						isError:    ev.IsError,
						agentID:    ev.AgentID,
					}
				case agentruntime.EventStepStart:
					ch <- stepStartMsg{step: ev.StepNumber, agentID: ev.AgentID}
				case agentruntime.EventStepEnd:
					ch <- stepEndMsg{step: ev.StepNumber, agentID: ev.AgentID}
				case agentruntime.EventStatus:
					ch <- statusMsg{agentID: ev.AgentID, text: ev.Text}
				case agentruntime.EventComplete:
					if ev.Usage != nil {
						ch <- completeMsg{
							text:         ev.Text,
							inputTokens:  ev.Usage.InputTokens,
							outputTokens: ev.Usage.OutputTokens,
							costCents:    ev.Usage.CostCents,
						}
					}
				case agentruntime.EventError:
					ch <- toolResultMsg{
						toolName: "error",
						text:     ev.Text,
						isError:  true,
						agentID:  ev.AgentID,
					}
				}
			},
		}

		result, err := rt.Run(ctx, runCfg)

		// Send completion event with usage.
		if result != nil {
			ch <- completeMsg{
				inputTokens:  result.TotalInputTokens,
				outputTokens: result.TotalOutputTokens,
				costCents:    result.TotalCredits,
			}
		}

		close(ch)

		return streamDoneMsg{err: err}
	}
}

// sendCoordinatedGoal runs the coordinator pattern: decompose goal -> execute tasks -> synthesize.
// Accepts an optional parent context for cancellation support; defaults to context.Background().
// The optional eventLog callback is called for every agent event (used by /verbose).
func sendCoordinatedGoal(
	rt *agentruntime.Runtime,
	agentReg *agent.Registry,
	resolver *costmode.Resolver,
	goal string,
	ch chan tea.Msg,
	mode costmode.Mode,
	eventLog func(agentruntime.Event),
	parentCtx ...context.Context,
) tea.Cmd {
	return func() tea.Msg {
		var ctx context.Context
		if len(parentCtx) > 0 && parentCtx[0] != nil {
			ctx = parentCtx[0]
		} else {
			ctx = context.Background()
		}

		agentDef, ok := agentReg.Get("base")
		if !ok {
			close(ch)
			return streamDoneMsg{err: fmt.Errorf("base agent not found")}
		}

		if mode != "" && resolver != nil {
			agentDef = agentDef.WithCostMode(mode, resolver)
		}

		cwd, _ := os.Getwd()

		cfg := agentruntime.RunConfig{
			AgentDef:      agentDef,
			ProjectRoot:   cwd,
			CostMode:      mode,
			ModelResolver: resolver,
			SharedMemory:  agentruntime.NewSharedMemory(),
			OnEvent: func(ev agentruntime.Event) {
				if eventLog != nil {
					eventLog(ev)
				}
				switch ev.Type {
				case agentruntime.EventDelta:
					ch <- streamChunkMsg{text: ev.Text, agentID: ev.AgentID}
				case agentruntime.EventToolCall:
					ch <- toolCallMsg{
						toolCallID: ev.ToolCallID,
						toolName:   ev.ToolName,
						argsJSON:   ev.ArgsJSON,
						agentID:    ev.AgentID,
					}
				case agentruntime.EventToolResult:
					ch <- toolResultMsg{
						toolCallID: ev.ToolCallID,
						toolName:   ev.ToolName,
						text:       ev.Text,
						isError:    ev.IsError,
						agentID:    ev.AgentID,
					}
				case agentruntime.EventStepStart:
					ch <- stepStartMsg{step: ev.StepNumber, agentID: ev.AgentID}
				case agentruntime.EventStepEnd:
					ch <- stepEndMsg{step: ev.StepNumber, agentID: ev.AgentID}
				case agentruntime.EventStatus:
					ch <- statusMsg{agentID: ev.AgentID, text: ev.Text}
				case agentruntime.EventCompact:
					ch <- statusMsg{agentID: ev.AgentID, text: ev.Text}
				}
			},
		}

		result, err := rt.RunCoordinatedGoal(ctx, goal, cfg)

		if result != nil {
			ch <- streamChunkMsg{text: result.FinalText, agentID: "coordinator"}
			ch <- completeMsg{
				inputTokens:  0,
				outputTokens: 0,
				costCents:    0,
			}
		}

		close(ch)
		return streamDoneMsg{err: err}
	}
}

func checkForUpdateCmd() tea.Cmd {
	return func() tea.Msg {
		return updateCheckResultMsg{}
	}
}

// copyToClipboard writes text to the system clipboard using platform-specific commands.
func copyToClipboard(text string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("pbcopy")
	case "linux":
		if _, err := exec.LookPath("xclip"); err == nil {
			cmd = exec.Command("xclip", "-selection", "clipboard")
		} else {
			cmd = exec.Command("xsel", "--clipboard", "--input")
		}
	case "windows":
		cmd = exec.Command("clip")
	default:
		return fmt.Errorf("unsupported platform: %s", runtime.GOOS)
	}
	cmd.Stdin = strings.NewReader(text)
	return cmd.Run()
}

func copyToClipboardCmd(text string) tea.Cmd {
	return func() tea.Msg {
		return clipboardResultMsg{err: copyToClipboard(text)}
	}
}

// initLocalRuntimeFromConfig loads agents and model config using the unified config.
// Falls back to embedded agents if the agents dir doesn't exist on disk.
func initLocalRuntimeFromConfig(ucfg *cliconfig.UnifiedConfig) tea.Cmd {
	return func() tea.Msg {
		agentReg := agent.NewRegistry()

		// Determine agents directory.
		var agentsDir string
		if ucfg != nil {
			agentsDir = ucfg.GetAgentsDir()
		}

		// Try loading from disk first.
		loaded := false
		if agentsDir != "" {
			if err := agentReg.LoadDir(agentsDir); err == nil {
				loaded = true
			}
		}

		// Fall back to embedded agents.
		if !loaded {
			entries, err := agentdata.FS.ReadDir(".")
			if err != nil {
				return runtimeInitMsg{err: fmt.Errorf("read embedded agents: %w", err)}
			}
			for _, entry := range entries {
				if entry.IsDir() {
					continue
				}
				data, err := agentdata.FS.ReadFile(entry.Name())
				if err != nil {
					continue
				}
				def, err := agent.LoadBytes(data, entry.Name())
				if err != nil {
					continue
				}
				agentReg.Register(def)
			}
		}

		// Resolve model config: from unified config inline modes, or from disk file.
		var resolver *costmode.Resolver
		if ucfg != nil && len(ucfg.Modes) > 0 {
			modelCfg := ucfg.ToModelConfig()
			resolver = costmode.NewResolverFromConfig(modelCfg)
		}

		return runtimeInitMsg{
			agentRegistry: agentReg,
			modelResolver: resolver,
		}
	}
}


// localModelsInfo builds a string showing registered providers and model config for local mode.
func (m Model) localModelsInfo() string {
	var b strings.Builder
	b.WriteString("Local Mode -- Registered Providers\n\n")

	providerKeys := []struct {
		name     string
		provider string
		envVar   string
	}{
		{"anthropic", "anthropic", "ANTHROPIC_API_KEY"},
		{"openai", "openai", "OPENAI_API_KEY"},
		{"google", "google", "GOOGLE_AI_API_KEY"},
		{"xai", "xai", "XAI_API_KEY"},
		{"z-ai", "zai", "ZAI_API_KEY"},
		{"together", "together", "TOGETHER_API_KEY"},
		{"openrouter", "openrouter", "OPENROUTER_API_KEY"},
		{"ollama", "ollama", "OLLAMA_URL"},
		{"llamacpp", "llamacpp", "LLAMACPP_URL"},
	}

	found := false
	for _, p := range providerKeys {
		hasKey := false
		if m.unifiedCfg != nil {
			hasKey = m.unifiedCfg.GetAPIKey(p.provider) != ""
		}
		if !hasKey {
			hasKey = os.Getenv(p.envVar) != ""
		}
		if hasKey {
			b.WriteString(fmt.Sprintf("  + %s\n", p.name))
			found = true
		}
	}
	if !found {
		b.WriteString("  (none -- configure API keys in bujicoder.yaml)\n")
	}

	if m.modelResolver != nil {
		b.WriteString("\nModel Assignments:\n")
		cfg := m.modelResolver.GetConfig()
		for _, mode := range costmode.AllModes() {
			if mapping, ok := cfg.Modes[mode]; ok {
				b.WriteString(fmt.Sprintf("\n  %s:\n", mode))
				b.WriteString(fmt.Sprintf("    main:           %s\n", mapping.Main))
				b.WriteString(fmt.Sprintf("    file_explorer:  %s\n", mapping.FileExplorer))
				b.WriteString(fmt.Sprintf("    sub_agent:      %s\n", mapping.SubAgent))
				for agentID, model := range mapping.AgentOverrides {
					b.WriteString(fmt.Sprintf("    %s: %s\n", agentID, model))
				}
			}
		}
	}

	return b.String()
}

// registerLocalProviders registers LLM providers from the unified config (with env var fallback)
// for standalone local mode (no gateway required).
func registerLocalProviders(reg *llm.Registry, ucfg *cliconfig.UnifiedConfig) {
	getKey := func(provider, envVar string) string {
		if ucfg != nil {
			if k := ucfg.GetAPIKey(provider); k != "" {
				return k
			}
		}
		return os.Getenv(envVar)
	}

	// Resolve configured request timeout (default 90s).
	var timeout time.Duration
	if ucfg != nil && ucfg.RequestTimeout > 0 {
		timeout = time.Duration(ucfg.RequestTimeout) * time.Second
	}

	if key := getKey("anthropic", "ANTHROPIC_API_KEY"); key != "" {
		reg.Register(llm.NewAnthropicProvider(key, timeout))
	}
	if key := getKey("openai", "OPENAI_API_KEY"); key != "" {
		reg.Register(llm.NewOpenAIProvider(key, timeout))
	}
	if key := getKey("google", "GOOGLE_AI_API_KEY"); key != "" {
		reg.Register(llm.NewGeminiProvider(key, timeout))
	}
	if key := getKey("xai", "XAI_API_KEY"); key != "" {
		reg.Register(llm.NewXAIProvider(key, timeout))
	}
	if key := getKey("zai", "ZAI_API_KEY"); key != "" {
		reg.Register(llm.NewZAIProvider(key, timeout))
	}
	if key := getKey("together", "TOGETHER_API_KEY"); key != "" {
		reg.Register(llm.NewTogetherProvider(key, timeout))
	}
	if key := getKey("groq", "GROQ_API_KEY"); key != "" {
		reg.Register(llm.NewGroqProvider(key, timeout))
	}
	if key := getKey("cerebras", "CEREBRAS_API_KEY"); key != "" {
		reg.Register(llm.NewCerebrasProvider(key, timeout))
	}
	if key := getKey("kilocode", "KILOCODE_API_KEY"); key != "" {
		kiloProvider := llm.NewKilocodeProvider(key, timeout)
		reg.Register(kiloProvider)
		// If no OpenRouter key, Kilocode becomes the default fallback gateway.
		if getKey("openrouter", "OPENROUTER_API_KEY") == "" {
			reg.SetDefault(kiloProvider)
		}
	}
	if key := getKey("openrouter", "OPENROUTER_API_KEY"); key != "" {
		orProvider := llm.NewOpenRouterProvider(key, timeout)
		reg.Register(orProvider)
		reg.SetDefault(orProvider)
	}
	if u := getKey("ollama", "OLLAMA_URL"); u != "" {
		reg.Register(llm.NewOllamaProvider(u, timeout))
	}
	if u := getKey("llamacpp", "LLAMACPP_URL"); u != "" {
		reg.Register(llm.NewLlamaCppProvider(u, timeout))
	}
}

// listenForAskUser waits for a question from the ask_user tool and sends it to the TUI.
func listenForAskUser(questionCh chan string) tea.Cmd {
	return func() tea.Msg {
		question, ok := <-questionCh
		if !ok {
			return nil
		}
		return askUserQuestionMsg{question: question}
	}
}

// listenForApproval waits for a dangerous command approval request and sends it to the TUI.
func listenForApproval(cmdCh chan string) tea.Cmd {
	return func() tea.Msg {
		payload, ok := <-cmdCh
		if !ok {
			return nil
		}
		parts := strings.SplitN(payload, "\n", 2)
		command := parts[0]
		reason := ""
		if len(parts) > 1 {
			reason = parts[1]
		}
		return approvalRequestMsg{command: command, reason: reason}
	}
}

// Init returns the initial command.
func (m Model) Init() tea.Cmd {
	if m.state == StateSetup {
		return tickCmd() // needed for spinner during model fetch
	}
	if m.state == StateChat {
		return tea.Batch(initLocalRuntimeFromConfig(m.unifiedCfg), tickCmd())
	}
	return nil
}

// Update handles messages and syncs the viewport after every update.
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	m, cmd := m.handleUpdate(msg)
	// When not streaming, always rebuild viewport immediately.
	// During streaming, viewportDirty is set by tick (debounced) or structural events.
	if !m.streaming {
		m.viewportDirty = true
	}
	return m.syncViewport(), cmd
}

// handleUpdate contains the core message handling logic.
func (m Model) handleUpdate(msg tea.Msg) (Model, tea.Cmd) {
	switch msg := msg.(type) {

	case tea.KeyMsg:
		switch msg.String() {
		case "ctrl+c":
			if m.state == StateSetup {
				return m, tea.Quit
			}
			if m.mcpManager != nil {
				m.mcpManager.ShutdownAll()
			}
			m.stopVerboseLog()
			return m, tea.Quit

		default:
			// Delegate all non-ctrl+c keys in setup mode to the setup handler.
			if m.state == StateSetup {
				return m.handleSetupKeys(msg)
			}
		}

		switch msg.String() {
		case "shift+tab":
			// Cycle cost modes: normal -> heavy -> max -> plan -> normal
			if m.state == StateChat && !m.streaming {
				var newLabel string
				if m.planMode {
					m.planMode = false
					m.costMode = costmode.ModeNormal
					newLabel = "normal"
				} else {
					switch m.costMode {
					case costmode.ModeNormal:
						m.costMode = costmode.ModeHeavy
						newLabel = "heavy"
					case costmode.ModeHeavy:
						m.costMode = costmode.ModeMax
						newLabel = "max"
					case costmode.ModeMax:
						m.planMode = true
						m.costMode = costmode.ModeNormal
						newLabel = "plan"
					default:
						m.costMode = costmode.ModeNormal
						newLabel = "normal"
					}
				}
				m.messages = append(m.messages, ChatMessage{
					Role:    "assistant",
					Content: fmt.Sprintf("Mode -> %s", newLabel),
				})
				return m, nil
			}

		case "ctrl+y":
			if m.state == StateChat && !m.streaming {
				for i := len(m.messages) - 1; i >= 0; i-- {
					if m.messages[i].Role == "assistant" {
						return m, copyToClipboardCmd(m.messages[i].Content)
					}
				}
				m.messages = append(m.messages, ChatMessage{
					Role:    "assistant",
					Content: "No assistant response to copy.",
				})
				return m, nil
			}

		case "backspace":
			if len(m.input) > 0 && m.cursorPos > 0 && m.state == StateChat && (!m.streaming || m.pendingQuestion != "" || m.pendingApproval != "") {
				runes := []rune(m.input)
				m.input = string(runes[:m.cursorPos-1]) + string(runes[m.cursorPos:])
				m.cursorPos--
				m.historyIdx = -1 // reset history browsing on edit
				m.spinnerFrame = 0
				m.updateAutocomplete()
			}

		case "delete":
			runes := []rune(m.input)
			if m.cursorPos < len(runes) && m.state == StateChat && (!m.streaming || m.pendingQuestion != "" || m.pendingApproval != "") {
				m.input = string(runes[:m.cursorPos]) + string(runes[m.cursorPos+1:])
				m.historyIdx = -1
				m.spinnerFrame = 0
				m.updateAutocomplete()
			}

		case "enter":
			if m.state == StateHistory && len(m.historyItems) > 0 {
				selected := m.historyItems[m.historyCursor]
				m.state = StateChat
				m.historyItems = nil
				m.historyCursor = 0
				m.historyOffset = 0
				if m.localStore != nil {
					return m, resumeLocalConversation(m.localStore, selected.ID)
				}
				m.messages = append(m.messages, ChatMessage{Role: "assistant", Content: "Local history not available."})
				return m, nil
			}
			// Handle approval response.
			if m.state == StateChat && m.pendingApproval != "" && m.streaming {
				answer := strings.ToLower(strings.TrimSpace(m.input))
				approved := answer == "y" || answer == "yes"
				m.input = ""
				m.cursorPos = 0
				m.pendingApproval = ""
				m.pendingApprovalCmd = ""
				m.approvalRespCh <- approved
				return m, listenForApproval(m.approvalCmdCh)
			}

			// Handle ask_user answer.
			if m.state == StateChat && m.pendingQuestion != "" && m.streaming {
				answer := strings.TrimSpace(m.input)
				if answer == "" {
					answer = "(no answer)"
				}
				m.input = ""
				m.cursorPos = 0
				m.pendingQuestion = ""
				m.askAnswerCh <- answer
				return m, listenForAskUser(m.askQuestionCh)
			}

			// /quit and /exit should work immediately, even during streaming.
			if m.state == StateChat {
				trimmed := strings.TrimSpace(m.input)
				if trimmed == "/quit" || trimmed == "/exit" {
					m.input = ""
				m.cursorPos = 0
					if m.mcpManager != nil {
						m.mcpManager.ShutdownAll()
					}
					m.stopVerboseLog()
					return m, tea.Quit
				}
			}

			// Handle mode picker selection.
			if m.state == StateChat && m.modePickerVisible {
				selected := modeOptions[m.modePickerCursor]
				m.modePickerVisible = false
				m.input = ""
				m.cursorPos = 0
				if selected.name == "plan" {
					m.planMode = true
					m.costMode = costmode.ModeNormal
					if m.unifiedCfg != nil {
						m.unifiedCfg.CostMode = "plan"
						_, _ = cliconfig.SaveUnifiedConfig(m.unifiedCfg)
					}
					m.messages = append(m.messages, ChatMessage{
						Role:    "assistant",
						Content: "Switched to plan mode\n\nIn plan mode, BujiCoder will only read code for understanding and create/modify .md documentation files. No source code changes will be made.",
					})
					return m, nil
				}
				newMode := costmode.ParseMode(selected.name)
				m.costMode = newMode
				m.planMode = false
				if m.unifiedCfg != nil {
					m.unifiedCfg.CostMode = string(newMode)
					_, _ = cliconfig.SaveUnifiedConfig(m.unifiedCfg)
				}
				m.messages = append(m.messages, ChatMessage{
					Role:    "assistant",
					Content: fmt.Sprintf("Switched to %s mode", newMode),
				})
				return m, nil
			}

			if m.state == StateChat && !m.streaming && strings.TrimSpace(m.input) != "" {
				// Accept autocomplete selection on enter, then execute.
				if m.acVisible && len(m.acMatches) > 0 {
					selected := slashCommands[m.acMatches[m.acCursor]]
					m.input = selected.cmd
					m.cursorPos = len([]rune(m.input))
					m.acVisible = false
					m.acMatches = nil
					m.acCursor = 0
				}
				userMsg := strings.TrimSpace(m.input)

				if userMsg == "/new" {
					m.input = ""
				m.cursorPos = 0
					m.conversationID = uuid.NewString()
					m.messages = []ChatMessage{}
					m.err = nil
					m.messages = append(m.messages, ChatMessage{
						Role:    "assistant",
						Content: "Started new conversation.",
					})
					return m, nil
				}

				if userMsg == "/history" {
					m.input = ""
				m.cursorPos = 0
					if m.localStore != nil {
						return m, fetchLocalHistory(m.localStore)
					}
					m.messages = append(m.messages, ChatMessage{Role: "assistant", Content: "Local history not available."})
					return m, nil
				}

				if strings.HasPrefix(userMsg, "/resume") {
					m.input = ""
				m.cursorPos = 0
					parts := strings.Fields(userMsg)
					if len(parts) < 2 {
						m.messages = append(m.messages, ChatMessage{
							Role:    "assistant",
							Content: "Usage: /resume <conversation_id>",
						})
						return m, nil
					}
					if m.localStore != nil {
						return m, resumeLocalConversation(m.localStore, parts[1])
					}
					m.messages = append(m.messages, ChatMessage{Role: "assistant", Content: "Local history not available."})
					return m, nil
				}

				if strings.HasPrefix(userMsg, "/search") {
					m.input = ""
					m.cursorPos = 0
					parts := strings.Fields(userMsg)
					if len(parts) < 2 {
						m.messages = append(m.messages, ChatMessage{
							Role:    "assistant",
							Content: "Usage: /search <query>",
						})
						return m, nil
					}
					query := strings.Join(parts[1:], " ")
					if m.localStore != nil {
						return m, searchConversations(m.localStore, query)
					}
					m.messages = append(m.messages, ChatMessage{Role: "assistant", Content: "Search not available."})
					return m, nil
				}

				if userMsg == "/models" {
					m.input = ""
				m.cursorPos = 0
					m.messages = append(m.messages, ChatMessage{Role: "assistant", Content: m.localModelsInfo()})
					return m, nil
				}

				if userMsg == "/refresh" {
					m.input = ""
				m.cursorPos = 0
					// Reload unified config from disk.
					if newCfg := cliconfig.LoadUnifiedConfig(); newCfg != nil {
						m.unifiedCfg = newCfg
						m.localCfg = newCfg.ToLegacyConfig()
						m.costMode = costmode.ParseMode(newCfg.CostMode)
					}
					m.runtimeReady = false
					return m, initLocalRuntimeFromConfig(m.unifiedCfg)
				}

				if userMsg == "/usage" {
					m.input = ""
				m.cursorPos = 0
					m.messages = append(m.messages, ChatMessage{Role: "assistant", Content: "Usage tracking available with BujiCoder Enterprise. Visit community.bujicoder.com for details."})
					return m, nil
				}

				if userMsg == "/init" {
					m.input = ""
				m.cursorPos = 0
					m.messages = append(m.messages, ChatMessage{
						Role:    "assistant",
						Content: gatherCodebaseInfo(),
					})
					return m, nil
				}

				if userMsg == "/update" {
					m.input = ""
				m.cursorPos = 0
					if m.updateVersion != "" {
						m.messages = append(m.messages, ChatMessage{
							Role:    "assistant",
							Content: fmt.Sprintf("Update available: v%s -> v%s\n\nRun `buji update` from your terminal to install the latest version.", m.version, m.updateVersion),
						})
					} else {
						m.messages = append(m.messages, ChatMessage{
							Role:    "assistant",
							Content: fmt.Sprintf("You're running buji v%s (latest).", m.version),
						})
					}
					return m, nil
				}

				if userMsg == "/copy" {
					m.input = ""
				m.cursorPos = 0
					for i := len(m.messages) - 1; i >= 0; i-- {
						if m.messages[i].Role == "assistant" {
							return m, copyToClipboardCmd(m.messages[i].Content)
						}
					}
					m.messages = append(m.messages, ChatMessage{
						Role:    "assistant",
						Content: "No assistant response to copy.",
					})
					return m, nil
				}

				// /quit and /exit handled above (works even during streaming)

				if userMsg == "/help" {
					m.input = ""
				m.cursorPos = 0
					m.welcomeCollapsed = false
					return m, nil
				}

				if userMsg == "/about" {
					m.input = ""
				m.cursorPos = 0
					cwd, _ := os.Getwd()
					var b strings.Builder
					b.WriteString("BujiCoder -- AI Coding Assistant\n\n")
					b.WriteString(fmt.Sprintf("  Version:    %s\n", m.version))
					b.WriteString(fmt.Sprintf("  Commit:     %s\n", m.commit))
					b.WriteString(fmt.Sprintf("  Built:      %s\n", m.buildTime))
					modeLabel := string(m.costMode)
					if m.planMode {
						modeLabel = "plan"
					}
					b.WriteString(fmt.Sprintf("  Cost Mode:  %s\n", modeLabel))
					b.WriteString("  Runtime:    local (standalone)\n")
					cfgPath := cliconfig.UnifiedConfigPath()
					if cfgPath != "" {
						b.WriteString(fmt.Sprintf("  Config:     %s\n", cfgPath))
					}
					if m.unifiedCfg != nil {
						b.WriteString(fmt.Sprintf("  Agents:     %s\n", m.unifiedCfg.GetAgentsDir()))
					}
					b.WriteString(fmt.Sprintf("  Project:    %s\n", cwd))
					m.messages = append(m.messages, ChatMessage{
						Role:    "assistant",
						Content: b.String(),
					})
					return m, nil
				}

				if userMsg == "/memory" || strings.HasPrefix(userMsg, "/memory ") {
					m.input = ""
					m.cursorPos = 0
					cwd, _ := os.Getwd()
					content := tools.ReadProjectMemory(cwd)
					if content == "" {
						m.messages = append(m.messages, ChatMessage{
							Role:    "assistant",
							Content: "No project memory found.\n\nTo create one, I can write to `.bujicoder/BUJI.md` during our conversation, or you can create it manually.\n\nUsage: Just ask me to remember something, and I'll persist it to project memory.",
						})
					} else {
						m.messages = append(m.messages, ChatMessage{
							Role:    "assistant",
							Content: "## Project Memory\n\n" + content,
						})
					}
					return m, nil
				}

				if userMsg == "/verbose" {
					m.input = ""
					m.cursorPos = 0
					if m.verboseEnabled {
						logPath := m.verboseFile.Name()
						m.stopVerboseLog()
						m.messages = append(m.messages, ChatMessage{
							Role:    "assistant",
							Content: fmt.Sprintf("Verbose logging **disabled**.\n\nLog saved to `%s`", logPath),
						})
					} else {
						if err := m.startVerboseLog(); err != nil {
							m.messages = append(m.messages, ChatMessage{
								Role:    "assistant",
								Content: fmt.Sprintf("Failed to start verbose log: %s", err),
							})
						} else {
							m.messages = append(m.messages, ChatMessage{
								Role:    "assistant",
								Content: fmt.Sprintf("Verbose logging **enabled**.\n\nAll agent communications will be logged to:\n`%s`", m.verboseFile.Name()),
							})
						}
					}
					return m, nil
				}

				if strings.HasPrefix(userMsg, "/mcp") {
					m.input = ""
				m.cursorPos = 0
					parts := strings.Fields(userMsg)
					subCmd := ""
					if len(parts) > 1 {
						subCmd = strings.ToLower(parts[1])
					}

					switch subCmd {
					case "add":
						// /mcp add <name> <command> [args...]
						if len(parts) < 4 {
							m.messages = append(m.messages, ChatMessage{
								Role:    "assistant",
								Content: "Usage: `/mcp add <name> <command> [args...]`\n\nExample:\n```\n/mcp add browser npx -y @anthropic-ai/mcp-server-puppeteer\n```",
							})
							return m, nil
						}
						name := parts[2]
						command := parts[3]
						args := parts[4:]

						// Check for duplicate name.
						if m.unifiedCfg != nil {
							for _, s := range m.unifiedCfg.MCPServers {
								if s.Name == name {
									m.messages = append(m.messages, ChatMessage{
										Role:    "assistant",
										Content: fmt.Sprintf("MCP server `%s` already exists. Remove it first with `/mcp remove %s`.", name, name),
									})
									return m, nil
								}
							}
						}

						newServer := cliconfig.MCPServerConfig{
							Name:    name,
							Command: command,
							Args:    args,
							Lazy:    true,
						}

						if m.unifiedCfg == nil {
							m.unifiedCfg = &cliconfig.UnifiedConfig{}
						}
						m.unifiedCfg.MCPServers = append(m.unifiedCfg.MCPServers, newServer)
						_, _ = cliconfig.SaveUnifiedConfig(m.unifiedCfg)

						m.messages = append(m.messages, ChatMessage{
							Role:    "assistant",
							Content: fmt.Sprintf("Added MCP server `%s` (lazy). It will start on first tool call.\n\nRestart BujiCoder or use `/refresh` to activate it in this session.", name),
						})
						return m, nil

					case "remove", "rm":
						// /mcp remove <name>
						if len(parts) < 3 {
							m.messages = append(m.messages, ChatMessage{
								Role:    "assistant",
								Content: "Usage: `/mcp remove <name>`",
							})
							return m, nil
						}
						name := parts[2]
						found := false
						if m.unifiedCfg != nil {
							filtered := m.unifiedCfg.MCPServers[:0]
							for _, s := range m.unifiedCfg.MCPServers {
								if s.Name == name {
									found = true
									continue
								}
								filtered = append(filtered, s)
							}
							if found {
								m.unifiedCfg.MCPServers = filtered
								_, _ = cliconfig.SaveUnifiedConfig(m.unifiedCfg)
							}
						}
						if !found {
							m.messages = append(m.messages, ChatMessage{
								Role:    "assistant",
								Content: fmt.Sprintf("MCP server `%s` not found.", name),
							})
							return m, nil
						}
						m.messages = append(m.messages, ChatMessage{
							Role:    "assistant",
							Content: fmt.Sprintf("Removed MCP server `%s`. Restart BujiCoder to fully unload it.", name),
						})
						return m, nil

					default:
						// /mcp (no subcommand) — show status
						var b strings.Builder
						b.WriteString("**MCP Servers**\n\n")

						if m.unifiedCfg == nil || len(m.unifiedCfg.MCPServers) == 0 {
							b.WriteString("No MCP servers configured.\n\n")
							b.WriteString("Add one with:\n```\n/mcp add <name> <command> [args...]\n```\n")
							b.WriteString("Example:\n```\n/mcp add browser npx -y @anthropic-ai/mcp-server-puppeteer\n```")
							m.messages = append(m.messages, ChatMessage{
								Role:    "assistant",
								Content: b.String(),
							})
							return m, nil
						}

						// Gather live status if manager is running.
						running := make(map[string][]string)
						if m.mcpManager != nil {
							for _, info := range m.mcpManager.Status() {
								if info.Running {
									running[info.Name] = info.Tools
								}
							}
						}

						for _, s := range m.unifiedCfg.MCPServers {
							status := "stopped"
							if tools, ok := running[s.Name]; ok {
								status = fmt.Sprintf("running · %d tools", len(tools))
							} else if s.Lazy {
								status = "lazy (starts on first call)"
							}
							b.WriteString(fmt.Sprintf("  **%s** — `%s %s`\n", s.Name, s.Command, strings.Join(s.Args, " ")))
							b.WriteString(fmt.Sprintf("    Status: %s\n\n", status))

							if tools, ok := running[s.Name]; ok && len(tools) > 0 {
								for _, t := range tools {
									b.WriteString(fmt.Sprintf("    · %s\n", t))
								}
								b.WriteString("\n")
							}
						}

						b.WriteString("Commands: `/mcp add <name> <cmd> [args]` · `/mcp remove <name>`")
						m.messages = append(m.messages, ChatMessage{
							Role:    "assistant",
							Content: b.String(),
						})
						return m, nil
					}
				}

				if strings.HasPrefix(userMsg, "/mode") {
					m.input = ""
				m.cursorPos = 0
					parts := strings.Fields(userMsg)
					if len(parts) < 2 {
						// Show mode picker dropdown
						m.modePickerVisible = true
						// Pre-select current mode
						currentMode := string(m.costMode)
						if m.planMode {
							currentMode = "plan"
						}
						m.modePickerCursor = 0
						for i, opt := range modeOptions {
							if opt.name == currentMode {
								m.modePickerCursor = i
								break
							}
						}
						return m, nil
					}
					modeName := strings.ToLower(parts[1])
					if modeName == "plan" {
						m.planMode = true
						m.costMode = costmode.ModeNormal
						if m.unifiedCfg != nil {
							m.unifiedCfg.CostMode = "plan"
							_, _ = cliconfig.SaveUnifiedConfig(m.unifiedCfg)
						}
						m.messages = append(m.messages, ChatMessage{
							Role:    "assistant",
							Content: "Switched to plan mode\n\nIn plan mode, BujiCoder will only read code for understanding and create/modify .md documentation files. No source code changes will be made.",
						})
						return m, nil
					}
					newMode := costmode.ParseMode(modeName)
					m.costMode = newMode
					m.planMode = false
					if m.unifiedCfg != nil {
						m.unifiedCfg.CostMode = string(newMode)
						_, _ = cliconfig.SaveUnifiedConfig(m.unifiedCfg)
					}
					m.messages = append(m.messages, ChatMessage{
						Role:    "assistant",
						Content: fmt.Sprintf("Switched to %s mode", newMode),
					})
					return m, nil
				}

				m.messages = append(m.messages, ChatMessage{Role: "user", Content: userMsg})
				m.promptHistory = append(m.promptHistory, userMsg)
				m.historyIdx = -1
				m.verboseLog("USER ▸ %s", userMsg)
				m.input = ""
				m.cursorPos = 0
				m.streaming = true
				m.streamBuf = ""
				m.subAgentStreams = make(map[string]string)
				m.activities = nil
				m.currentStep = 0
				m.totalSteps = 0
				m.inputTokens = 0
				m.outputTokens = 0
				m.costCents = 0
				m.startTime = time.Now()
				m.spinnerFrame = 0
				m.lastActivity = "Thinking"

				m.streamCh = make(chan tea.Msg, 64)

				if !m.runtimeReady {
					m.streaming = false
					msg := "Initializing runtime, please wait..."
					if m.llmRegistry == nil || !m.llmRegistry.HasProviders() {
						msg = "No LLM providers configured. Add API keys to ~/.bujicoder/bujicoder.yaml or set environment variables, then run /refresh."
					}
					m.messages = append(m.messages, ChatMessage{
						Role:    "assistant",
						Content: msg,
					})
					return m, nil
				}

				// Coordinator pattern: /goal <description> decomposes into task DAG.
				var sendCmd tea.Cmd
				var evLog func(agentruntime.Event)
				if m.verboseEnabled {
					evLog = m.verboseLogEvent
				}
				if strings.HasPrefix(userMsg, "/goal ") {
					goalText := strings.TrimPrefix(userMsg, "/goal ")
					sendCmd = sendCoordinatedGoal(
						m.agentRuntime, m.agentRegistry, m.modelResolver,
						goalText, m.streamCh, m.costMode, evLog,
					)
				} else {
					sendCmd = sendMessageLocal(
						m.agentRuntime, m.agentRegistry, m.modelResolver,
						m.messages, m.streamCh, m.costMode, m.planMode, m.conversationID, evLog,
					)
				}

				return m, tea.Batch(
					sendCmd,
					waitForChunks(m.streamCh),
					tickCmd(),
				)
			}

		case "tab":
			// Accept autocomplete selection.
			if m.state == StateChat && !m.streaming && m.acVisible && len(m.acMatches) > 0 {
				selected := slashCommands[m.acMatches[m.acCursor]]
				m.acVisible = false
				m.acMatches = nil
				m.acCursor = 0
				if selected.cmd == "/mode" {
					m.input = ""
					m.cursorPos = 0
					m.modePickerVisible = true
					currentMode := string(m.costMode)
					if m.planMode {
						currentMode = "plan"
					}
					m.modePickerCursor = 0
					for i, opt := range modeOptions {
						if opt.name == currentMode {
							m.modePickerCursor = i
							break
						}
					}
				} else {
					m.input = selected.cmd
					m.cursorPos = len([]rune(m.input))
				}
				return m, nil
			}

		case "right":
			// Accept autocomplete selection if visible.
			if m.state == StateChat && !m.streaming && m.acVisible && len(m.acMatches) > 0 {
				selected := slashCommands[m.acMatches[m.acCursor]]
				m.acVisible = false
				m.acMatches = nil
				m.acCursor = 0
				if selected.cmd == "/mode" {
					m.input = ""
					m.cursorPos = 0
					m.modePickerVisible = true
					currentMode := string(m.costMode)
					if m.planMode {
						currentMode = "plan"
					}
					m.modePickerCursor = 0
					for i, opt := range modeOptions {
						if opt.name == currentMode {
							m.modePickerCursor = i
							break
						}
					}
				} else {
					m.input = selected.cmd
					m.cursorPos = len([]rune(m.input))
				}
				return m, nil
			}
			// Move cursor right within input.
			if m.state == StateChat && m.cursorPos < len([]rune(m.input)) {
				m.cursorPos++
			}

		case "left":
			// Move cursor left within input.
			if m.state == StateChat && m.cursorPos > 0 {
				m.cursorPos--
			}

		case "home":
			if m.state == StateHistory && len(m.historyItems) > 0 {
				m.historyCursor = 0
				return m, nil
			}
			// Move cursor to beginning of input.
			if m.state == StateChat {
				m.cursorPos = 0
			}

		case "end":
			if m.state == StateHistory && len(m.historyItems) > 0 {
				m.historyCursor = len(m.historyItems) - 1
				return m, nil
			}
			// Move cursor to end of input.
			if m.state == StateChat {
				m.cursorPos = len([]rune(m.input))
			}

		case "esc":
			// Dismiss mode picker first.
			if m.state == StateChat && m.modePickerVisible {
				m.modePickerVisible = false
				return m, nil
			}
			// Dismiss autocomplete.
			if m.state == StateChat && m.acVisible {
				m.acVisible = false
				m.acMatches = nil
				m.acCursor = 0
				return m, nil
			}
			if m.state == StateHistory {
				m.state = StateChat
				m.historyItems = nil
				m.historyCursor = 0
				m.historyOffset = 0
				return m, nil
			}

		case "up", "down", "pgup", "pgdown":
			// Navigate mode picker.
			if m.state == StateChat && m.modePickerVisible {
				switch msg.String() {
				case "up":
					if m.modePickerCursor > 0 {
						m.modePickerCursor--
					} else {
						m.modePickerCursor = len(modeOptions) - 1
					}
				case "down":
					if m.modePickerCursor < len(modeOptions)-1 {
						m.modePickerCursor++
					} else {
						m.modePickerCursor = 0
					}
				}
				return m, nil
			}
			// Navigate autocomplete dropdown.
			if m.state == StateChat && !m.streaming && m.acVisible && len(m.acMatches) > 0 {
				switch msg.String() {
				case "up":
					if m.acCursor > 0 {
						m.acCursor--
					} else {
						m.acCursor = len(m.acMatches) - 1
					}
				case "down":
					if m.acCursor < len(m.acMatches)-1 {
						m.acCursor++
					} else {
						m.acCursor = 0
					}
				}
				return m, nil
			}
			if m.state == StateHistory && len(m.historyItems) > 0 {
				visibleRows := m.height - 8
				if visibleRows < 1 {
					visibleRows = 1
				}
				switch msg.String() {
				case "up":
					if m.historyCursor > 0 {
						m.historyCursor--
					}
				case "down":
					if m.historyCursor < len(m.historyItems)-1 {
						m.historyCursor++
					}
				case "pgup":
					m.historyCursor -= visibleRows
					if m.historyCursor < 0 {
						m.historyCursor = 0
					}
				case "pgdown":
					m.historyCursor += visibleRows
					if m.historyCursor >= len(m.historyItems) {
						m.historyCursor = len(m.historyItems) - 1
					}
				}
				if m.historyCursor < m.historyOffset {
					m.historyOffset = m.historyCursor
				}
				if m.historyCursor >= m.historyOffset+visibleRows {
					m.historyOffset = m.historyCursor - visibleRows + 1
				}
				return m, nil
			}
			// In chat state, Up/Down/PgUp/PgDown scroll the viewport.
			if m.state == StateChat && m.ready {
				var cmd tea.Cmd
				m.viewport, cmd = m.viewport.Update(msg)
				return m, cmd
			}

		case "ctrl+up", "ctrl+down":
			// Prompt history navigation (Ctrl+Up/Down in input area).
			if m.state == StateChat && !m.streaming && len(m.promptHistory) > 0 {
				switch msg.String() {
				case "ctrl+up":
					if m.historyIdx == -1 {
						m.historySaved = m.input
						m.historyIdx = len(m.promptHistory) - 1
					} else if m.historyIdx > 0 {
						m.historyIdx--
					}
					m.input = m.promptHistory[m.historyIdx]
					m.cursorPos = len([]rune(m.input))
					return m, nil
				case "ctrl+down":
					if m.historyIdx >= 0 {
						if m.historyIdx < len(m.promptHistory)-1 {
							m.historyIdx++
							m.input = m.promptHistory[m.historyIdx]
						} else {
							m.historyIdx = -1
							m.input = m.historySaved
						}
						m.cursorPos = len([]rune(m.input))
						return m, nil
					}
				}
			}

		default:
			if m.state == StateChat && (!m.streaming || m.pendingQuestion != "" || m.pendingApproval != "") {
				key := tea.Key(msg)
				isPaste := key.Paste
				// Only accept printable rune input (KeyRunes) and paste events.
				// All special keys (function keys, arrows, etc.) are handled
				// by explicit cases above — ignore anything else here.
				if key.Type != tea.KeyRunes && key.Type != tea.KeySpace && !isPaste {
					break
				}
				ch := msg.String()
				// Filter out terminal OSC escape sequence responses (e.g. background
				// color query replies like "\e]11;rgb:1717/1414/2121\e\\") that leak
				// into the input buffer on Windows.
				if strings.Contains(ch, ";rgb:") || strings.Contains(ch, "]11;") || strings.Contains(ch, "]10;") {
					break
				}
				if isPaste {
					// Strip carriage returns from pasted text and
					// replace newlines with spaces for single-line input.
					ch = strings.ReplaceAll(ch, "\r\n", " ")
					ch = strings.ReplaceAll(ch, "\r", " ")
					ch = strings.ReplaceAll(ch, "\n", " ")
					ch = strings.TrimSpace(ch)
					if ch == "" {
						break
					}
				}
				// Toggle welcome collapse when typing "/"
				if len(m.input) == 0 && ch == "/" && len(m.messages) == 0 {
					m.welcomeCollapsed = !m.welcomeCollapsed
				}
				runes := []rune(m.input)
				pastedRunes := []rune(ch)
				m.input = string(runes[:m.cursorPos]) + ch + string(runes[m.cursorPos:])
				m.cursorPos += len(pastedRunes)
				m.historyIdx = -1 // reset history browsing on new input
				m.spinnerFrame = 0
				m.updateAutocomplete()
			}
		}

	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.viewportDirty = true

		fh := m.calcFooterHeight()
		vpHeight := msg.Height - fh
		if vpHeight < 1 {
			vpHeight = 1
		}

		if !m.ready {
			m.viewport = viewport.New(msg.Width, vpHeight)
			m.viewport.MouseWheelEnabled = true
			m.viewport.SetContent(m.buildScrollableContent())
			m.ready = true
		} else {
			m.viewport.Width = msg.Width
			m.viewport.Height = vpHeight
		}

		if r, err := newMarkdownRenderer(msg.Width); err == nil {
			m.mdRenderer = r
			for i := range m.messages {
				if m.messages[i].Role == "assistant" && m.messages[i].Content != "" {
					m.messages[i].RenderedContent = renderMarkdown(r, m.messages[i].Content)
				}
			}
		}

	case tickMsg:
		m.spinnerFrame = (m.spinnerFrame + 1) % len(spinnerFrames)
		if m.streaming {
			// Flush buffered content changes on tick (100ms debounce).
			m.viewportDirty = true
		}
		if m.streaming || m.state == StateChat || (m.state == StateSetup && m.setupFetching) {
			return m, tickCmd()
		}
		return m, nil

	case modelsFetchedMsg:
		return m.handleModelsFetched(msg)

	case streamChunkMsg:
		if msg.agentID == "" || msg.agentID == "base" {
			m.streamBuf += msg.text
		} else {
			if m.subAgentStreams == nil {
				m.subAgentStreams = make(map[string]string)
			}
			m.subAgentStreams[msg.agentID] += msg.text
		}
		if name := shortAgentName(msg.agentID); name != "" {
			m.lastActivity = fmt.Sprintf("%s: Generating", name)
		} else {
			m.lastActivity = "Generating"
		}
		return m, waitForChunks(m.streamCh)

	case toolCallMsg:
		m.viewportDirty = true
		parsedArgs := parseToolArgs(msg.toolName, msg.argsJSON)
		m.activities = append(m.activities, activityEntry{
			Kind:       actToolCall,
			AgentID:    msg.agentID,
			ToolName:   msg.toolName,
			ToolCallID: msg.toolCallID,
			Args:       parsedArgs,
			Timestamp:  time.Now(),
		})
		display := toolDisplayName(msg.toolName)
		prefix := shortAgentName(msg.agentID)
		if parsedArgs != "" {
			if prefix != "" {
				m.lastActivity = fmt.Sprintf("%s: %s %s", prefix, display, parsedArgs)
			} else {
				m.lastActivity = fmt.Sprintf("%s %s", display, parsedArgs)
			}
		} else {
			if prefix != "" {
				m.lastActivity = fmt.Sprintf("%s: %s", prefix, display)
			} else {
				m.lastActivity = display
			}
		}
		if msg.agentID != "" && msg.agentID != "base" {
			if m.liveAgents == nil {
				m.liveAgents = make(map[string]*agentLiveState)
			}
			st, exists := m.liveAgents[msg.agentID]
			if !exists {
				m.spawnCounter++
				st = &agentLiveState{spawnOrder: m.spawnCounter}
				m.liveAgents[msg.agentID] = st
			}
			st.currentTool = display
			st.currentArgs = parsedArgs
		}
		return m, waitForChunks(m.streamCh)

	case toolResultMsg:
		m.viewportDirty = true
		resultSummary := truncateResult(msg.text, msg.isError)
		m.activities = append(m.activities, activityEntry{
			Kind:       actToolResult,
			AgentID:    msg.agentID,
			ToolName:   msg.toolName,
			ToolCallID: msg.toolCallID,
			Result:     resultSummary,
			IsError:    msg.isError,
			Timestamp:  time.Now(),
		})
		if name := shortAgentName(msg.agentID); name != "" {
			m.lastActivity = fmt.Sprintf("%s: Thinking", name)
		} else {
			m.lastActivity = "Thinking"
		}
		if msg.agentID != "" && msg.agentID != "base" {
			if m.liveAgents != nil {
				if st, ok := m.liveAgents[msg.agentID]; ok {
					st.currentTool = ""
					st.currentArgs = ""
				}
			}
		}
		return m, waitForChunks(m.streamCh)

	case stepStartMsg:
		m.viewportDirty = true
		stepNum := msg.step + 1
		if msg.agentID == "" || msg.agentID == "base" {
			m.currentStep = stepNum
			if m.currentStep > m.totalSteps {
				m.totalSteps = m.currentStep
			}
		}
		m.activities = append(m.activities, activityEntry{
			AgentID:   msg.agentID,
			Kind:      actStepStart,
			Step:      stepNum,
			Timestamp: time.Now(),
		})
		if name := shortAgentName(msg.agentID); name != "" {
			m.lastActivity = fmt.Sprintf("%s: Step %d", name, stepNum)
		} else {
			m.lastActivity = "Thinking"
		}
		if msg.agentID != "" && msg.agentID != "base" {
			if m.liveAgents == nil {
				m.liveAgents = make(map[string]*agentLiveState)
			}
			st, exists := m.liveAgents[msg.agentID]
			if !exists {
				m.spawnCounter++
				st = &agentLiveState{spawnOrder: m.spawnCounter}
				m.liveAgents[msg.agentID] = st
			}
			st.step = stepNum
			st.currentTool = ""
			st.currentArgs = ""
		}
		return m, waitForChunks(m.streamCh)

	case stepEndMsg:
		return m, waitForChunks(m.streamCh)

	case statusMsg:
		m.viewportDirty = true
		m.activities = append(m.activities, activityEntry{
			AgentID:   msg.agentID,
			Kind:      actStatus,
			ToolName:  msg.agentID,
			Result:    msg.text,
			Timestamp: time.Now(),
		})
		if name := shortAgentName(msg.agentID); name != "" {
			m.lastActivity = fmt.Sprintf("%s: %s", name, msg.text)
		} else {
			m.lastActivity = msg.text
		}
		if msg.agentID != "" && msg.agentID != "base" {
			if m.liveAgents == nil {
				m.liveAgents = make(map[string]*agentLiveState)
			}
			st, exists := m.liveAgents[msg.agentID]
			if !exists {
				m.spawnCounter++
				st = &agentLiveState{spawnOrder: m.spawnCounter}
				m.liveAgents[msg.agentID] = st
			}
			text := msg.text
			if strings.HasPrefix(text, "Completed ") {
				st.done = true
				st.lastStatus = "Done"
				st.currentTool = ""
				st.currentArgs = ""
			} else {
				st.lastStatus = text
			}
		}
		return m, waitForChunks(m.streamCh)

	case completeMsg:
		m.inputTokens = msg.inputTokens
		m.outputTokens = msg.outputTokens
		m.costCents = msg.costCents
		return m, waitForChunks(m.streamCh)

	case streamDoneMsg:
		m.viewportDirty = true
		m.streaming = false
		elapsed := time.Since(m.startTime)
		if msg.err != nil {
			m.err = msg.err
			m.verboseLog("SESSION ERROR ▸ %v", msg.err)
		}
		m.verboseLog("──── SESSION COMPLETE ▸ steps=%d elapsed=%s input_tokens=%d output_tokens=%d cost=%.4f¢ ────",
			m.totalSteps, elapsed.Round(time.Millisecond), m.inputTokens, m.outputTokens, float64(m.costCents))
		if m.streamBuf != "" || len(m.activities) > 0 {
			content := m.streamBuf
			if content == "" && msg.err != nil {
				content = fmt.Sprintf("Error: %v", msg.err)
			}
			m.messages = append(m.messages, ChatMessage{
				Role:            "assistant",
				Content:         content,
				RenderedContent: renderMarkdown(m.mdRenderer, content),
				Activities:      append([]activityEntry(nil), m.activities...),
				Elapsed:         elapsed,
				Steps:           m.totalSteps,
				InputTokens:     m.inputTokens,
				OutputTokens:    m.outputTokens,
				CostCents:       m.costCents,
			})
			m.streamBuf = ""
			if content != "" {
				response := content
				if len(response) > 3000 {
					response = response[:3000] + "... (truncated in log)"
				}
				m.verboseLog("ASSISTANT RESPONSE ▸\n%s", response)
			}

			// Persist to local store.
			if m.localStore != nil {
				var userContent, assistantContent string
				assistantContent = content
				for i := len(m.messages) - 2; i >= 0; i-- {
					if m.messages[i].Role == "user" {
						userContent = m.messages[i].Content
						break
					}
				}
				title := userContent
				if len(title) > 100 {
					title = title[:100]
				}
				go func() {
					var msgs []store.StoredMessage
					if userContent != "" {
						msgs = append(msgs, store.StoredMessage{
							Role: "user", Content: userContent, CreatedAt: time.Now().UTC(),
						})
					}
					msgs = append(msgs, store.StoredMessage{
						Role: "assistant", Content: assistantContent, CreatedAt: time.Now().UTC(),
					})
					_ = m.localStore.AppendMessages(m.conversationID, title, msgs...)
				}()
			}
		}
		m.activities = nil
		m.liveAgents = nil
		m.spawnCounter = 0
		m.inputTokens = 0
		m.outputTokens = 0
		m.costCents = 0
		return m, nil

	case historyResultMsg:
		if msg.err != nil {
			m.messages = append(m.messages, ChatMessage{
				Role:    "assistant",
				Content: fmt.Sprintf("Failed to load history: %v", msg.err),
			})
			return m, nil
		}
		if len(msg.conversations) == 0 {
			m.messages = append(m.messages, ChatMessage{
				Role:    "assistant",
				Content: "No conversations found.",
			})
			return m, nil
		}
		m.historyItems = msg.conversations
		m.historyCursor = 0
		m.historyOffset = 0
		m.state = StateHistory
		return m, nil

	case searchResultMsg:
		if msg.err != nil {
			m.messages = append(m.messages, ChatMessage{
				Role:    "assistant",
				Content: fmt.Sprintf("Search failed: %v", msg.err),
			})
			return m, nil
		}
		if len(msg.results) == 0 {
			m.messages = append(m.messages, ChatMessage{
				Role:    "assistant",
				Content: "No results found.",
			})
			return m, nil
		}
		var sb strings.Builder
		sb.WriteString(fmt.Sprintf("Found %d results:\n\n", len(msg.results)))
		for i, r := range msg.results {
			snippet := r.Snippet
			if len(snippet) > 120 {
				snippet = snippet[:120] + "..."
			}
			sb.WriteString(fmt.Sprintf("%d. **%s** (`%s`)\n   %s\n\n",
				i+1, r.ConversationTitle, r.ConversationID[:8], snippet))
		}
		sb.WriteString("Use `/resume <id>` to open a conversation.")
		m.messages = append(m.messages, ChatMessage{
			Role:    "assistant",
			Content: sb.String(),
		})
		return m, nil

	case clipboardResultMsg:
		if msg.err != nil {
			m.messages = append(m.messages, ChatMessage{
				Role:    "assistant",
				Content: fmt.Sprintf("Failed to copy: %v", msg.err),
			})
		} else {
			m.messages = append(m.messages, ChatMessage{
				Role:    "assistant",
				Content: "Copied last response to clipboard.",
			})
		}
		return m, nil

	case updateCheckResultMsg:
		if msg.version != "" {
			m.updateVersion = msg.version
		}
		return m, nil

	case runtimeInitMsg:
		if msg.err != nil {
			m.messages = append(m.messages, ChatMessage{
				Role:    "assistant",
				Content: fmt.Sprintf("Failed to initialize runtime: %v", msg.err),
			})
			return m, nil
		}
		m.agentRegistry = msg.agentRegistry
		m.modelResolver = msg.modelResolver

		// Create ask_user channels.
		m.askQuestionCh = make(chan string, 1)
		m.askAnswerCh = make(chan string, 1)

		// Create approval channels.
		m.approvalCmdCh = make(chan string, 1)
		m.approvalRespCh = make(chan bool, 1)

		// Create tool registry with ask_user and approval wired to channels.
		cwd, _ := os.Getwd()

		// Load per-project permissions from .bujicoder/permissions.yaml.
		perms := tools.LoadProjectPermissions(cwd)
		m.permissions = perms
		if perms != nil {
			m.messages = append(m.messages, ChatMessage{
				Role:    "assistant",
				Content: fmt.Sprintf("Loaded project permissions from %s (mode: %s, %d command rules)", perms.SourceFile(), perms.Mode, perms.CommandRuleCount()),
			})
		}

		m.toolRegistry = tools.NewRegistry(cwd, tools.RegistryOpts{
			UserPrompt: func(question string) (string, error) {
				m.askQuestionCh <- question
				answer := <-m.askAnswerCh
				return answer, nil
			},
			Approval: func(command, reason string) (bool, error) {
				m.approvalCmdCh <- command + "\n" + reason
				approved := <-m.approvalRespCh
				return approved, nil
			},
			Permissions: perms,
		})

		// Register MCP server tools if configured.
		if m.unifiedCfg != nil && len(m.unifiedCfg.MCPServers) > 0 {
			var mcpConfigs []mcp.ServerConfig
			for _, s := range m.unifiedCfg.MCPServers {
				mcpConfigs = append(mcpConfigs, mcp.ServerConfig{
					Name:    s.Name,
					Command: s.Command,
					Args:    s.Args,
					Lazy:    s.Lazy,
				})
			}
			m.mcpManager = mcp.NewManager(mcpConfigs)
			if err := m.mcpManager.RegisterTools(m.toolRegistry); err != nil {
				m.messages = append(m.messages, ChatMessage{
					Role:    "assistant",
					Content: fmt.Sprintf("MCP tool registration failed: %v\nContinuing without MCP tools.", err),
				})
			}
		}

		// Create LLM registry and register providers from config + env vars.
		m.llmRegistry = llm.NewRegistry()
		registerLocalProviders(m.llmRegistry, m.unifiedCfg)
		if !m.llmRegistry.HasProviders() {
			m.messages = append(m.messages, ChatMessage{
				Role: "assistant",
				Content: "No LLM providers configured. Add API keys to your config file or set environment variables " +
					"(e.g. OPENROUTER_API_KEY, ANTHROPIC_API_KEY). Run /refresh to reload config.",
			})
			return m, nil
		}

		// Create agent runtime with persistent logger.
		m.log = logging.New(logging.Config{})
		m.log.Info().
			Str("cost_mode", string(m.costMode)).
			Bool("plan_mode", m.planMode).
			Msg("session started")
		m.agentRuntime = agentruntime.New(m.llmRegistry, m.toolRegistry, m.agentRegistry, m.log)
		m.runtimeReady = true

		cmds := []tea.Cmd{listenForAskUser(m.askQuestionCh), listenForApproval(m.approvalCmdCh), checkForUpdateCmd()}
		return m, tea.Batch(cmds...)

	case askUserQuestionMsg:
		m.pendingQuestion = msg.question
		return m, nil

	case approvalRequestMsg:
		m.pendingApprovalCmd = msg.command
		m.pendingApproval = msg.reason
		return m, nil

	case resumeResultMsg:
		if msg.err != nil {
			m.messages = append(m.messages, ChatMessage{
				Role:    "assistant",
				Content: fmt.Sprintf("Failed to resume: %v", msg.err),
			})
			return m, nil
		}
		m.conversationID = msg.conversationID
		m.messages = []ChatMessage{}
		for _, cm := range msg.messages {
			entry := ChatMessage{Role: cm.Role, Content: cm.Content}
			if cm.Role == "assistant" {
				entry.RenderedContent = renderMarkdown(m.mdRenderer, cm.Content)
			}
			m.messages = append(m.messages, entry)
		}
		m.messages = append(m.messages, ChatMessage{
			Role:    "assistant",
			Content: fmt.Sprintf("Resumed conversation %s (%d messages loaded).", msg.conversationID[:8], len(msg.messages)),
		})
		return m, nil

	default:
		if m.state == StateChat && m.ready {
			var cmd tea.Cmd
			m.viewport, cmd = m.viewport.Update(msg)
			return m, cmd
		}
	}

	return m, nil
}

func fetchLocalHistory(store *store.Store) tea.Cmd {
	return func() tea.Msg {
		summaries, err := store.ListConversations(20, 0)
		if err != nil {
			return historyResultMsg{err: err}
		}
		return historyResultMsg{conversations: summaries}
	}
}

func resumeLocalConversation(store *store.Store, conversationID string) tea.Cmd {
	return func() tea.Msg {
		msgs, err := store.GetMessages(conversationID)
		if err != nil {
			return resumeResultMsg{err: fmt.Errorf("load conversation: %w", err)}
		}
		var convMsgs []ConversationMessage
		for i, m := range msgs {
			convMsgs = append(convMsgs, ConversationMessage{
				ID:          fmt.Sprintf("%d", i),
				Role:        m.Role,
				Content:     m.Content,
				SequenceNum: i,
				CreatedAt:   m.CreatedAt.Format(time.RFC3339),
			})
		}
		return resumeResultMsg{conversationID: conversationID, messages: convMsgs}
	}
}

// openLocalStore opens the bbolt+Bleve store, auto-migrating from JSON if needed.
func openLocalStore(log zerolog.Logger) *store.Store {
	home, _ := os.UserHomeDir()
	baseDir := filepath.Join(home, ".bujicoder")
	dbPath := filepath.Join(baseDir, "bujicoder.db")
	indexPath := filepath.Join(baseDir, "search.bleve")
	jsonDir := filepath.Join(baseDir, "conversations")

	s, err := store.Open(dbPath, indexPath)
	if err != nil {
		log.Error().Err(err).Msg("failed to open store, falling back to nil")
		return nil
	}

	// Auto-migrate from old JSON files if they exist.
	if store.NeedsMigration(jsonDir, dbPath) {
		// DB was just created by Open(), so migration check is on the JSON dir only.
	}
	// Always try migration — it's a no-op if jsonDir doesn't exist.
	if err := store.MigrateFromJSON(jsonDir, s); err != nil {
		log.Warn().Err(err).Msg("JSON migration failed (non-fatal)")
	}

	return s
}

func searchConversations(st *store.Store, query string) tea.Cmd {
	return func() tea.Msg {
		results, err := st.SearchMessages(query, 20)
		if err != nil {
			return searchResultMsg{err: err}
		}
		return searchResultMsg{results: results}
	}
}

// gatherCodebaseInfo scans the current working directory and returns a summary
// of the codebase: project type, git info, file counts, and knowledge files.
func gatherCodebaseInfo() string {
	cwd, err := os.Getwd()
	if err != nil {
		return fmt.Sprintf("Error: %v", err)
	}

	var b strings.Builder
	b.WriteString(fmt.Sprintf("Codebase: %s\n", filepath.Base(cwd)))
	b.WriteString(fmt.Sprintf("Path:     %s\n\n", cwd))

	// Git info
	if _, err := os.Stat(filepath.Join(cwd, ".git")); err == nil {
		b.WriteString("Git:\n")
		if branch := runQuietCmd(cwd, "git", "branch", "--show-current"); branch != "" {
			b.WriteString(fmt.Sprintf("  Branch:   %s\n", branch))
		}
		if remote := runQuietCmd(cwd, "git", "remote", "get-url", "origin"); remote != "" {
			b.WriteString(fmt.Sprintf("  Remote:   %s\n", remote))
		}
		if status := runQuietCmd(cwd, "git", "status", "--porcelain"); status != "" {
			changed := len(strings.Split(strings.TrimSpace(status), "\n"))
			b.WriteString(fmt.Sprintf("  Changed:  %d files\n", changed))
		} else {
			b.WriteString("  Changed:  clean\n")
		}
		b.WriteString("\n")
	}

	// Project type detection
	projectFiles := []struct {
		file  string
		label string
	}{
		{"go.mod", "Go"},
		{"package.json", "Node.js"},
		{"Cargo.toml", "Rust"},
		{"pyproject.toml", "Python"},
		{"requirements.txt", "Python"},
		{"pom.xml", "Java (Maven)"},
		{"build.gradle", "Java (Gradle)"},
		{"Gemfile", "Ruby"},
		{"composer.json", "PHP"},
		{"mix.exs", "Elixir"},
		{"CMakeLists.txt", "C/C++ (CMake)"},
		{"Makefile", "Make"},
		{"Dockerfile", "Docker"},
		{"docker-compose.yml", "Docker Compose"},
	}
	var detected []string
	for _, pf := range projectFiles {
		if _, err := os.Stat(filepath.Join(cwd, pf.file)); err == nil {
			detected = append(detected, fmt.Sprintf("%s (%s)", pf.label, pf.file))
		}
	}
	if len(detected) > 0 {
		b.WriteString("Project type:\n")
		for _, d := range detected {
			b.WriteString(fmt.Sprintf("  %s\n", d))
		}
		b.WriteString("\n")
	}

	// File counts by extension
	extCounts := map[string]int{}
	totalFiles := 0
	skipDirs := map[string]bool{
		".git": true, "node_modules": true, "vendor": true, "__pycache__": true,
		".next": true, "dist": true, "build": true, "target": true, ".venv": true,
	}
	_ = filepath.WalkDir(cwd, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() && skipDirs[d.Name()] {
			return filepath.SkipDir
		}
		if !d.IsDir() {
			totalFiles++
			ext := strings.ToLower(filepath.Ext(d.Name()))
			if ext != "" {
				extCounts[ext]++
			}
		}
		if totalFiles > 10000 {
			return filepath.SkipAll
		}
		return nil
	})

	b.WriteString(fmt.Sprintf("Files:    %d", totalFiles))
	if totalFiles > 10000 {
		b.WriteString("+")
	}
	b.WriteString("\n")

	// Top extensions
	if len(extCounts) > 0 {
		type extEntry struct {
			ext   string
			count int
		}
		var sorted []extEntry
		for ext, count := range extCounts {
			sorted = append(sorted, extEntry{ext, count})
		}
		for i := 0; i < len(sorted); i++ {
			for j := i + 1; j < len(sorted); j++ {
				if sorted[j].count > sorted[i].count {
					sorted[i], sorted[j] = sorted[j], sorted[i]
				}
			}
		}
		limit := 8
		if len(sorted) < limit {
			limit = len(sorted)
		}
		var extParts []string
		for _, e := range sorted[:limit] {
			extParts = append(extParts, fmt.Sprintf("%s(%d)", e.ext, e.count))
		}
		b.WriteString(fmt.Sprintf("Top:      %s\n", strings.Join(extParts, "  ")))
	}

	// AI assistant directories
	aiDirs := []struct {
		dir   string
		label string
	}{
		{".agents", "Custom Agents"},
		{".claude", "Claude AI Config"},
		{".kiro", "Kiro AI Config"},
		{".cursor", "Cursor AI Config"},
		{".github", "GitHub Config"},
	}
	var foundAIDirs []string
	for _, ad := range aiDirs {
		dirPath := filepath.Join(cwd, ad.dir)
		if info, statErr := os.Stat(dirPath); statErr == nil && info.IsDir() {
			entries, _ := os.ReadDir(dirPath)
			count := len(entries)
			foundAIDirs = append(foundAIDirs, fmt.Sprintf("  %-14s %s (%d entries)", ad.dir+"/", ad.label, count))
		}
	}
	if len(foundAIDirs) > 0 {
		b.WriteString("\nAI/Config dirs:\n")
		for _, d := range foundAIDirs {
			b.WriteString(d + "\n")
		}
	}

	// Discover all documentation files
	type docFile struct {
		relPath string
		absPath string
	}
	var docs []docFile
	seen := map[string]bool{}

	addDoc := func(rel string) {
		if seen[rel] {
			return
		}
		abs := filepath.Join(cwd, rel)
		if info, statErr := os.Stat(abs); statErr == nil && !info.IsDir() {
			docs = append(docs, docFile{relPath: rel, absPath: abs})
			seen[rel] = true
		}
	}

	knowledgeNames := []string{"knowledge.md", "CLAUDE.md", "README.md", "CONTRIBUTING.md", "ARCHITECTURE.md"}
	for _, name := range knowledgeNames {
		addDoc(name)
	}
	kMatches, _ := filepath.Glob(filepath.Join(cwd, "*.knowledge.md"))
	for _, match := range kMatches {
		base := filepath.Base(match)
		if base != "knowledge.md" {
			addDoc(base)
		}
	}
	kiroRules, _ := filepath.Glob(filepath.Join(cwd, ".kiro", "*.md"))
	for _, match := range kiroRules {
		addDoc(filepath.Join(".kiro", filepath.Base(match)))
	}
	agentFiles, _ := filepath.Glob(filepath.Join(cwd, ".agents", "*.yaml"))
	for _, match := range agentFiles {
		addDoc(filepath.Join(".agents", filepath.Base(match)))
	}
	agentMDs, _ := filepath.Glob(filepath.Join(cwd, ".agents", "*.md"))
	for _, match := range agentMDs {
		addDoc(filepath.Join(".agents", filepath.Base(match)))
	}

	// Read and analyze each documentation file
	if len(docs) > 0 {
		b.WriteString("\n--- Documentation Analysis ---\n\n")
		var projectName, projectDesc string
		var mentionedTech []string

		for _, doc := range docs {
			excerpt := readFileExcerpt(doc.absPath, 8000)
			if excerpt.heading == "" && excerpt.body == "" {
				b.WriteString(fmt.Sprintf("  %s  (empty or binary)\n\n", doc.relPath))
				continue
			}

			b.WriteString(fmt.Sprintf("  %s\n", doc.relPath))
			if excerpt.heading != "" {
				b.WriteString(fmt.Sprintf("   # %s\n", excerpt.heading))
			}
			if excerpt.body != "" {
				for _, line := range strings.Split(excerpt.body, "\n") {
					b.WriteString(fmt.Sprintf("   %s\n", line))
				}
			}
			if len(excerpt.sections) > 0 {
				b.WriteString(fmt.Sprintf("   Sections: %s\n", strings.Join(excerpt.sections, ", ")))
			}
			b.WriteString("\n")

			base := filepath.Base(doc.relPath)
			if projectName == "" && (base == "README.md" || base == "knowledge.md") {
				if excerpt.heading != "" {
					projectName = excerpt.heading
				}
				if excerpt.body != "" {
					projectDesc = excerpt.body
				}
			}
			mentionedTech = append(mentionedTech, excerpt.technologies...)
		}

		b.WriteString("--- What BujiCoder Understands ---\n\n")

		if projectName != "" {
			b.WriteString(fmt.Sprintf("Project: %s\n", projectName))
		} else {
			b.WriteString(fmt.Sprintf("Project: %s\n", filepath.Base(cwd)))
		}

		if projectDesc != "" {
			b.WriteString(fmt.Sprintf("\n%s\n", projectDesc))
		}

		if len(detected) > 0 {
			var techLabels []string
			for _, d := range detected {
				if idx := strings.Index(d, " ("); idx > 0 {
					techLabels = append(techLabels, d[:idx])
				}
			}
			b.WriteString(fmt.Sprintf("\nDetected stack: %s\n", strings.Join(techLabels, ", ")))
		}

		if len(mentionedTech) > 0 {
			uniqueTech := map[string]bool{}
			var deduped []string
			for _, t := range mentionedTech {
				lower := strings.ToLower(t)
				if !uniqueTech[lower] {
					uniqueTech[lower] = true
					deduped = append(deduped, t)
				}
			}
			if len(deduped) > 12 {
				deduped = deduped[:12]
			}
			b.WriteString(fmt.Sprintf("Mentioned tech: %s\n", strings.Join(deduped, ", ")))
		}

		b.WriteString(fmt.Sprintf("\nBujiCoder read %d documentation file(s) to understand this project.\n", len(docs)))
		b.WriteString("Use the chat to ask questions -- BujiCoder will use this context.\n")
	}

	return b.String()
}

// fileExcerpt holds parsed information extracted from a documentation file.
type fileExcerpt struct {
	heading      string
	body         string
	sections     []string
	technologies []string
}

// readFileExcerpt reads a markdown/yaml file and extracts structured information.
func readFileExcerpt(path string, maxBytes int) fileExcerpt {
	data, err := os.ReadFile(path)
	if err != nil {
		return fileExcerpt{}
	}
	content := string(data)
	if len(content) > maxBytes {
		content = content[:maxBytes]
	}

	ext := strings.ToLower(filepath.Ext(path))
	if ext == ".yaml" || ext == ".yml" {
		return parseYAMLExcerpt(content)
	}

	lines := strings.Split(content, "\n")
	var result fileExcerpt
	var paraLines []string
	inParagraph := false
	inCodeBlock := false

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)

		if strings.HasPrefix(trimmed, "```") {
			inCodeBlock = !inCodeBlock
			continue
		}
		if inCodeBlock {
			continue
		}

		if result.heading == "" && (strings.HasPrefix(trimmed, "# ") || strings.HasPrefix(trimmed, "## ")) {
			result.heading = stripMarkdownHeadingPrefix(trimmed)
			continue
		}

		if strings.HasPrefix(trimmed, "## ") || strings.HasPrefix(trimmed, "### ") {
			sectionName := stripMarkdownHeadingPrefix(trimmed)
			if len(result.sections) < 8 {
				result.sections = append(result.sections, sectionName)
			}
			if inParagraph {
				inParagraph = false
			}
			continue
		}

		if strings.HasPrefix(trimmed, "---") || strings.HasPrefix(trimmed, ">") || strings.HasPrefix(trimmed, "<") {
			continue
		}

		if result.heading != "" && result.body == "" {
			if !inParagraph && trimmed == "" {
				continue
			}
			if trimmed != "" {
				inParagraph = true
				paraLines = append(paraLines, trimmed)
				if len(paraLines) >= 4 {
					result.body = strings.Join(paraLines, " ")
					if len(result.body) > 250 {
						result.body = result.body[:250] + "..."
					}
				}
			} else if inParagraph {
				result.body = strings.Join(paraLines, " ")
				if len(result.body) > 250 {
					result.body = result.body[:250] + "..."
				}
				inParagraph = false
			}
		}
	}

	if result.body == "" && len(paraLines) > 0 {
		result.body = strings.Join(paraLines, " ")
		if len(result.body) > 250 {
			result.body = result.body[:250] + "..."
		}
	}

	result.technologies = detectTechnologies(content)

	return result
}

func parseYAMLExcerpt(content string) fileExcerpt {
	var result fileExcerpt
	for _, line := range strings.Split(content, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "name:") {
			result.heading = strings.TrimSpace(strings.TrimPrefix(trimmed, "name:"))
			result.heading = strings.Trim(result.heading, "\"'")
		}
		if strings.HasPrefix(trimmed, "description:") {
			result.body = strings.TrimSpace(strings.TrimPrefix(trimmed, "description:"))
			result.body = strings.Trim(result.body, "\"'")
		}
		if result.heading != "" && result.body != "" {
			break
		}
	}
	return result
}

func stripMarkdownHeadingPrefix(s string) string {
	for strings.HasPrefix(s, "#") {
		s = s[1:]
	}
	return strings.TrimSpace(s)
}

func detectTechnologies(content string) []string {
	lower := strings.ToLower(content)
	techKeywords := []struct {
		keyword string
		label   string
	}{
		{"golang", "Go"}, {"go module", "Go"}, {"go.mod", "Go"},
		{"typescript", "TypeScript"}, {"javascript", "JavaScript"},
		{"react", "React"}, {"next.js", "Next.js"}, {"nextjs", "Next.js"},
		{"vue", "Vue"}, {"angular", "Angular"}, {"svelte", "Svelte"},
		{"python", "Python"}, {"django", "Django"}, {"flask", "Flask"}, {"fastapi", "FastAPI"},
		{"rust", "Rust"}, {"cargo", "Rust"},
		{"docker", "Docker"}, {"kubernetes", "Kubernetes"}, {"k8s", "Kubernetes"},
		{"postgresql", "PostgreSQL"}, {"postgres", "PostgreSQL"},
		{"mysql", "MySQL"}, {"mongodb", "MongoDB"}, {"redis", "Redis"},
		{"sqlite", "SQLite"}, {"drizzle", "Drizzle ORM"},
		{"grpc", "gRPC"}, {"protobuf", "Protocol Buffers"},
		{"graphql", "GraphQL"}, {"rest api", "REST"},
		{"terraform", "Terraform"}, {"aws", "AWS"}, {"gcp", "GCP"}, {"azure", "Azure"},
		{"rabbitmq", "RabbitMQ"}, {"kafka", "Kafka"}, {"nats", "NATS"},
		{"nginx", "Nginx"}, {"caddy", "Caddy"},
		{"tailwind", "Tailwind CSS"}, {"sass", "Sass"},
		{"jest", "Jest"}, {"vitest", "Vitest"}, {"playwright", "Playwright"}, {"cypress", "Cypress"},
		{"bun ", "Bun"}, {"deno", "Deno"}, {"node.js", "Node.js"}, {"nodejs", "Node.js"},
		{"bubble tea", "Bubble Tea"}, {"bubbletea", "Bubble Tea"},
		{"openai", "OpenAI"}, {"anthropic", "Anthropic"}, {"gemini", "Gemini"},
		{"ollama", "Ollama"}, {"llm", "LLM"},
		{"llamacpp", "Llama.cpp"},
	}

	seen := map[string]bool{}
	var found []string
	for _, tk := range techKeywords {
		if !seen[tk.label] && strings.Contains(lower, tk.keyword) {
			seen[tk.label] = true
			found = append(found, tk.label)
		}
	}
	return found
}

func runQuietCmd(dir, name string, args ...string) string {
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func waitForChunks(ch chan tea.Msg) tea.Cmd {
	return func() tea.Msg {
		msg, ok := <-ch
		if !ok {
			return nil
		}
		return msg
	}
}

// ---------------------------------------------------------------------------
// Rendering helpers
// ---------------------------------------------------------------------------

// renderAgentColumns renders active sub-agents as side-by-side columns.
func renderAgentColumns(agents map[string]*agentLiveState, spinner string, width int) string {
	if len(agents) == 0 {
		return ""
	}

	type agentEntry struct {
		id string
		st *agentLiveState
	}
	sorted := make([]agentEntry, 0, len(agents))
	for id, st := range agents {
		sorted = append(sorted, agentEntry{id, st})
	}
	sort.Slice(sorted, func(i, j int) bool {
		if sorted[i].st.done != sorted[j].st.done {
			return !sorted[i].st.done
		}
		return sorted[i].st.spawnOrder < sorted[j].st.spawnOrder
	})

	cols := len(sorted)
	if cols > 3 {
		cols = 3
	}
	colWidth := (width - 4) / cols
	if colWidth < 20 {
		cols = 1
		colWidth = width - 4
		if colWidth < 20 {
			colWidth = 20
		}
	}

	activeBorder := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("#7C3AED")).
		Width(colWidth-2).
		Padding(0, 1)

	doneBorder := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("#374151")).
		Width(colWidth-2).
		Padding(0, 1)

	agentNameStyle := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#A78BFA"))
	doneNameStyle := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#374151"))

	var boxes []string
	for i, ae := range sorted {
		if i >= cols {
			break
		}
		st := ae.st
		agentName := shortAgentName(ae.id)
		if agentName == "" {
			agentName = ae.id
		}

		var content strings.Builder

		if st.done {
			content.WriteString(doneNameStyle.Render(agentName) + "\n")
			content.WriteString(dimStyle.Render("Done"))
		} else {
			content.WriteString(agentNameStyle.Render(agentName) + "\n")
			if st.step > 0 {
				content.WriteString(dimStyle.Render(fmt.Sprintf("Step %d", st.step)) + "\n")
			}
			if st.currentTool != "" {
				line := toolStyle.Render(st.currentTool)
				if st.currentArgs != "" {
					arg := st.currentArgs
					maxArg := colWidth - len(st.currentTool) - 4
					if maxArg < 4 {
						maxArg = 4
					}
					if len(arg) > maxArg {
						arg = arg[:maxArg] + "..."
					}
					line += " " + dimStyle.Render(arg)
				}
				content.WriteString(line + "\n")
			} else if st.lastStatus != "" {
				status := st.lastStatus
				if len(status) > colWidth-2 {
					status = status[:colWidth-2] + "..."
				}
				content.WriteString(dimStyle.Render(status) + "\n")
			} else {
				content.WriteString(dimStyle.Render(spinner+" Thinking") + "\n")
			}
		}

		box := activeBorder
		if st.done {
			box = doneBorder
		}
		boxes = append(boxes, box.Render(content.String()))
	}

	if len(boxes) == 0 {
		return ""
	}

	row := lipgloss.JoinHorizontal(lipgloss.Top, boxes...)
	return "  " + row + "\n"
}

func renderActivities(activities []activityEntry, width int) string {
	var b strings.Builder
	for _, a := range activities {
		agentTag := agentPrefix(a.AgentID)
		agentLabel := ""
		if agentTag != "" {
			agentLabel = dimStyle.Render(agentTag) + " "
		}
		switch a.Kind {
		case actStepStart:
			tagText := ""
			if agentTag != "" {
				tagText = agentTag + " "
			}
			rawLabel := fmt.Sprintf(" %sStep %d ", tagText, a.Step)
			lineWidth := width - len(rawLabel) - 4
			if lineWidth < 10 {
				lineWidth = 10
			}
			line := strings.Repeat("-", lineWidth)
			b.WriteString("  " + stepStyle.Render("--"+rawLabel) + dimStyle.Render(line) + "\n")

		case actToolCall:
			icon := toolStyle.Render("*")
			verb := toolStyle.Render(toolDisplayName(a.ToolName))
			if a.Args != "" {
				b.WriteString(fmt.Sprintf("  %s %s%s  %s\n", icon, agentLabel, verb, dimStyle.Render(a.Args)))
			} else {
				b.WriteString(fmt.Sprintf("  %s %s%s\n", icon, agentLabel, verb))
			}

		case actToolResult:
			if a.IsError {
				b.WriteString(fmt.Sprintf("  %s %s%s\n", errorStyle.Render("x"), agentLabel, errorStyle.Render(a.Result)))
			} else {
				b.WriteString(fmt.Sprintf("  %s %s%s\n", successStyle.Render("ok"), agentLabel, resultStyle.Render(a.Result)))
			}

		case actStatus:
			icon := dimStyle.Render(">")
			b.WriteString(fmt.Sprintf("  %s %s%s\n", icon, agentLabel, dimStyle.Render(a.Result)))
		}
	}
	return b.String()
}

func renderWelcomeScreen(version, buildTime string, width int, collapsed bool) string {
	var b strings.Builder

	b.WriteString(bannerStyle.Render(bujicoderBanner) + "\n")
	b.WriteString(dimStyle.Render(fmt.Sprintf("  v%s -- AI Coding Assistant . built %s", version, buildTime)) + "\n\n")

	sepWidth := width - 4
	if sepWidth < 20 {
		sepWidth = 20
	}
	if sepWidth > 60 {
		sepWidth = 60
	}
	sep := dimStyle.Render("  " + strings.Repeat("-", sepWidth))

	// Getting Started
	b.WriteString("  " + sectionStyle.Render("Getting Started") + "\n")
	b.WriteString(sep + "\n")
	b.WriteString(descStyle.Render("  Type a message and press Enter to chat with BujiCoder.") + "\n")
	b.WriteString(descStyle.Render("  BujiCoder can read, write, and edit files in your project.") + "\n\n")

	if collapsed {
		b.WriteString(descStyle.Render("  Type / to see available commands") + "\n\n")
		return b.String()
	}

	// Commands
	b.WriteString("  " + sectionStyle.Render("Commands") + "\n")
	b.WriteString(sep + "\n")
	for _, c := range slashCommands {
		b.WriteString(fmt.Sprintf("  %s  %s\n",
			cmdStyle.Render(fmt.Sprintf("%-16s", c.cmd)),
			descStyle.Render(c.desc)))
	}
	b.WriteString("\n")

	// Keyboard Shortcuts
	b.WriteString("  " + sectionStyle.Render("Keyboard Shortcuts") + "\n")
	b.WriteString(sep + "\n")
	keys := []struct{ key, desc string }{
		{"Enter", "Send message"},
		{"Up/Down", "Scroll chat up/down"},
		{"PgUp/PgDn", "Page up/down"},
		{"Ctrl+Up/Down", "Browse prompt history"},
		{"Left/Right", "Move cursor in input"},
		{"Home/End", "Jump to start/end of input"},
		{"Delete", "Delete character after cursor"},
		{"Ctrl+Y", "Copy last response to clipboard"},
		{"Ctrl+C", "Quit BujiCoder"},
	}
	for _, k := range keys {
		b.WriteString(fmt.Sprintf("  %s  %s\n",
			cmdStyle.Render(fmt.Sprintf("%-16s", k.key)),
			descStyle.Render(k.desc)))
	}
	b.WriteString("\n")

	// Tips
	b.WriteString("  " + sectionStyle.Render("Tips") + "\n")
	b.WriteString(sep + "\n")
	tips := []string{
		"Ask BujiCoder to explain, refactor, or debug your code",
		"Request file edits -- BujiCoder shows exactly what changed",
		"Use /mode max for complex multi-step tasks",
	}
	for _, t := range tips {
		b.WriteString("  " + tipStyle.Render("*") + " " + descStyle.Render(t) + "\n")
	}
	b.WriteString("\n")
	b.WriteString(descStyle.Render("  For teams: community.bujicoder.com") + "\n\n")

	return b.String()
}

func (m Model) renderHistoryView() string {
	var b strings.Builder

	b.WriteString("\n")
	b.WriteString(bannerStyle.Render(bujicoderBanner) + "\n\n")

	sep := dimStyle.Render("  " + strings.Repeat("-", 44))

	b.WriteString("  " + sectionStyle.Render("Conversation History") + "\n")
	b.WriteString(sep + "\n\n")

	if len(m.historyItems) == 0 {
		b.WriteString(descStyle.Render("  No conversations found.") + "\n")
	} else {
		visibleRows := m.height - 8
		if visibleRows < 1 {
			visibleRows = 1
		}
		end := m.historyOffset + visibleRows
		if end > len(m.historyItems) {
			end = len(m.historyItems)
		}

		if m.historyOffset > 0 {
			b.WriteString(dimStyle.Render("  ^ more above") + "\n")
		}

		for i := m.historyOffset; i < end; i++ {
			c := m.historyItems[i]
			id := c.ID
			if len(id) > 8 {
				id = id[:8]
			}
			title := c.Title
			if title == "" {
				title = "(untitled)"
			}
			maxTitle := m.width - 30
			if maxTitle < 20 {
				maxTitle = 20
			}
			if len(title) > maxTitle {
				title = title[:maxTitle] + "..."
			}
			date := ""
			if len(c.UpdatedAt) >= 10 {
				date = c.UpdatedAt[:10]
			}

			if i == m.historyCursor {
				b.WriteString(promptStyle.Render("  > ") + cmdStyle.Render(id) + "  " + title + "  " + dimStyle.Render(date) + "\n")
			} else {
				b.WriteString(dimStyle.Render("    "+id+"  "+title+"  "+date) + "\n")
			}
		}

		if end < len(m.historyItems) {
			b.WriteString(dimStyle.Render("  v more below") + "\n")
		}

		b.WriteString("\n")
		b.WriteString(dimStyle.Render(fmt.Sprintf("  %d conversations . Up/Down: navigate . Enter: resume . Esc: back", len(m.historyItems))) + "\n")
	}

	return b.String()
}

func renderCompletionFooter(steps int, elapsed time.Duration, inputTokens, outputTokens int, costCents int64) string {
	parts := []string{successStyle.Render("Done")}
	if steps > 0 {
		part := "step"
		if steps > 1 {
			part = "steps"
		}
		parts = append(parts, timeStyle.Render(fmt.Sprintf("%d %s", steps, part)))
	}
	if elapsed > 0 {
		parts = append(parts, timeStyle.Render(formatElapsed(elapsed)))
	}
	totalTokens := inputTokens + outputTokens
	if totalTokens > 0 {
		parts = append(parts, timeStyle.Render(formatTokenCount(totalTokens)+" tokens"))
	}
	return "  " + strings.Join(parts, timeStyle.Render(" . "))
}

// formatTokenCount formats a token count with K/M suffixes for readability.
func formatTokenCount(n int) string {
	if n >= 1_000_000 {
		return fmt.Sprintf("%.1fM", float64(n)/1_000_000)
	}
	if n >= 1_000 {
		return fmt.Sprintf("%.1fK", float64(n)/1_000)
	}
	return fmt.Sprintf("%d", n)
}

// syncViewport rebuilds viewport content from current state.
func (m Model) syncViewport() Model {
	if !m.ready || m.state != StateChat {
		return m
	}

	fh := m.calcFooterHeight()
	vpHeight := m.height - fh
	if vpHeight < 1 {
		vpHeight = 1
	}

	heightChanged := m.viewport.Height != vpHeight
	if heightChanged {
		m.viewport.Height = vpHeight
	}

	// During streaming, only rebuild content on tick (100ms) or height changes
	// to avoid flickering from per-chunk SetContent calls.
	if !m.viewportDirty && !heightChanged {
		return m
	}

	atBottom := m.viewport.AtBottom()
	m.viewport.SetContent(m.buildScrollableContent())
	if atBottom {
		m.viewport.GotoBottom()
	}
	m.viewportDirty = false
	return m
}

// buildScrollableContent renders all chat content for the viewport.
func (m Model) buildScrollableContent() string {
	var b strings.Builder
	effectiveWidth := m.width
	if effectiveWidth == 0 {
		effectiveWidth = 80
	}

	if len(m.messages) == 0 && !m.streaming {
		b.WriteString(renderWelcomeScreen(m.version, m.buildTime, effectiveWidth, m.welcomeCollapsed))
		if m.updateVersion != "" {
			b.WriteString(updateStyle.Render(fmt.Sprintf("  Update available: v%s -> v%s  --  run 'buji update' to upgrade", m.version, m.updateVersion)) + "\n\n")
		}
	} else {
		header := titleStyle.Render(fmt.Sprintf(" BujiCoder %s ", m.version)) + dimStyle.Render(fmt.Sprintf(" built %s", m.buildTime))
		b.WriteString(header + "\n\n")
	}

	// Render completed messages (with their activity logs)
	for _, msg := range m.messages {
		switch msg.Role {
		case "user":
			b.WriteString(userStyle.Render("You") + " " + msg.Content + "\n\n")
		case "assistant":
			if len(msg.Activities) > 0 {
				b.WriteString(renderActivities(msg.Activities, effectiveWidth))
				b.WriteString("\n")
			}
			displayContent := msg.RenderedContent
			if displayContent == "" {
				displayContent = msg.Content
			}
			b.WriteString(assistStyle.Render("BujiCoder") + "\n" + displayContent + "\n")
			if msg.Elapsed > 0 {
				b.WriteString(renderCompletionFooter(msg.Steps, msg.Elapsed, msg.InputTokens, msg.OutputTokens, msg.CostCents) + "\n")
			}
			b.WriteString("\n")
		}
	}

	// Render current streaming response
	if m.streaming {
		if len(m.activities) > 0 {
			b.WriteString(renderActivities(m.activities, effectiveWidth))
			b.WriteString("\n")
		}
		if len(m.liveAgents) > 1 {
			spinner := spinnerFrames[m.spinnerFrame]
			b.WriteString(renderAgentColumns(m.liveAgents, spinner, effectiveWidth))
			b.WriteString("\n")
		}
		if m.streamBuf != "" {
			b.WriteString(assistStyle.Render("BujiCoder") + " " + m.streamBuf + dimStyle.Render("|") + "\n")
		}

		if len(m.subAgentStreams) > 0 {
			var agentIDs []string
			for id := range m.subAgentStreams {
				agentIDs = append(agentIDs, id)
			}
			sort.Strings(agentIDs)

			for _, id := range agentIDs {
				name := shortAgentName(id)
				if name == "" {
					name = id
				}
				streamTxt := m.subAgentStreams[id]
				if streamTxt != "" {
					b.WriteString("\n" + stepStyle.Render("-- "+name) + " " + streamTxt + dimStyle.Render("|") + "\n")
				}
			}
		}

		elapsed := time.Since(m.startTime)
		spinner := dimStyle.Render(spinnerFrames[m.spinnerFrame])
		activityText := dimStyle.Render(m.lastActivity)
		elapsedText := timeStyle.Render(formatElapsed(elapsed))
		b.WriteString(fmt.Sprintf("\n  %s %s %s %s\n", spinner, activityText, timeStyle.Render("."), elapsedText))
		b.WriteString("\n")
	}

	if m.err != nil {
		b.WriteString(errorStyle.Render(fmt.Sprintf("Error: %v", m.err)) + "\n")
	}

	return b.String()
}

// wrapInput splits input text into display lines that fit within the terminal width.
func wrapInput(input string, width int) []string {
	const promptWidth = 2
	availWidth := width - promptWidth
	if availWidth < 1 {
		availWidth = 1
	}
	runes := []rune(input)
	if len(runes) <= availWidth {
		return []string{input}
	}
	var lines []string
	for len(runes) > 0 {
		end := availWidth
		if end > len(runes) {
			end = len(runes)
		}
		lines = append(lines, string(runes[:end]))
		runes = runes[end:]
	}
	return lines
}

// updateAutocomplete refreshes the autocomplete matches based on the current input.
func (m *Model) updateAutocomplete() {
	if !strings.HasPrefix(m.input, "/") || strings.Contains(m.input, " ") || m.input == "" {
		m.acVisible = false
		m.acMatches = nil
		m.acCursor = 0
		return
	}
	prefix := strings.ToLower(m.input)
	var matches []int
	for i, sc := range slashCommands {
		if strings.HasPrefix(sc.cmd, prefix) && sc.cmd != prefix {
			matches = append(matches, i)
		}
	}
	m.acMatches = matches
	m.acVisible = len(matches) > 0
	if m.acCursor >= len(matches) {
		m.acCursor = 0
	}
}

func (m Model) calcFooterHeight() int {
	if m.width <= 0 {
		return 1
	}
	if m.streaming {
		if m.pendingApproval != "" || m.pendingQuestion != "" {
			innerWidth := m.width - 4
			if innerWidth < 10 {
				innerWidth = 10
			}
			lines := wrapInput(m.input, innerWidth-1)
			// box (content + 2 border) + prompt banner + status line
			return len(lines) + 4 + 1
		}
		// Streaming with no approval/question: just the status bar (1 line).
		return 1
	}
	innerWidth := m.width - 4
	if innerWidth < 10 {
		innerWidth = 10
	}
	lines := wrapInput(m.input, innerWidth-1)
	height := len(lines) + 4
	if m.modePickerVisible {
		height += len(modeOptions) + 1 // options + header
	}
	if m.acVisible && len(m.acMatches) > 0 {
		height += len(m.acMatches)
	}
	return height
}

// renderFooter renders the fixed input prompt and status bar at the bottom.
func (m Model) renderFooter() string {
	modeIndicator := string(m.costMode)
	if m.planMode {
		modeIndicator = "plan"
	}
	permIndicator := ""
	if m.permissions != nil {
		permIndicator = fmt.Sprintf(" · permissions:%s", m.permissions.Mode)
	}
	status := fmt.Sprintf("  mode:%s%s . ctrl+y copy . ctrl+c quit . Up/Down scroll", modeIndicator, permIndicator)
	if m.streaming && m.pendingApproval == "" && m.pendingQuestion == "" {
		return dimStyle.Render(status)
	}

	innerWidth := m.width - 4
	if innerWidth < 10 {
		innerWidth = 10
	}

	cursorChar := " "
	if m.spinnerFrame < 5 {
		cursorChar = promptStyle.Render("|")
	}

	var content strings.Builder
	if m.input == "" {
		if m.pendingApproval != "" {
			content.WriteString(promptStyle.Render("> ") + cursorChar + dimStyle.Render("y/n"))
		} else {
			content.WriteString(promptStyle.Render("> ") + cursorChar + dimStyle.Render("Type a message..."))
		}
	} else {
		// Insert cursor character at cursorPos within the input text.
		runes := []rune(m.input)
		pos := m.cursorPos
		if pos > len(runes) {
			pos = len(runes)
		}
		displayText := string(runes[:pos]) + cursorChar + string(runes[pos:])
		lines := wrapInput(displayText, innerWidth-1)
		for i, line := range lines {
			if i == 0 {
				content.WriteString(promptStyle.Render("> ") + line)
			} else {
				content.WriteString("\n  " + line)
			}
		}
	}

	boxWidth := m.width - 2
	if boxWidth < 10 {
		boxWidth = 10
	}
	box := inputBoxStyle.Width(boxWidth).Render(content.String())

	var promptBanner string
	if m.pendingApproval != "" {
		warnStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#F59E0B")).Bold(true)
		promptBanner = warnStyle.Render(fmt.Sprintf("  Approve: %s", m.pendingApprovalCmd))
		if m.pendingApproval != "" {
			promptBanner += "\n" + dimStyle.Render(fmt.Sprintf("    %s -- type y/n and press enter", m.pendingApproval))
		}
		promptBanner += "\n"
	} else if m.pendingQuestion != "" {
		promptBanner = toolStyle.Render(fmt.Sprintf("  ? %s", m.pendingQuestion)) + "\n"
	}

	var acDropdown string
	if m.modePickerVisible {
		currentMode := string(m.costMode)
		if m.planMode {
			currentMode = "plan"
		}
		var mp strings.Builder
		mp.WriteString("  " + dimStyle.Render(fmt.Sprintf("Select mode (current: %s):", currentMode)) + "\n")
		for i, opt := range modeOptions {
			marker := "  "
			if opt.name == currentMode {
				marker = "* "
			}
			if i == m.modePickerCursor {
				mp.WriteString("  " + promptStyle.Render("> ") + cmdStyle.Render(fmt.Sprintf("%-10s", marker+opt.name)) + " " + descStyle.Render(opt.desc) + "\n")
			} else {
				mp.WriteString("    " + dimStyle.Render(fmt.Sprintf("%-10s", marker+opt.name)) + " " + dimStyle.Render(opt.desc) + "\n")
			}
		}
		acDropdown = mp.String()
	} else if m.acVisible && len(m.acMatches) > 0 {
		var ac strings.Builder
		for i, idx := range m.acMatches {
			sc := slashCommands[idx]
			if i == m.acCursor {
				ac.WriteString("  " + promptStyle.Render("> ") + cmdStyle.Render(fmt.Sprintf("%-16s", sc.cmd)) + " " + descStyle.Render(sc.desc) + "\n")
			} else {
				ac.WriteString("    " + dimStyle.Render(fmt.Sprintf("%-16s", sc.cmd)) + " " + dimStyle.Render(sc.desc) + "\n")
			}
		}
		acDropdown = ac.String()
	}

	return promptBanner + acDropdown + box + "\n" + dimStyle.Render(status)
}

// View renders the TUI.
func (m Model) View() string {
	switch m.state {
	case StateChat:
		if !m.ready {
			return "\n  Initializing...\n"
		}
		return m.viewport.View() + m.renderFooter()

	case StateHistory:
		return m.renderHistoryView()

	case StateSetup:
		return m.renderSetupView()
	}

	return ""
}
