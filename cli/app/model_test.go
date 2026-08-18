package app

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	cliconfig "github.com/TechnoAllianceAE/bujicoder/cli/config"
	"github.com/TechnoAllianceAE/bujicoder/shared/agentruntime"
	"github.com/TechnoAllianceAE/bujicoder/shared/costmode"
	"github.com/TechnoAllianceAE/bujicoder/shared/store"
)

// chatModel returns a Model in chat state, as it exists before the first
// WindowSizeMsg (width/height still zero) unless overridden.
func chatModel() Model {
	return Model{
		state:      StateChat,
		messages:   []ChatMessage{},
		costMode:   costmode.ModeNormal,
		historyIdx: -1,
	}
}

func enterKey() tea.KeyMsg { return tea.KeyMsg{Type: tea.KeyEnter} }

func runes(s string) tea.KeyMsg {
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
}

func lastContent(t *testing.T, m Model) string {
	t.Helper()
	if len(m.messages) == 0 {
		t.Fatal("expected at least one message")
	}
	return m.messages[len(m.messages)-1].Content
}

func TestShortID(t *testing.T) {
	tests := []struct {
		in, want string
	}{
		{"", ""},
		{"abc", "abc"},
		{"12345678", "12345678"},
		{"123456789", "12345678"},
		{"550e8400-e29b-41d4-a716-446655440000", "550e8400"},
	}
	for _, tc := range tests {
		if got := shortID(tc.in); got != tc.want {
			t.Errorf("shortID(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestTruncateRunes(t *testing.T) {
	tests := []struct {
		name string
		in   string
		max  int
		want string
	}{
		{name: "shorter than max", in: "abc", max: 10, want: "abc"},
		{name: "exact", in: "abc", max: 3, want: "abc"},
		{name: "clipped", in: "abcdef", max: 3, want: "abc"},
		{name: "zero max", in: "abc", max: 0, want: ""},
		{name: "negative max", in: "abc", max: -1, want: ""},
		// Byte slicing would split these multi-byte runes into invalid UTF-8.
		{name: "multibyte kept intact", in: "héllo→", max: 2, want: "hé"},
		{name: "emoji", in: "🙂🙂🙂", max: 2, want: "🙂🙂"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := truncateRunes(tc.in, tc.max)
			if got != tc.want {
				t.Fatalf("truncateRunes(%q, %d) = %q, want %q", tc.in, tc.max, got, tc.want)
			}
			if !utf8Valid(got) {
				t.Errorf("result %q is not valid UTF-8", got)
			}
		})
	}
}

func utf8Valid(s string) bool {
	for _, r := range s {
		if r == '\uFFFD' {
			return false
		}
	}
	return true
}

// A search hit whose conversation ID is shorter than the 8-character display
// prefix used to panic with a slice-bounds error and take the whole TUI down.
func TestSearchResultShortConversationID(t *testing.T) {
	m := chatModel()
	got, _ := m.handleUpdate(searchResultMsg{results: []store.SearchResult{
		{ConversationID: "ab", ConversationTitle: "short", Snippet: "hit"},
		{ConversationID: "", ConversationTitle: "empty", Snippet: "hit"},
		{ConversationID: "550e8400-e29b", ConversationTitle: "uuid", Snippet: strings.Repeat("x", 300)},
	}})

	content := lastContent(t, got)
	for _, want := range []string{"`ab`", "`550e8400`", "Found 3 results"} {
		if !strings.Contains(content, want) {
			t.Errorf("search output missing %q:\n%s", want, content)
		}
	}
}

func TestResumeResultShortConversationID(t *testing.T) {
	m := chatModel()
	got, _ := m.handleUpdate(resumeResultMsg{
		conversationID: "ab",
		messages: []ConversationMessage{
			{Role: "user", Content: "hi"},
			{Role: "assistant", Content: "hello"},
		},
	})

	if len(got.messages) != 3 {
		t.Fatalf("got %d messages, want 2 restored + 1 notice", len(got.messages))
	}
	if got.conversationID != "ab" {
		t.Errorf("conversationID = %q, want ab", got.conversationID)
	}
	if !strings.Contains(lastContent(t, got), "Resumed conversation ab") {
		t.Errorf("unexpected notice: %s", lastContent(t, got))
	}
}

// A run that ends — for any reason — must drop the "waiting on the user" states,
// otherwise the footer keeps prompting for an answer nobody will read.
func TestStreamDoneClearsRunningState(t *testing.T) {
	tests := []struct {
		name string
		msg  streamDoneMsg
	}{
		{name: "success", msg: streamDoneMsg{}},
		{name: "failure", msg: streamDoneMsg{err: os.ErrDeadlineExceeded}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			m := chatModel()
			m.streaming = true
			m.pendingQuestion = "Which file?"
			m.pendingApproval = "rm -rf /"
			m.pendingApprovalCmd = "rm -rf /"
			m.streamBuf = "partial answer"
			m.liveAgents = map[string]*agentLiveState{"researcher": {step: 2}}

			got, _ := m.handleUpdate(tc.msg)

			if got.streaming {
				t.Error("streaming must be false after streamDoneMsg")
			}
			if got.pendingQuestion != "" || got.pendingApproval != "" || got.pendingApprovalCmd != "" {
				t.Errorf("pending prompts not cleared: q=%q approval=%q cmd=%q",
					got.pendingQuestion, got.pendingApproval, got.pendingApprovalCmd)
			}
			if got.liveAgents != nil {
				t.Error("live agent columns must be cleared")
			}
			if got.streamBuf != "" {
				t.Error("stream buffer must be flushed into a message")
			}
		})
	}
}

// Slash commands must never panic and must not be sent to the LLM when they are
// incomplete: the user gets a usage hint instead.
func TestSlashCommandParsing(t *testing.T) {
	tests := []struct {
		name         string
		input        string
		wantContains string
		wantStream   bool
	}{
		{name: "bare slash is treated as a prompt", input: "/", wantStream: true},
		{name: "unknown command is treated as a prompt", input: "/definitely-not-a-command", wantStream: true},
		{name: "resume without id", input: "/resume", wantContains: "Usage: /resume"},
		{name: "resume with only spaces", input: "/resume    ", wantContains: "Usage: /resume"},
		{name: "search without query", input: "/search", wantContains: "Usage: /search"},
		{name: "history without store", input: "/history", wantContains: "Local history not available."},
		{name: "mcp add missing args", input: "/mcp add browser", wantContains: "Usage: `/mcp add"},
		{name: "mcp remove missing name", input: "/mcp remove", wantContains: "Usage: `/mcp remove"},
		{name: "mcp status", input: "/mcp", wantContains: "MCP Servers"},
		{name: "usage", input: "/usage", wantContains: "BujiCoder Enterprise"},
		{name: "new conversation", input: "/new", wantContains: "Started new conversation."},
		{name: "copy with no response", input: "/copy", wantContains: "No assistant response to copy."},
		{name: "models", input: "/models", wantContains: "Registered Providers"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			m := chatModel()
			m.input = tc.input
			m.cursorPos = len([]rune(tc.input))

			got, _ := m.handleUpdate(enterKey())

			if got.input != "" {
				t.Errorf("input not consumed: %q", got.input)
			}
			if tc.wantStream {
				// Not a recognised command: it becomes a user message. The runtime
				// is not ready in this model, so streaming is refused with a notice.
				if len(got.messages) == 0 || got.messages[0].Role != "user" {
					t.Fatalf("expected the text to be sent as a user message, got %+v", got.messages)
				}
				if got.messages[0].Content != strings.TrimSpace(tc.input) {
					t.Errorf("user message = %q, want %q", got.messages[0].Content, tc.input)
				}
				return
			}
			if content := lastContent(t, got); !strings.Contains(content, tc.wantContains) {
				t.Errorf("response %q does not contain %q", content, tc.wantContains)
			}
		})
	}
}

func TestModeCommand(t *testing.T) {
	t.Run("no argument opens the picker", func(t *testing.T) {
		m := chatModel()
		m.input = "/mode"
		got, _ := m.handleUpdate(enterKey())
		if !got.modePickerVisible {
			t.Fatal("mode picker should be visible")
		}
		if got.modePickerCursor < 0 || got.modePickerCursor >= len(modeOptions) {
			t.Fatalf("picker cursor %d out of range", got.modePickerCursor)
		}
	})

	t.Run("unknown mode falls back to normal", func(t *testing.T) {
		m := chatModel()
		m.costMode = costmode.ModeMax
		m.input = "/mode bogus"
		got, _ := m.handleUpdate(enterKey())
		if got.modePickerVisible {
			t.Error("picker must not open when an argument is given")
		}
		if got.costMode != costmode.ModeNormal {
			t.Errorf("costMode = %q, want normal", got.costMode)
		}
	})

	t.Run("plan mode", func(t *testing.T) {
		m := chatModel()
		m.input = "/mode plan"
		got, _ := m.handleUpdate(enterKey())
		if !got.planMode {
			t.Error("planMode should be enabled")
		}
	})

	t.Run("picker selection with cursor at each option", func(t *testing.T) {
		for i := range modeOptions {
			m := chatModel()
			m.modePickerVisible = true
			m.modePickerCursor = i
			got, _ := m.handleUpdate(enterKey())
			if got.modePickerVisible {
				t.Errorf("option %d: picker should close", i)
			}
		}
	})
}

func TestAutocompleteCursorStaysInRange(t *testing.T) {
	m := chatModel()
	m.input = "/m"
	m.updateAutocomplete()
	if !m.acVisible || len(m.acMatches) == 0 {
		t.Fatal("expected autocomplete matches for /m")
	}
	// Move the cursor to the last match, then narrow the match set.
	m.acCursor = len(m.acMatches) - 1
	m.input = "/mo"
	m.updateAutocomplete()
	if m.acCursor >= len(m.acMatches) {
		t.Fatalf("acCursor %d out of range for %d matches", m.acCursor, len(m.acMatches))
	}

	// Accepting the selection with tab / right / enter must not panic.
	for _, key := range []tea.KeyMsg{{Type: tea.KeyTab}, {Type: tea.KeyRight}, enterKey()} {
		mm := chatModel()
		mm.input = "/m"
		mm.updateAutocomplete()
		mm.acCursor = len(mm.acMatches) - 1
		_, _ = mm.handleUpdate(key)
	}
}

// "/mode" is a prefix of "/models", so the autocomplete dropdown is open when the
// user presses enter. Accepting the suggestion there ran /models and made the
// mode picker unreachable by typing the command.
func TestExactCommandBeatsAutocompleteSuggestion(t *testing.T) {
	m := chatModel()
	m.input = "/mode"
	m.cursorPos = len("/mode")
	m.updateAutocomplete()
	if !m.acVisible {
		t.Fatal("expected the autocomplete dropdown to be open for /mode")
	}

	got, _ := m.handleUpdate(enterKey())
	if !got.modePickerVisible {
		t.Fatalf("enter on the exact command /mode must open the mode picker, got messages %+v", got.messages)
	}
	if got.acVisible || got.acMatches != nil {
		t.Error("autocomplete state should be cleared after enter")
	}

	// A genuine prefix still completes to the highlighted suggestion.
	p := chatModel()
	p.input = "/mod"
	p.updateAutocomplete()
	completed, _ := p.handleUpdate(enterKey())
	if !completed.modePickerVisible && len(completed.messages) == 0 {
		t.Error("a partial command should still be completed and executed")
	}
	if isSlashCommand("/nope") || !isSlashCommand("/MODE") {
		t.Error("isSlashCommand should match known commands case-insensitively")
	}
}

func TestTypingAndEditingInput(t *testing.T) {
	m := chatModel()
	for _, ch := range []string{"h", "e", "l", "l", "o"} {
		m, _ = m.handleUpdate(runes(ch))
	}
	if m.input != "hello" || m.cursorPos != 5 {
		t.Fatalf("input = %q cursor = %d, want hello/5", m.input, m.cursorPos)
	}

	// Home, then delete forward, then backspace at position 0.
	m, _ = m.handleUpdate(tea.KeyMsg{Type: tea.KeyHome})
	if m.cursorPos != 0 {
		t.Fatalf("cursor = %d after home, want 0", m.cursorPos)
	}
	m, _ = m.handleUpdate(tea.KeyMsg{Type: tea.KeyDelete})
	if m.input != "ello" {
		t.Fatalf("input = %q after delete, want ello", m.input)
	}
	m, _ = m.handleUpdate(tea.KeyMsg{Type: tea.KeyBackspace})
	if m.input != "ello" {
		t.Fatalf("backspace at start must not change input, got %q", m.input)
	}

	// Multi-byte input must be indexed by rune, not byte.
	m2 := chatModel()
	m2, _ = m2.handleUpdate(runes("héllo→"))
	m2, _ = m2.handleUpdate(tea.KeyMsg{Type: tea.KeyBackspace})
	if m2.input != "héllo" {
		t.Fatalf("input = %q, want héllo", m2.input)
	}
	if m2.cursorPos != len([]rune(m2.input)) {
		t.Fatalf("cursor = %d, want %d", m2.cursorPos, len([]rune(m2.input)))
	}
}

func TestPromptHistoryNavigation(t *testing.T) {
	m := chatModel()
	m.promptHistory = []string{"first", "second"}

	m, _ = m.handleUpdate(tea.KeyMsg{Type: tea.KeyUp, Alt: false, Runes: nil})
	// Plain up scrolls the viewport, not history; ctrl+up browses history.
	m.historyIdx = -1
	m, _ = m.handleUpdate(tea.KeyMsg{Type: tea.KeyCtrlUp})
	if m.input != "second" {
		t.Fatalf("input = %q, want second", m.input)
	}
	m, _ = m.handleUpdate(tea.KeyMsg{Type: tea.KeyCtrlUp})
	if m.input != "first" {
		t.Fatalf("input = %q, want first", m.input)
	}
	m, _ = m.handleUpdate(tea.KeyMsg{Type: tea.KeyCtrlDown})
	if m.input != "second" {
		t.Fatalf("input = %q, want second", m.input)
	}
	m, _ = m.handleUpdate(tea.KeyMsg{Type: tea.KeyCtrlDown})
	if m.input != "" || m.historyIdx != -1 {
		t.Fatalf("expected the saved (empty) input back, got %q idx %d", m.input, m.historyIdx)
	}
	if m.cursorPos != len([]rune(m.input)) {
		t.Fatalf("cursor %d must follow recalled input length %d", m.cursorPos, len([]rune(m.input)))
	}
}

// Navigating an empty history list must not index into it.
func TestHistoryViewWithNoItems(t *testing.T) {
	m := chatModel()
	m.state = StateHistory
	for _, key := range []tea.KeyMsg{
		{Type: tea.KeyUp}, {Type: tea.KeyDown}, {Type: tea.KeyPgUp}, {Type: tea.KeyPgDown},
		{Type: tea.KeyHome}, {Type: tea.KeyEnd}, enterKey(),
	} {
		m, _ = m.handleUpdate(key)
	}
	if view := m.renderHistoryView(); !strings.Contains(view, "No conversations found") {
		t.Errorf("unexpected history view:\n%s", view)
	}
}

func TestHistoryNavigationStaysInBounds(t *testing.T) {
	m := chatModel()
	m.state = StateHistory
	m.height = 0 // zero-size terminal
	m.historyItems = []store.ConversationSummary{
		{ID: "a", Title: "one", UpdatedAt: "2024-01-01T00:00:00Z"},
		{ID: "bb", Title: "two", UpdatedAt: "2024-01-02T00:00:00Z"},
	}
	keys := []tea.KeyMsg{
		{Type: tea.KeyDown}, {Type: tea.KeyDown}, {Type: tea.KeyDown},
		{Type: tea.KeyPgDown}, {Type: tea.KeyPgUp},
		{Type: tea.KeyUp}, {Type: tea.KeyUp},
		{Type: tea.KeyEnd}, {Type: tea.KeyHome},
	}
	for _, k := range keys {
		m, _ = m.handleUpdate(k)
		if m.historyCursor < 0 || m.historyCursor >= len(m.historyItems) {
			t.Fatalf("historyCursor %d out of range after %v", m.historyCursor, k)
		}
		_ = m.renderHistoryView()
	}
}

// Rendering must survive a zero-size terminal: lipgloss width math and the
// wrapping helpers all take the terminal width as input.
func TestRenderingWithZeroSizeTerminal(t *testing.T) {
	for _, size := range [][2]int{{0, 0}, {1, 1}, {2, 3}, {80, 24}} {
		width, height := size[0], size[1]
		t.Run(strings.Join([]string{itoa(width), itoa(height)}, "x"), func(t *testing.T) {
			m := chatModel()
			m.width, m.height = width, height
			m.streaming = true
			m.lastActivity = "Thinking"
			m.activities = []activityEntry{
				{Kind: actStepStart, Step: 1},
				{Kind: actToolCall, ToolName: "read_file", Args: strings.Repeat("p", 200)},
				{Kind: actToolResult, Result: strings.Repeat("r", 200), IsError: true},
				{Kind: actStatus, Result: "working"},
			}
			m.liveAgents = map[string]*agentLiveState{
				"researcher": {step: 1, currentTool: "read_file", currentArgs: strings.Repeat("a", 200)},
				"editor":     {step: 2, lastStatus: strings.Repeat("s", 200)},
				"reviewer":   {done: true},
			}
			m.subAgentStreams = map[string]string{"researcher": "text"}
			m.input = strings.Repeat("z", 120)
			m.cursorPos = len(m.input)
			m.acVisible = true
			m.acMatches = []int{0, 1}

			_ = m.buildScrollableContent()
			if fh := m.calcFooterHeight(); fh < 1 {
				t.Errorf("footer height = %d, want >= 1", fh)
			}
			_ = m.renderFooter()
			_ = m.View()
			_ = renderWelcomeScreen("1.0.0", "today", width, false)
			_ = renderActivities(m.activities, width)
			_ = renderAgentColumns(m.liveAgents, "|", width)
		})
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

func TestWrapInput(t *testing.T) {
	tests := []struct {
		name  string
		input string
		width int
		want  []string
	}{
		{name: "fits", input: "abc", width: 10, want: []string{"abc"}},
		{name: "zero width", input: "abc", width: 0, want: []string{"a", "b", "c"}},
		{name: "negative width", input: "ab", width: -5, want: []string{"a", "b"}},
		{name: "exact split", input: "abcd", width: 4, want: []string{"ab", "cd"}},
		{name: "empty input", input: "", width: 10, want: []string{""}},
		{name: "multibyte", input: "héllo", width: 4, want: []string{"hé", "ll", "o"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := wrapInput(tc.input, tc.width)
			if len(got) != len(tc.want) {
				t.Fatalf("wrapInput(%q, %d) = %q, want %q", tc.input, tc.width, got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("wrapInput(%q, %d) = %q, want %q", tc.input, tc.width, got, tc.want)
				}
			}
		})
	}
}

func TestUnknownMessageTypesAreIgnored(t *testing.T) {
	type customMsg struct{ n int }
	m := chatModel()
	got, cmd := m.handleUpdate(customMsg{n: 1})
	if cmd != nil {
		t.Error("unknown message should not produce a command while the viewport is not ready")
	}
	if got.state != StateChat {
		t.Errorf("state changed to %v", got.state)
	}
	// nil messages reach Update from cancelled commands.
	if _, err := func() (Model, error) { g, _ := m.handleUpdate(nil); return g, nil }(); err != nil {
		t.Fatal(err)
	}
}

func TestWindowSizeInitialisesViewport(t *testing.T) {
	m := chatModel()
	m.messages = []ChatMessage{{Role: "assistant", Content: "# hi"}}

	got, _ := m.handleUpdate(tea.WindowSizeMsg{Width: 0, Height: 0})
	if !got.ready {
		t.Fatal("viewport should be initialised even for a zero-size window")
	}
	if got.viewport.Height < 1 {
		t.Errorf("viewport height = %d, want >= 1", got.viewport.Height)
	}
	// A second resize takes the already-ready branch.
	got, _ = got.handleUpdate(tea.WindowSizeMsg{Width: 100, Height: 40})
	if got.viewport.Width != 100 {
		t.Errorf("viewport width = %d, want 100", got.viewport.Width)
	}
	_ = got.View()
}

// An agents directory that exists but holds no usable base agent must still fall
// back to the embedded agents, or every prompt fails with
// "base agent not found in registry".
func TestInitLocalRuntimeFallsBackForUnusableAgentsDir(t *testing.T) {
	partial := t.TempDir()
	if err := os.WriteFile(filepath.Join(partial, "helper.yaml"),
		[]byte("id: helper\nname: Helper\nmodel: test/model\nsystem_prompt: hi\n"), 0o600); err != nil {
		t.Fatalf("seed agent: %v", err)
	}

	tests := []struct {
		name string
		dir  string
	}{
		{name: "empty dir", dir: t.TempDir()},
		{name: "dir without base agent", dir: partial},
		{name: "missing dir", dir: filepath.Join(t.TempDir(), "nope")},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("BUJICODER_AGENTS_DIR", tc.dir)

			msg := initLocalRuntimeFromConfig(&cliconfig.UnifiedConfig{
				Mode:  "local",
				Modes: map[string]cliconfig.UnifiedModeMapping{"normal": {Main: "test/model"}},
			})()
			initMsg, ok := msg.(runtimeInitMsg)
			if !ok {
				t.Fatalf("got %T, want runtimeInitMsg", msg)
			}
			if initMsg.err != nil {
				t.Fatalf("init error: %v", initMsg.err)
			}
			if initMsg.agentRegistry == nil {
				t.Fatal("agent registry is nil")
			}
			if _, ok := initMsg.agentRegistry.Get("base"); !ok {
				t.Error("base agent missing: embedded agents were not loaded as a fallback")
			}
			if initMsg.modelResolver == nil {
				t.Error("model resolver should be built from the inline modes")
			}
		})
	}
}

// The verbose sink is handed to the runtime as its OnEvent callback and is
// therefore called concurrently by parallel sub-agents. Run with -race.
func TestVerboseSinkConcurrentEvents(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("BUJICODER_CONFIG_DIR", dir)

	m := chatModel()
	if err := m.startVerboseLog(); err != nil {
		t.Fatalf("start verbose log: %v", err)
	}
	logPath := m.verbose.path()
	if logPath == "" {
		t.Fatal("verbose log path is empty")
	}
	if !strings.HasPrefix(logPath, filepath.Join(dir, "logs")) {
		t.Errorf("log path %q is not under the config dir", logPath)
	}

	agents := []string{"researcher", "editor", "reviewer", "planner"}
	var wg sync.WaitGroup
	for _, id := range agents {
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			for i := range 50 {
				m.verbose.logEvent(agentruntime.Event{Type: agentruntime.EventStepStart, AgentID: id, StepNumber: i})
				m.verbose.logEvent(agentruntime.Event{Type: agentruntime.EventDelta, AgentID: id, Text: "chunk "})
				m.verbose.logEvent(agentruntime.Event{Type: agentruntime.EventToolCall, AgentID: id, ToolName: "read_file"})
				m.verbose.logEvent(agentruntime.Event{Type: agentruntime.EventStepEnd, AgentID: id, StepNumber: i})
			}
			m.verbose.write("done %s", id)
		}(id)
	}
	wg.Wait()

	m.stopVerboseLog()
	if m.verbose != nil {
		t.Error("verbose sink should be nil after stop")
	}

	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	for _, id := range agents {
		if !strings.Contains(string(data), "agent:"+id) {
			t.Errorf("log is missing events for %s", id)
		}
	}
}

func TestVerboseSinkNilAndClosedAreSafe(t *testing.T) {
	var v *verboseSink
	v.write("no panic")
	v.logEvent(agentruntime.Event{Type: agentruntime.EventDelta, Text: "x"})
	v.close()
	if v.path() != "" {
		t.Error("nil sink path must be empty")
	}

	dir := t.TempDir()
	t.Setenv("BUJICODER_CONFIG_DIR", dir)
	real, err := newVerboseSink("normal")
	if err != nil {
		t.Fatalf("new sink: %v", err)
	}
	real.close()
	real.close() // double close must be a no-op
	real.write("after close")
	real.logEvent(agentruntime.Event{Type: agentruntime.EventStatus, Text: "after close"})
}

func TestVerboseToggleCommand(t *testing.T) {
	t.Setenv("BUJICODER_CONFIG_DIR", t.TempDir())

	m := chatModel()
	m.input = "/verbose"
	on, _ := m.handleUpdate(enterKey())
	if on.verbose == nil {
		t.Fatal("verbose logging should be enabled")
	}
	if !strings.Contains(lastContent(t, on), "enabled") {
		t.Errorf("unexpected notice: %s", lastContent(t, on))
	}

	on.input = "/verbose"
	off, _ := on.handleUpdate(enterKey())
	if off.verbose != nil {
		t.Fatal("verbose logging should be disabled")
	}
	if !strings.Contains(lastContent(t, off), "disabled") {
		t.Errorf("unexpected notice: %s", lastContent(t, off))
	}
}

// An agents directory that exists but holds no usable agent definitions must
// still fall back to the embedded agents, or every prompt fails with
// "base agent not found in registry".
func TestInitLocalRuntimeFallsBackForEmptyAgentsDir(t *testing.T) {
	emptyDir := t.TempDir()
	t.Setenv("BUJICODER_AGENTS_DIR", emptyDir)

	msg := initLocalRuntimeFromConfig(nil)()
	initMsg, ok := msg.(runtimeInitMsg)
	if !ok {
		t.Fatalf("got %T, want runtimeInitMsg", msg)
	}
	if initMsg.err != nil {
		t.Fatalf("init error: %v", initMsg.err)
	}
	if initMsg.agentRegistry == nil {
		t.Fatal("agent registry is nil")
	}
	if _, ok := initMsg.agentRegistry.Get("base"); !ok {
		t.Error("base agent missing: embedded agents were not loaded as a fallback")
	}
}

func TestExtractImagePartsLeavesNonImagesAlone(t *testing.T) {
	text, parts := extractImageParts("look at @notes.txt and @ and @missing.png")
	if len(parts) != 0 {
		t.Errorf("expected no image parts, got %d", len(parts))
	}
	for _, want := range []string{"@notes.txt", "@missing.png"} {
		if !strings.Contains(text, want) {
			t.Errorf("cleaned text %q lost %q", text, want)
		}
	}
}

func TestFormatTokenCount(t *testing.T) {
	tests := []struct {
		in   int
		want string
	}{
		{0, "0"}, {999, "999"}, {1000, "1.0K"}, {1500, "1.5K"}, {1_000_000, "1.0M"},
	}
	for _, tc := range tests {
		if got := formatTokenCount(tc.in); got != tc.want {
			t.Errorf("formatTokenCount(%d) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
