// Package store provides transactional conversation persistence using bbolt + Bleve,
// replacing the JSON-file-based localstore.
package store

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/blevesearch/bleve/v2"
	bolt "go.etcd.io/bbolt"
)

// Bucket names in bbolt.
var (
	bucketConversations = []byte("conversations")
	bucketMessages      = []byte("messages")
	bucketMetadata      = []byte("metadata")
)

// Store is the primary persistence layer backed by bbolt + Bleve.
type Store struct {
	db    *bolt.DB
	index bleve.Index
}

// Conversation is the metadata for a stored conversation (no messages).
type Conversation struct {
	ID        string  `json:"id"`
	Title     string  `json:"title"`
	CreatedAt string  `json:"created_at"` // RFC3339
	UpdatedAt string  `json:"updated_at"` // RFC3339
	ParentID  string  `json:"parent_id,omitempty"`
	CostCents float64 `json:"cost_cents,omitempty"`
	Summary   string  `json:"summary,omitempty"`
}

// StoredMessage is a single persisted message (backward-compatible with localstore).
type StoredMessage struct {
	Role      string    `json:"role"`
	Content   string    `json:"content"`
	CreatedAt time.Time `json:"created_at"`
}

// ConversationSummary is returned by ListConversations (backward-compatible).
type ConversationSummary struct {
	ID        string `json:"id"`
	Title     string `json:"title"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
}

// SearchResult represents a search hit across conversations.
type SearchResult struct {
	ConversationID    string  `json:"conversation_id"`
	ConversationTitle string  `json:"conversation_title"`
	MessageSeq        int     `json:"message_seq"`
	Snippet           string  `json:"snippet"`
	Score             float64 `json:"score"`
}

// bleveDoc is the document indexed in Bleve.
type bleveDoc struct {
	ConversationID string `json:"conversation_id"`
	Role           string `json:"role"`
	Content        string `json:"content"`
}

// Open opens (or creates) a Store at the given paths.
func Open(dbPath, indexPath string) (*Store, error) {
	// Ensure parent directories exist.
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o700); err != nil {
		return nil, fmt.Errorf("create db dir: %w", err)
	}

	db, err := bolt.Open(dbPath, 0o600, &bolt.Options{Timeout: 2 * time.Second})
	if err != nil {
		return nil, fmt.Errorf("open bbolt: %w", err)
	}

	// Create buckets.
	if err := db.Update(func(tx *bolt.Tx) error {
		for _, name := range [][]byte{bucketConversations, bucketMessages, bucketMetadata} {
			if _, err := tx.CreateBucketIfNotExists(name); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		db.Close()
		return nil, fmt.Errorf("create buckets: %w", err)
	}

	// Open or create Bleve index.
	var idx bleve.Index
	if _, err := os.Stat(indexPath); os.IsNotExist(err) {
		mapping := bleve.NewIndexMapping()
		idx, err = bleve.New(indexPath, mapping)
		if err != nil {
			db.Close()
			return nil, fmt.Errorf("create bleve index: %w", err)
		}
	} else {
		idx, err = bleve.Open(indexPath)
		if err != nil {
			db.Close()
			return nil, fmt.Errorf("open bleve index: %w", err)
		}
	}

	return &Store{db: db, index: idx}, nil
}

// Close closes the store and its search index.
func (s *Store) Close() error {
	var errs []string
	if err := s.index.Close(); err != nil {
		errs = append(errs, "bleve: "+err.Error())
	}
	if err := s.db.Close(); err != nil {
		errs = append(errs, "bbolt: "+err.Error())
	}
	if len(errs) > 0 {
		return fmt.Errorf("close store: %s", strings.Join(errs, "; "))
	}
	return nil
}

// SaveConversation writes a full conversation (creates or overwrites).
func (s *Store) SaveConversation(id, title string, msgs []StoredMessage) error {
	now := time.Now().UTC().Format(time.RFC3339)
	conv := Conversation{
		ID:        id,
		Title:     title,
		CreatedAt: now,
		UpdatedAt: now,
	}
	convData, err := json.Marshal(conv)
	if err != nil {
		return err
	}

	return s.db.Update(func(tx *bolt.Tx) error {
		cb := tx.Bucket(bucketConversations)
		mb := tx.Bucket(bucketMessages)

		// Delete existing messages for this conversation.
		s.deleteConvMessages(mb, id)

		// Write conversation metadata.
		if err := cb.Put([]byte(id), convData); err != nil {
			return err
		}

		// Write messages.
		for i, msg := range msgs {
			key := msgKey(id, i)
			data, err := json.Marshal(msg)
			if err != nil {
				return err
			}
			if err := mb.Put(key, data); err != nil {
				return err
			}
			// Index in Bleve.
			s.indexMessage(id, i, msg)
		}
		return nil
	})
}

// AppendMessages appends messages to a conversation, creating it if needed.
func (s *Store) AppendMessages(id, title string, msgs ...StoredMessage) error {
	now := time.Now().UTC().Format(time.RFC3339)

	return s.db.Update(func(tx *bolt.Tx) error {
		cb := tx.Bucket(bucketConversations)
		mb := tx.Bucket(bucketMessages)

		// Check if conversation exists.
		existing := cb.Get([]byte(id))
		var conv Conversation
		if existing != nil {
			if err := json.Unmarshal(existing, &conv); err != nil {
				return err
			}
			conv.UpdatedAt = now
			if conv.Title == "" && title != "" {
				conv.Title = title
			}
		} else {
			conv = Conversation{
				ID:        id,
				Title:     title,
				CreatedAt: now,
				UpdatedAt: now,
			}
		}

		convData, err := json.Marshal(conv)
		if err != nil {
			return err
		}
		if err := cb.Put([]byte(id), convData); err != nil {
			return err
		}

		// Count existing messages via prefix scan.
		count := s.countMessages(mb, id)

		// Append new messages.
		for i, msg := range msgs {
			seq := count + i
			key := msgKey(id, seq)
			data, err := json.Marshal(msg)
			if err != nil {
				return err
			}
			if err := mb.Put(key, data); err != nil {
				return err
			}
			s.indexMessage(id, seq, msg)
		}
		return nil
	})
}

// ListConversations returns summaries sorted by UpdatedAt DESC.
func (s *Store) ListConversations(limit, offset int) ([]ConversationSummary, error) {
	var summaries []ConversationSummary

	if err := s.db.View(func(tx *bolt.Tx) error {
		cb := tx.Bucket(bucketConversations)
		return cb.ForEach(func(k, v []byte) error {
			var conv Conversation
			if err := json.Unmarshal(v, &conv); err != nil {
				return nil // skip corrupt entries
			}
			summaries = append(summaries, ConversationSummary{
				ID:        conv.ID,
				Title:     conv.Title,
				CreatedAt: conv.CreatedAt,
				UpdatedAt: conv.UpdatedAt,
			})
			return nil
		})
	}); err != nil {
		return nil, err
	}

	// Sort by UpdatedAt descending.
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

// GetMessages returns all messages for a conversation in order.
func (s *Store) GetMessages(id string) ([]StoredMessage, error) {
	var msgs []StoredMessage

	if err := s.db.View(func(tx *bolt.Tx) error {
		mb := tx.Bucket(bucketMessages)
		prefix := []byte(id + "/")
		c := mb.Cursor()
		for k, v := c.Seek(prefix); k != nil && hasPrefix(k, prefix); k, v = c.Next() {
			var msg StoredMessage
			if err := json.Unmarshal(v, &msg); err != nil {
				continue
			}
			msgs = append(msgs, msg)
		}
		return nil
	}); err != nil {
		return nil, err
	}
	return msgs, nil
}

// DeleteConversation removes a conversation and all its messages.
func (s *Store) DeleteConversation(id string) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		cb := tx.Bucket(bucketConversations)
		mb := tx.Bucket(bucketMessages)

		if err := cb.Delete([]byte(id)); err != nil {
			return err
		}
		s.deleteConvMessages(mb, id)
		return nil
	})
}

// SearchMessages performs full-text search across all conversations.
func (s *Store) SearchMessages(query string, limit int) ([]SearchResult, error) {
	if limit <= 0 {
		limit = 20
	}

	q := bleve.NewQueryStringQuery(query)
	req := bleve.NewSearchRequestOptions(q, limit, 0, false)
	req.Fields = []string{"conversation_id", "role", "content"}

	res, err := s.index.Search(req)
	if err != nil {
		return nil, fmt.Errorf("search: %w", err)
	}

	var results []SearchResult
	for _, hit := range res.Hits {
		convID, seq := parseMsgKey(hit.ID)
		if convID == "" {
			continue
		}

		// Get conversation title.
		var title string
		_ = s.db.View(func(tx *bolt.Tx) error {
			cb := tx.Bucket(bucketConversations)
			data := cb.Get([]byte(convID))
			if data != nil {
				var conv Conversation
				if err := json.Unmarshal(data, &conv); err == nil {
					title = conv.Title
				}
			}
			return nil
		})

		// Build snippet from content field.
		snippet := ""
		if content, ok := hit.Fields["content"].(string); ok {
			if len(content) > 200 {
				snippet = content[:200] + "..."
			} else {
				snippet = content
			}
		}

		results = append(results, SearchResult{
			ConversationID:    convID,
			ConversationTitle: title,
			MessageSeq:        seq,
			Snippet:           snippet,
			Score:             hit.Score,
		})
	}
	return results, nil
}

// ForkConversation creates a new conversation from an existing one up to atMessageSeq.
func (s *Store) ForkConversation(fromID string, atMessageSeq int) (string, error) {
	newID := generateID()
	now := time.Now().UTC().Format(time.RFC3339)

	return newID, s.db.Update(func(tx *bolt.Tx) error {
		cb := tx.Bucket(bucketConversations)
		mb := tx.Bucket(bucketMessages)

		// Get source conversation.
		srcData := cb.Get([]byte(fromID))
		if srcData == nil {
			return fmt.Errorf("source conversation %q not found", fromID)
		}
		var src Conversation
		if err := json.Unmarshal(srcData, &src); err != nil {
			return err
		}

		// Create forked conversation.
		fork := Conversation{
			ID:        newID,
			Title:     src.Title + " (fork)",
			CreatedAt: now,
			UpdatedAt: now,
			ParentID:  fromID,
		}
		forkData, err := json.Marshal(fork)
		if err != nil {
			return err
		}
		if err := cb.Put([]byte(newID), forkData); err != nil {
			return err
		}

		// Copy messages up to atMessageSeq.
		prefix := []byte(fromID + "/")
		c := mb.Cursor()
		seq := 0
		for k, v := c.Seek(prefix); k != nil && hasPrefix(k, prefix); k, v = c.Next() {
			if seq > atMessageSeq {
				break
			}
			newKey := msgKey(newID, seq)
			if err := mb.Put(newKey, v); err != nil {
				return err
			}
			// Index the copied message.
			var msg StoredMessage
			if err := json.Unmarshal(v, &msg); err == nil {
				s.indexMessage(newID, seq, msg)
			}
			seq++
		}
		return nil
	})
}

// UpdateCost updates the cost in cents for a conversation.
func (s *Store) UpdateCost(id string, costCents float64) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		cb := tx.Bucket(bucketConversations)
		data := cb.Get([]byte(id))
		if data == nil {
			return fmt.Errorf("conversation %q not found", id)
		}
		var conv Conversation
		if err := json.Unmarshal(data, &conv); err != nil {
			return err
		}
		conv.CostCents = costCents
		conv.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
		updated, err := json.Marshal(conv)
		if err != nil {
			return err
		}
		return cb.Put([]byte(id), updated)
	})
}

// --- Internal helpers ---

// msgKey builds a message key: "<convID>/<zero-padded-seq>".
func msgKey(convID string, seq int) []byte {
	return []byte(fmt.Sprintf("%s/%08d", convID, seq))
}

// parseMsgKey extracts convID and seq from a message key.
func parseMsgKey(key string) (string, int) {
	idx := strings.LastIndex(key, "/")
	if idx < 0 {
		return "", 0
	}
	var seq int
	fmt.Sscanf(key[idx+1:], "%d", &seq)
	return key[:idx], seq
}

// hasPrefix checks if a byte slice has a given prefix.
func hasPrefix(b, prefix []byte) bool {
	if len(b) < len(prefix) {
		return false
	}
	for i := range prefix {
		if b[i] != prefix[i] {
			return false
		}
	}
	return true
}

// countMessages counts messages for a conversation via prefix scan.
func (s *Store) countMessages(mb *bolt.Bucket, convID string) int {
	prefix := []byte(convID + "/")
	count := 0
	c := mb.Cursor()
	for k, _ := c.Seek(prefix); k != nil && hasPrefix(k, prefix); k, _ = c.Next() {
		count++
	}
	return count
}

// deleteConvMessages deletes all messages for a conversation.
func (s *Store) deleteConvMessages(mb *bolt.Bucket, convID string) {
	prefix := []byte(convID + "/")
	c := mb.Cursor()
	var toDelete [][]byte
	for k, _ := c.Seek(prefix); k != nil && hasPrefix(k, prefix); k, _ = c.Next() {
		key := make([]byte, len(k))
		copy(key, k)
		toDelete = append(toDelete, key)
	}
	for _, key := range toDelete {
		mb.Delete(key)
		s.index.Delete(string(key))
	}
}

// indexMessage indexes a message in Bleve (best-effort, errors silently).
func (s *Store) indexMessage(convID string, seq int, msg StoredMessage) {
	doc := bleveDoc{
		ConversationID: convID,
		Role:           msg.Role,
		Content:        msg.Content,
	}
	key := string(msgKey(convID, seq))
	_ = s.index.Index(key, doc)
}

// generateID creates a simple UUID-like identifier.
func generateID() string {
	now := time.Now().UnixNano()
	return fmt.Sprintf("%x-%x", now, now%1000000)
}
