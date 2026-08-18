// Package localstore provides JSON file-based conversation persistence for standalone mode.
package localstore

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/TechnoAllianceAE/bujicoder/cli/config"
)

// Store manages local conversation files in ~/.bujicoder/conversations/.
//
// A Store is safe for concurrent use: conversation files are updated with a
// read-modify-write cycle, so the mutex is what keeps two concurrent appends
// from dropping each other's messages.
type Store struct {
	mu  sync.Mutex
	dir string
}

// ConversationFile is the on-disk JSON structure for a conversation.
type ConversationFile struct {
	ID        string          `json:"id"`
	Title     string          `json:"title"`
	CreatedAt time.Time       `json:"created_at"`
	UpdatedAt time.Time       `json:"updated_at"`
	Messages  []StoredMessage `json:"messages"`
}

// StoredMessage is a single message persisted to disk.
type StoredMessage struct {
	Role      string    `json:"role"`
	Content   string    `json:"content"`
	CreatedAt time.Time `json:"created_at"`
}

// ConversationSummary is returned by ListConversations (no message bodies).
type ConversationSummary struct {
	ID        string `json:"id"`
	Title     string `json:"title"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
}

// NewStore creates a Store using <config dir>/conversations/ as the storage
// directory, honouring BUJICODER_CONFIG_DIR like the rest of the CLI.
func NewStore() *Store {
	dir := filepath.Join(config.Dir(), "conversations")
	_ = os.MkdirAll(dir, 0o700)
	return &Store{dir: dir}
}

// convPath validates the conversation ID and returns its file path.
// IDs are used as filenames, so anything that could escape the store directory
// (path separators, "..", empty) is rejected rather than written outside it.
func (s *Store) convPath(id string) (string, error) {
	if id == "" {
		return "", fmt.Errorf("invalid conversation id: empty")
	}
	if id == "." || id == ".." || strings.ContainsAny(id, `/\`) || strings.Contains(id, "..") {
		return "", fmt.Errorf("invalid conversation id: %q", id)
	}
	return filepath.Join(s.dir, id+".json"), nil
}

// SaveConversation writes a full conversation file.
func (s *Store) SaveConversation(id, title string, msgs []StoredMessage) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now().UTC()
	conv := ConversationFile{
		ID:        id,
		Title:     title,
		CreatedAt: now,
		UpdatedAt: now,
		Messages:  msgs,
	}
	return s.writeConv(&conv)
}

// AppendMessages appends messages to an existing conversation.
// Creates the conversation file if it doesn't exist.
func (s *Store) AppendMessages(id, title string, msgs ...StoredMessage) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	conv, err := s.readConv(id)
	if err != nil {
		// Only a missing file means "new conversation". Any other failure
		// (corrupt JSON, permissions) must not silently replace the existing
		// history with just these messages.
		if !os.IsNotExist(err) {
			return fmt.Errorf("read conversation %s: %w", id, err)
		}
		now := time.Now().UTC()
		conv = &ConversationFile{
			ID:        id,
			Title:     title,
			CreatedAt: now,
			UpdatedAt: now,
		}
	}
	conv.Messages = append(conv.Messages, msgs...)
	conv.UpdatedAt = time.Now().UTC()
	if conv.Title == "" && title != "" {
		conv.Title = title
	}
	return s.writeConv(conv)
}

// ListConversations returns conversation summaries sorted by updated_at DESC.
func (s *Store) ListConversations(limit, offset int) ([]ConversationSummary, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	entries, err := os.ReadDir(s.dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	var summaries []ConversationSummary
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		conv, err := s.readConv(strings.TrimSuffix(entry.Name(), ".json"))
		if err != nil {
			continue
		}
		summaries = append(summaries, ConversationSummary{
			ID:        conv.ID,
			Title:     conv.Title,
			CreatedAt: conv.CreatedAt.Format(time.RFC3339),
			UpdatedAt: conv.UpdatedAt.Format(time.RFC3339),
		})
	}

	sort.Slice(summaries, func(i, j int) bool {
		return summaries[i].UpdatedAt > summaries[j].UpdatedAt
	})

	// Apply offset.
	if offset < 0 {
		offset = 0
	}
	if offset > 0 {
		if offset >= len(summaries) {
			return nil, nil
		}
		summaries = summaries[offset:]
	}
	// Apply limit.
	if limit > 0 && limit < len(summaries) {
		summaries = summaries[:limit]
	}
	return summaries, nil
}

// GetMessages returns all messages for a conversation.
func (s *Store) GetMessages(id string) ([]StoredMessage, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	conv, err := s.readConv(id)
	if err != nil {
		return nil, err
	}
	return conv.Messages, nil
}

// DeleteConversation removes a conversation file.
func (s *Store) DeleteConversation(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	path, err := s.convPath(id)
	if err != nil {
		return err
	}
	return os.Remove(path)
}

func (s *Store) readConv(id string) (*ConversationFile, error) {
	path, err := s.convPath(id)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var conv ConversationFile
	if err := json.Unmarshal(data, &conv); err != nil {
		return nil, fmt.Errorf("parse conversation %s: %w", path, err)
	}
	return &conv, nil
}

// writeConv persists a conversation via a temp file + rename, so an interrupted
// write can never leave a truncated conversation behind.
func (s *Store) writeConv(conv *ConversationFile) error {
	path, err := s.convPath(conv.ID)
	if err != nil {
		return err
	}
	data, err := json.MarshalIndent(conv, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(s.dir, 0o700); err != nil {
		return err
	}
	f, err := os.CreateTemp(s.dir, "."+conv.ID+".tmp")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer func() { _ = os.Remove(tmp) }() // no-op once renamed
	if err := f.Chmod(0o600); err != nil {
		f.Close()
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
