package store

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func openTestStore(t *testing.T) *Store {
	t.Helper()
	dir := t.TempDir()
	s, err := Open(filepath.Join(dir, "test.db"), filepath.Join(dir, "test.bleve"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestOpenAndClose(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(filepath.Join(dir, "test.db"), filepath.Join(dir, "test.bleve"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

func TestSaveAndGetMessages(t *testing.T) {
	s := openTestStore(t)

	msgs := []StoredMessage{
		{Role: "user", Content: "Hello", CreatedAt: time.Now().UTC()},
		{Role: "assistant", Content: "Hi there!", CreatedAt: time.Now().UTC()},
	}

	if err := s.SaveConversation("conv-1", "Test Chat", msgs); err != nil {
		t.Fatalf("SaveConversation: %v", err)
	}

	got, err := s.GetMessages("conv-1")
	if err != nil {
		t.Fatalf("GetMessages: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d messages, want 2", len(got))
	}
	if got[0].Role != "user" || got[0].Content != "Hello" {
		t.Errorf("msg[0] = %+v, want user/Hello", got[0])
	}
	if got[1].Role != "assistant" || got[1].Content != "Hi there!" {
		t.Errorf("msg[1] = %+v, want assistant/Hi there!", got[1])
	}
}

func TestAppendMessages(t *testing.T) {
	s := openTestStore(t)

	// First append creates the conversation.
	if err := s.AppendMessages("conv-2", "Chat Two",
		StoredMessage{Role: "user", Content: "msg1", CreatedAt: time.Now().UTC()},
	); err != nil {
		t.Fatalf("first append: %v", err)
	}

	// Second append adds to it.
	if err := s.AppendMessages("conv-2", "",
		StoredMessage{Role: "assistant", Content: "msg2", CreatedAt: time.Now().UTC()},
		StoredMessage{Role: "user", Content: "msg3", CreatedAt: time.Now().UTC()},
	); err != nil {
		t.Fatalf("second append: %v", err)
	}

	got, err := s.GetMessages("conv-2")
	if err != nil {
		t.Fatalf("GetMessages: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("got %d messages, want 3", len(got))
	}
	if got[2].Content != "msg3" {
		t.Errorf("msg[2].Content = %q, want msg3", got[2].Content)
	}
}

func TestListConversations(t *testing.T) {
	s := openTestStore(t)

	// Create two conversations with different timestamps.
	s.SaveConversation("old", "Old Chat", []StoredMessage{
		{Role: "user", Content: "old", CreatedAt: time.Now().UTC()},
	})
	time.Sleep(10 * time.Millisecond) // ensure different updated_at
	s.SaveConversation("new", "New Chat", []StoredMessage{
		{Role: "user", Content: "new", CreatedAt: time.Now().UTC()},
	})

	list, err := s.ListConversations(10, 0)
	if err != nil {
		t.Fatalf("ListConversations: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("got %d conversations, want 2", len(list))
	}
	// Most recent first.
	if list[0].ID != "new" {
		t.Errorf("list[0].ID = %q, want new", list[0].ID)
	}
	if list[1].ID != "old" {
		t.Errorf("list[1].ID = %q, want old", list[1].ID)
	}
}

func TestListConversationsLimitOffset(t *testing.T) {
	s := openTestStore(t)

	for i := 0; i < 5; i++ {
		id := string(rune('a'+i)) + "-conv"
		s.SaveConversation(id, "Chat", []StoredMessage{
			{Role: "user", Content: "hi", CreatedAt: time.Now().UTC()},
		})
		time.Sleep(5 * time.Millisecond)
	}

	// Limit to 2.
	list, err := s.ListConversations(2, 0)
	if err != nil {
		t.Fatalf("ListConversations: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("got %d, want 2", len(list))
	}

	// Offset 3.
	list, err = s.ListConversations(10, 3)
	if err != nil {
		t.Fatalf("ListConversations: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("got %d, want 2", len(list))
	}
}

func TestDeleteConversation(t *testing.T) {
	s := openTestStore(t)

	s.SaveConversation("del-me", "Delete Me", []StoredMessage{
		{Role: "user", Content: "bye", CreatedAt: time.Now().UTC()},
	})

	if err := s.DeleteConversation("del-me"); err != nil {
		t.Fatalf("DeleteConversation: %v", err)
	}

	msgs, err := s.GetMessages("del-me")
	if err != nil {
		t.Fatalf("GetMessages: %v", err)
	}
	if len(msgs) != 0 {
		t.Errorf("got %d messages after delete, want 0", len(msgs))
	}

	list, _ := s.ListConversations(10, 0)
	if len(list) != 0 {
		t.Errorf("got %d conversations after delete, want 0", len(list))
	}
}

func TestSearchMessages(t *testing.T) {
	s := openTestStore(t)

	s.SaveConversation("search-conv", "Search Test", []StoredMessage{
		{Role: "user", Content: "Tell me about quantum computing", CreatedAt: time.Now().UTC()},
		{Role: "assistant", Content: "Quantum computing uses qubits for parallel processing", CreatedAt: time.Now().UTC()},
	})

	// Give Bleve a moment to index.
	time.Sleep(100 * time.Millisecond)

	results, err := s.SearchMessages("quantum", 10)
	if err != nil {
		t.Fatalf("SearchMessages: %v", err)
	}
	if len(results) == 0 {
		t.Fatal("expected at least one search result")
	}
	if results[0].ConversationID != "search-conv" {
		t.Errorf("result convID = %q, want search-conv", results[0].ConversationID)
	}
}

func TestForkConversation(t *testing.T) {
	s := openTestStore(t)

	s.SaveConversation("original", "Original", []StoredMessage{
		{Role: "user", Content: "step 0", CreatedAt: time.Now().UTC()},
		{Role: "assistant", Content: "step 1", CreatedAt: time.Now().UTC()},
		{Role: "user", Content: "step 2", CreatedAt: time.Now().UTC()},
		{Role: "assistant", Content: "step 3", CreatedAt: time.Now().UTC()},
	})

	// Fork at message 1 (should include messages 0 and 1).
	newID, err := s.ForkConversation("original", 1)
	if err != nil {
		t.Fatalf("ForkConversation: %v", err)
	}

	forkedMsgs, err := s.GetMessages(newID)
	if err != nil {
		t.Fatalf("GetMessages(forked): %v", err)
	}
	if len(forkedMsgs) != 2 {
		t.Fatalf("forked has %d messages, want 2", len(forkedMsgs))
	}
	if forkedMsgs[0].Content != "step 0" {
		t.Errorf("forked[0] = %q, want step 0", forkedMsgs[0].Content)
	}
	if forkedMsgs[1].Content != "step 1" {
		t.Errorf("forked[1] = %q, want step 1", forkedMsgs[1].Content)
	}

	// Original should be unchanged.
	origMsgs, _ := s.GetMessages("original")
	if len(origMsgs) != 4 {
		t.Errorf("original has %d messages, want 4", len(origMsgs))
	}
}

func TestUpdateCost(t *testing.T) {
	s := openTestStore(t)

	s.SaveConversation("cost-conv", "Cost Test", nil)

	if err := s.UpdateCost("cost-conv", 42.5); err != nil {
		t.Fatalf("UpdateCost: %v", err)
	}

	// Verify by listing (cost not in summary but we can verify no error).
	list, _ := s.ListConversations(10, 0)
	if len(list) != 1 || list[0].ID != "cost-conv" {
		t.Errorf("unexpected list after cost update: %v", list)
	}
}

func TestMigrateFromJSON(t *testing.T) {
	dir := t.TempDir()
	jsonDir := filepath.Join(dir, "conversations")
	os.MkdirAll(jsonDir, 0o700)

	// Write a fake JSON conversation.
	conv := jsonConversationFile{
		ID:        "migrated-1",
		Title:     "Migrated Chat",
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
		Messages: []jsonStoredMessage{
			{Role: "user", Content: "hello from json", CreatedAt: time.Now().UTC()},
			{Role: "assistant", Content: "migrated response", CreatedAt: time.Now().UTC()},
		},
	}
	data, _ := json.MarshalIndent(conv, "", "  ")
	os.WriteFile(filepath.Join(jsonDir, "migrated-1.json"), data, 0o600)

	// Open store and migrate.
	s, err := Open(filepath.Join(dir, "test.db"), filepath.Join(dir, "test.bleve"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()

	if err := MigrateFromJSON(jsonDir, s); err != nil {
		t.Fatalf("MigrateFromJSON: %v", err)
	}

	// Verify migration.
	msgs, err := s.GetMessages("migrated-1")
	if err != nil {
		t.Fatalf("GetMessages: %v", err)
	}
	if len(msgs) != 2 {
		t.Fatalf("got %d messages, want 2", len(msgs))
	}
	if msgs[0].Content != "hello from json" {
		t.Errorf("msg[0] = %q, want 'hello from json'", msgs[0].Content)
	}

	// JSON dir should be renamed to .bak.
	if _, err := os.Stat(jsonDir); !os.IsNotExist(err) {
		t.Error("json dir should be renamed after migration")
	}
	if _, err := os.Stat(jsonDir + ".bak"); os.IsNotExist(err) {
		t.Error("json .bak dir should exist after migration")
	}
}

func TestNeedsMigration(t *testing.T) {
	dir := t.TempDir()
	jsonDir := filepath.Join(dir, "conversations")
	dbPath := filepath.Join(dir, "test.db")

	// Neither exists — no migration needed.
	if NeedsMigration(jsonDir, dbPath) {
		t.Error("should not need migration when neither exists")
	}

	// JSON exists, DB doesn't — needs migration.
	os.MkdirAll(jsonDir, 0o700)
	if !NeedsMigration(jsonDir, dbPath) {
		t.Error("should need migration when json exists and db doesn't")
	}

	// Both exist — no migration needed.
	os.WriteFile(dbPath, []byte("x"), 0o600)
	if NeedsMigration(jsonDir, dbPath) {
		t.Error("should not need migration when both exist")
	}
}

func TestParseMsgKey(t *testing.T) {
	convID, seq := parseMsgKey("abc-123/00000005")
	if convID != "abc-123" || seq != 5 {
		t.Errorf("parseMsgKey = %q, %d; want abc-123, 5", convID, seq)
	}
}
