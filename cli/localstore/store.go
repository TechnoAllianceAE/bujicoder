// Package localstore provides JSON file-based conversation persistence for standalone mode.
package localstore

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"time"
)

// Store manages local conversation files in ~/.bujicoder/conversations/.
type Store struct {
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

// NewStore creates a Store using ~/.bujicoder/conversations/ as the storage directory.
func NewStore() *Store {
	home, _ := os.UserHomeDir()
	dir := filepath.Join(home, ".bujicoder", "conversations")
	_ = os.MkdirAll(dir, 0o700)
	return &Store{dir: dir}
}

// SaveConversation writes a full conversation file.
func (s *Store) SaveConversation(id, title string, msgs []StoredMessage) error {
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
	conv, err := s.readConv(id)
	if err != nil {
		// File doesn't exist — create new.
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
		conv, err := s.readConv(entry.Name()[:len(entry.Name())-5])
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
	conv, err := s.readConv(id)
	if err != nil {
		return nil, err
	}
	return conv.Messages, nil
}

// DeleteConversation removes a conversation file.
func (s *Store) DeleteConversation(id string) error {
	path := filepath.Join(s.dir, id+".json")
	return os.Remove(path)
}

func (s *Store) readConv(id string) (*ConversationFile, error) {
	path := filepath.Join(s.dir, id+".json")
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var conv ConversationFile
	if err := json.Unmarshal(data, &conv); err != nil {
		return nil, err
	}
	return &conv, nil
}

func (s *Store) writeConv(conv *ConversationFile) error {
	data, err := json.MarshalIndent(conv, "", "  ")
	if err != nil {
		return err
	}
	_ = os.MkdirAll(s.dir, 0o700)
	return os.WriteFile(filepath.Join(s.dir, conv.ID+".json"), data, 0o600)
}
