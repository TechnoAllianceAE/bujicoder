package localstore

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	t.Setenv("BUJICODER_CONFIG_DIR", t.TempDir())
	return NewStore()
}

func msg(role, content string) StoredMessage {
	return StoredMessage{Role: role, Content: content, CreatedAt: time.Now().UTC()}
}

func TestNewStoreHonoursConfigDir(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("BUJICODER_CONFIG_DIR", dir)

	s := NewStore()
	want := filepath.Join(dir, "conversations")
	if s.dir != want {
		t.Fatalf("store dir = %q, want %q", s.dir, want)
	}
	info, err := os.Stat(want)
	if err != nil {
		t.Fatalf("conversations dir not created: %v", err)
	}
	if !info.IsDir() {
		t.Fatal("conversations path is not a directory")
	}
}

func TestSaveAndGetMessages(t *testing.T) {
	s := newTestStore(t)

	want := []StoredMessage{msg("user", "hello"), msg("assistant", "hi")}
	if err := s.SaveConversation("conv-1", "greeting", want); err != nil {
		t.Fatalf("save: %v", err)
	}

	got, err := s.GetMessages("conv-1")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if len(got) != 2 || got[0].Content != "hello" || got[1].Content != "hi" {
		t.Fatalf("messages round-trip mismatch: %+v", got)
	}

	// Conversation files carry user data and must not be world-readable.
	info, err := os.Stat(filepath.Join(s.dir, "conv-1.json"))
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("conversation permissions = %#o, want 0600", perm)
	}
}

func TestAppendMessagesCreatesThenAppends(t *testing.T) {
	s := newTestStore(t)

	if err := s.AppendMessages("conv-1", "first title", msg("user", "one")); err != nil {
		t.Fatalf("append 1: %v", err)
	}
	if err := s.AppendMessages("conv-1", "later title", msg("assistant", "two")); err != nil {
		t.Fatalf("append 2: %v", err)
	}

	got, err := s.GetMessages("conv-1")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d messages, want 2 (append must not replace history)", len(got))
	}

	summaries, err := s.ListConversations(0, 0)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(summaries) != 1 || summaries[0].Title != "first title" {
		t.Errorf("title should stay the original: %+v", summaries)
	}
}

func TestAppendMessagesDoesNotDiscardCorruptHistory(t *testing.T) {
	s := newTestStore(t)

	if err := s.AppendMessages("conv-1", "t", msg("user", "keep me")); err != nil {
		t.Fatalf("append: %v", err)
	}
	path := filepath.Join(s.dir, "conv-1.json")
	// Simulate a partially written / corrupted file.
	if err := os.WriteFile(path, []byte(`{"id":"conv-1","messages":[{"role":`), 0o600); err != nil {
		t.Fatalf("corrupt: %v", err)
	}

	err := s.AppendMessages("conv-1", "t", msg("assistant", "new"))
	if err == nil {
		t.Fatal("appending to an unreadable conversation must fail loudly, not silently replace it")
	}

	// The corrupt bytes must still be on disk for recovery, not overwritten.
	data, readErr := os.ReadFile(path)
	if readErr != nil {
		t.Fatalf("read back: %v", readErr)
	}
	var conv ConversationFile
	if json.Unmarshal(data, &conv) == nil {
		t.Fatal("the corrupt file was overwritten with a fresh conversation")
	}
}

func TestListConversationsOrderingAndPaging(t *testing.T) {
	s := newTestStore(t)

	for i, id := range []string{"old", "mid", "new"} {
		if err := s.SaveConversation(id, id, []StoredMessage{msg("user", id)}); err != nil {
			t.Fatalf("save %s: %v", id, err)
		}
		// Force distinct updated_at values (RFC3339 has second resolution).
		stamp := time.Now().UTC().Add(time.Duration(i-3) * time.Hour)
		conv, err := s.readConv(id)
		if err != nil {
			t.Fatalf("read %s: %v", id, err)
		}
		conv.UpdatedAt = stamp
		if err := s.writeConv(conv); err != nil {
			t.Fatalf("rewrite %s: %v", id, err)
		}
	}

	all, err := s.ListConversations(0, 0)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(all) != 3 {
		t.Fatalf("got %d conversations, want 3", len(all))
	}
	if all[0].ID != "new" || all[2].ID != "old" {
		t.Errorf("expected updated_at DESC ordering, got %v %v %v", all[0].ID, all[1].ID, all[2].ID)
	}

	tests := []struct {
		name          string
		limit, offset int
		wantLen       int
	}{
		{name: "limit", limit: 2, wantLen: 2},
		{name: "offset", offset: 1, wantLen: 2},
		{name: "limit and offset", limit: 1, offset: 1, wantLen: 1},
		{name: "offset past end", offset: 99, wantLen: 0},
		{name: "negative offset treated as zero", offset: -5, wantLen: 3},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := s.ListConversations(tc.limit, tc.offset)
			if err != nil {
				t.Fatalf("list: %v", err)
			}
			if len(got) != tc.wantLen {
				t.Errorf("got %d summaries, want %d", len(got), tc.wantLen)
			}
		})
	}
}

func TestListConversationsMissingDir(t *testing.T) {
	s := newTestStore(t)
	if err := os.RemoveAll(s.dir); err != nil {
		t.Fatalf("remove: %v", err)
	}
	got, err := s.ListConversations(10, 0)
	if err != nil {
		t.Fatalf("missing dir must not be an error: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("got %d summaries, want 0", len(got))
	}
}

func TestConversationIDValidation(t *testing.T) {
	s := newTestStore(t)

	// A file outside the conversations dir that must never be touched.
	outside := filepath.Join(filepath.Dir(s.dir), "secret.json")
	if err := os.WriteFile(outside, []byte("{}"), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}

	for _, id := range []string{"", "..", "../secret", "sub/dir", `back\slash`, "a/../../b"} {
		t.Run("id="+id, func(t *testing.T) {
			if err := s.SaveConversation(id, "t", []StoredMessage{msg("user", "x")}); err == nil {
				t.Errorf("SaveConversation(%q) must be rejected", id)
			}
			if _, err := s.GetMessages(id); err == nil {
				t.Errorf("GetMessages(%q) must be rejected", id)
			}
			if err := s.DeleteConversation(id); err == nil {
				t.Errorf("DeleteConversation(%q) must be rejected", id)
			}
		})
	}

	if _, err := os.Stat(outside); err != nil {
		t.Errorf("file outside the store directory was affected: %v", err)
	}
}

func TestDeleteConversation(t *testing.T) {
	s := newTestStore(t)
	if err := s.SaveConversation("conv-1", "t", []StoredMessage{msg("user", "x")}); err != nil {
		t.Fatalf("save: %v", err)
	}
	if err := s.DeleteConversation("conv-1"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := s.GetMessages("conv-1"); !os.IsNotExist(err) {
		t.Errorf("expected not-exist after delete, got %v", err)
	}
}

func TestConcurrentAppendsKeepEveryMessage(t *testing.T) {
	s := newTestStore(t)

	const writers = 8
	var wg sync.WaitGroup
	errs := make([]error, writers)
	wg.Add(writers)
	for i := range writers {
		go func(i int) {
			defer wg.Done()
			errs[i] = s.AppendMessages("conv-1", "concurrent", msg("user", "m"))
		}(i)
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("writer %d: %v", i, err)
		}
	}

	got, err := s.GetMessages("conv-1")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if len(got) != writers {
		t.Errorf("got %d messages, want %d (concurrent appends lost data)", len(got), writers)
	}
}

func TestWriteConvLeavesNoTempFiles(t *testing.T) {
	s := newTestStore(t)
	if err := s.SaveConversation("conv-1", "t", []StoredMessage{msg("user", "x")}); err != nil {
		t.Fatalf("save: %v", err)
	}
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	if len(entries) != 1 || entries[0].Name() != "conv-1.json" {
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Errorf("directory contents = %v, want only conv-1.json", names)
	}
}
