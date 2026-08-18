package store

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	bolt "go.etcd.io/bbolt"
)

// TestSearchIndexNotPollutedByRolledBackTx checks that Bleve is only mutated
// after the bbolt transaction commits. Indexing inside the transaction left the
// index advertising messages that the rolled-back transaction never wrote.
//
// The rollback is triggered by a timestamp outside the range time.Time can
// marshal to JSON, which fails only after the earlier messages of the same call
// have already been written and indexed.
func TestSearchIndexNotPollutedByRolledBackTx(t *testing.T) {
	s := openTestStore(t)

	good := StoredMessage{Role: "user", Content: "zzuniquetokenzz", CreatedAt: time.Now().UTC()}
	unmarshalable := StoredMessage{Role: "user", Content: "second", CreatedAt: time.Date(12345, 1, 1, 0, 0, 0, 0, time.UTC)}

	if _, err := json.Marshal(unmarshalable); err == nil {
		t.Fatal("test premise broken: the timestamp is marshalable")
	}

	if err := s.AppendMessages("rollback", "Rollback", good, unmarshalable); err == nil {
		t.Fatal("AppendMessages should fail on an unmarshalable message")
	}

	// The transaction rolled back, so no message exists...
	msgs, err := s.GetMessages("rollback")
	if err != nil {
		t.Fatalf("GetMessages: %v", err)
	}
	if len(msgs) != 0 {
		t.Fatalf("expected no committed messages, got %d", len(msgs))
	}
	// ...and search must not report one either.
	hits, err := s.SearchMessages("zzuniquetokenzz", 10)
	if err != nil {
		t.Fatalf("SearchMessages: %v", err)
	}
	if len(hits) != 0 {
		t.Fatalf("search index references uncommitted message: %+v", hits)
	}
}

// TestForkConversationUniqueIDs checks that rapid forks get distinct IDs. The
// previous time-based generator returned the same string for two calls inside
// one clock tick, so the second fork silently overwrote the first.
func TestForkConversationUniqueIDs(t *testing.T) {
	s := openTestStore(t)

	if err := s.SaveConversation("src", "Source", []StoredMessage{
		{Role: "user", Content: "one", CreatedAt: time.Now().UTC()},
		{Role: "assistant", Content: "two", CreatedAt: time.Now().UTC()},
	}); err != nil {
		t.Fatalf("SaveConversation: %v", err)
	}

	seen := make(map[string]bool)
	for i := range 50 {
		id, err := s.ForkConversation("src", 1)
		if err != nil {
			t.Fatalf("ForkConversation: %v", err)
		}
		if seen[id] {
			t.Fatalf("duplicate fork id %q on iteration %d", id, i)
		}
		seen[id] = true
	}

	list, err := s.ListConversations(0, 0)
	if err != nil {
		t.Fatalf("ListConversations: %v", err)
	}
	if len(list) != 51 {
		t.Fatalf("expected 51 conversations (1 source + 50 forks), got %d", len(list))
	}
}

// TestForkConversationErrorReturnsNoID guards against handing the caller an ID
// for a fork that was never created.
func TestForkConversationErrorReturnsNoID(t *testing.T) {
	s := openTestStore(t)

	id, err := s.ForkConversation("does-not-exist", 5)
	if err == nil {
		t.Fatal("expected error forking a missing conversation")
	}
	if id != "" {
		t.Fatalf("got id %q for a failed fork, want empty", id)
	}
}

func TestForkConversationCopiesMessages(t *testing.T) {
	s := openTestStore(t)

	msgs := []StoredMessage{
		{Role: "user", Content: "a", CreatedAt: time.Now().UTC()},
		{Role: "assistant", Content: "b", CreatedAt: time.Now().UTC()},
		{Role: "user", Content: "c", CreatedAt: time.Now().UTC()},
	}
	if err := s.SaveConversation("src", "Source", msgs); err != nil {
		t.Fatalf("SaveConversation: %v", err)
	}

	for _, tc := range []struct {
		name  string
		at    int
		wantN int
	}{
		{"first only", 0, 1},
		{"through second", 1, 2},
		{"all", 10, 3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			id, err := s.ForkConversation("src", tc.at)
			if err != nil {
				t.Fatalf("ForkConversation: %v", err)
			}
			got, err := s.GetMessages(id)
			if err != nil {
				t.Fatalf("GetMessages: %v", err)
			}
			if len(got) != tc.wantN {
				t.Fatalf("got %d messages, want %d", len(got), tc.wantN)
			}
			for i := range got {
				if got[i].Content != msgs[i].Content {
					t.Errorf("message %d = %q, want %q", i, got[i].Content, msgs[i].Content)
				}
			}
		})
	}
}

// TestOpenRefusesNewerSchema protects a database written by a newer build from
// being opened — and then written to — by an older one.
func TestOpenRefusesNewerSchema(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")
	idxPath := filepath.Join(dir, "test.bleve")

	s, err := Open(dbPath, idxPath)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := s.db.Update(func(tx *bolt.Tx) error {
		return tx.Bucket(bucketMetadata).Put(keySchemaVersion, []byte(fmt.Sprint(schemaVersion+1)))
	}); err != nil {
		t.Fatalf("bump version: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	if _, err := Open(dbPath, idxPath); err == nil {
		t.Fatal("Open accepted a database written by a newer schema version")
	} else if !strings.Contains(err.Error(), "newer than supported") {
		t.Fatalf("unexpected error: %v", err)
	}
}

// TestOpenConcurrentFailsFast checks that a second Open on a locked database
// reports an error instead of hanging forever on the bbolt flock.
func TestOpenConcurrentFailsFast(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")

	first, err := Open(dbPath, filepath.Join(dir, "a.bleve"))
	if err != nil {
		t.Fatalf("first Open: %v", err)
	}
	defer first.Close()

	done := make(chan error, 1)
	go func() {
		second, err := Open(dbPath, filepath.Join(dir, "b.bleve"))
		if err == nil {
			second.Close()
		}
		done <- err
	}()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("second Open on a locked database should fail")
		}
	case <-time.After(30 * time.Second):
		t.Fatal("second Open hung instead of timing out")
	}
}

// TestMigrateFromJSONIsIdempotent is the data-loss guard: re-running migration
// must never replay stale JSON over conversations that have since been updated.
func TestMigrateFromJSONIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	jsonDir := filepath.Join(dir, "conversations")
	if err := os.MkdirAll(jsonDir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	legacy := jsonConversationFile{
		ID:    "conv-1",
		Title: "Legacy",
		Messages: []StoredMessage{
			{Role: "user", Content: "old message", CreatedAt: time.Now().UTC()},
		},
	}
	data, err := json.Marshal(legacy)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := os.WriteFile(filepath.Join(jsonDir, "conv-1.json"), data, 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	s := openTestStore(t)
	if err := MigrateFromJSON(jsonDir, s); err != nil {
		t.Fatalf("first migration: %v", err)
	}

	// Simulate the rename having failed: the legacy directory is still present.
	if err := os.Rename(jsonDir+".bak", jsonDir); err != nil {
		t.Fatalf("restore legacy dir: %v", err)
	}

	// The user keeps chatting after the migration.
	if err := s.AppendMessages("conv-1", "Legacy", StoredMessage{
		Role: "assistant", Content: "new message", CreatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("AppendMessages: %v", err)
	}

	// Next start migrates again — it must be a no-op.
	if err := MigrateFromJSON(jsonDir, s); err != nil {
		t.Fatalf("second migration: %v", err)
	}

	msgs, err := s.GetMessages("conv-1")
	if err != nil {
		t.Fatalf("GetMessages: %v", err)
	}
	if len(msgs) != 2 {
		t.Fatalf("re-migration clobbered live data: got %d messages, want 2", len(msgs))
	}
	if msgs[1].Content != "new message" {
		t.Errorf("msgs[1] = %q, want %q", msgs[1].Content, "new message")
	}
}

// TestMigrateFromJSONKeepsRecordWithoutID guards against dropping a legacy file
// whose "id" field is missing: it used to be stored under an empty key.
func TestMigrateFromJSONKeepsRecordWithoutID(t *testing.T) {
	dir := t.TempDir()
	jsonDir := filepath.Join(dir, "conversations")
	if err := os.MkdirAll(jsonDir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	body := `{"title":"No ID","messages":[{"role":"user","content":"kept"}]}`
	if err := os.WriteFile(filepath.Join(jsonDir, "orphan.json"), []byte(body), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	s := openTestStore(t)
	if err := MigrateFromJSON(jsonDir, s); err != nil {
		t.Fatalf("MigrateFromJSON: %v", err)
	}

	msgs, err := s.GetMessages("orphan")
	if err != nil {
		t.Fatalf("GetMessages: %v", err)
	}
	if len(msgs) != 1 || msgs[0].Content != "kept" {
		t.Fatalf("record without id was not migrated under its file name: %+v", msgs)
	}
}

// TestSaveConversationRejectsEmptyID keeps an unkeyable record out of the store.
func TestSaveConversationRejectsEmptyID(t *testing.T) {
	s := openTestStore(t)
	if err := s.SaveConversation("", "No ID", nil); err == nil {
		t.Fatal("SaveConversation accepted an empty id")
	}
	if err := s.AppendMessages("", "No ID", StoredMessage{Role: "user", Content: "x"}); err == nil {
		t.Fatal("AppendMessages accepted an empty id")
	}
}

// TestSearchSnippetIsValidUTF8 checks that a long multi-byte message is not cut
// mid-rune when building the result snippet.
func TestSearchSnippetIsValidUTF8(t *testing.T) {
	s := openTestStore(t)

	content := strings.Repeat("日", 400) + " marker"
	if err := s.SaveConversation("uni", "Unicode", []StoredMessage{
		{Role: "user", Content: content, CreatedAt: time.Now().UTC()},
	}); err != nil {
		t.Fatalf("SaveConversation: %v", err)
	}

	hits, err := s.SearchMessages("marker", 5)
	if err != nil {
		t.Fatalf("SearchMessages: %v", err)
	}
	if len(hits) == 0 {
		t.Skip("bleve returned no hits for the tokenized query")
	}
	for _, h := range hits {
		if !utf8ValidString(h.Snippet) {
			t.Fatalf("snippet is not valid UTF-8: %q", h.Snippet)
		}
	}
}

func utf8ValidString(s string) bool {
	for _, r := range s {
		if r == '\uFFFD' {
			return false
		}
	}
	return true
}

// TestConcurrentAppendMessages exercises the store from several goroutines to
// catch races and lost writes.
func TestConcurrentAppendMessages(t *testing.T) {
	s := openTestStore(t)

	const writers = 8
	const each = 10

	var wg sync.WaitGroup
	wg.Add(writers)
	for w := range writers {
		go func(w int) {
			defer wg.Done()
			id := fmt.Sprintf("conv-%d", w)
			for i := range each {
				if err := s.AppendMessages(id, "Concurrent", StoredMessage{
					Role:      "user",
					Content:   fmt.Sprintf("w%d-m%d", w, i),
					CreatedAt: time.Now().UTC(),
				}); err != nil {
					t.Errorf("AppendMessages: %v", err)
					return
				}
			}
		}(w)
	}
	wg.Wait()

	for w := range writers {
		msgs, err := s.GetMessages(fmt.Sprintf("conv-%d", w))
		if err != nil {
			t.Fatalf("GetMessages: %v", err)
		}
		if len(msgs) != each {
			t.Errorf("conv-%d has %d messages, want %d", w, len(msgs), each)
		}
	}
}

func TestParseMsgKeyTable(t *testing.T) {
	tests := []struct {
		key      string
		wantConv string
		wantSeq  int
	}{
		{"conv/00000003", "conv", 3},
		{"conv/with/slash/00000001", "conv/with/slash", 1},
		{"nokey", "", 0},
		{"conv/notanumber", "", 0},
		{"conv/", "", 0},
	}
	for _, tc := range tests {
		t.Run(tc.key, func(t *testing.T) {
			conv, seq := parseMsgKey(tc.key)
			if conv != tc.wantConv || seq != tc.wantSeq {
				t.Errorf("parseMsgKey(%q) = (%q, %d), want (%q, %d)", tc.key, conv, seq, tc.wantConv, tc.wantSeq)
			}
		})
	}
}

func TestTruncateRunes(t *testing.T) {
	tests := []struct {
		name string
		in   string
		max  int
		want string
	}{
		{"short", "abc", 10, "abc"},
		{"exact", "abcde", 5, "abcde"},
		{"cut ascii", "abcdef", 3, "abc..."},
		{"cut multibyte", "日本語です", 2, "日本..."},
		{"zero", "abc", 0, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := truncateRunes(tc.in, tc.max); got != tc.want {
				t.Errorf("truncateRunes(%q, %d) = %q, want %q", tc.in, tc.max, got, tc.want)
			}
		})
	}
}
