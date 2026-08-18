// Package store provides transactional conversation persistence using bbolt + Bleve,
// replacing the JSON-file-based localstore.
package store

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/blevesearch/bleve/v2"
	"github.com/rs/zerolog/log"
	bolt "go.etcd.io/bbolt"
)

// schemaVersion is the on-disk layout version understood by this package.
const schemaVersion = 1

// Bucket names in bbolt.
var (
	bucketConversations = []byte("conversations")
	bucketMessages      = []byte("messages")
	bucketMetadata      = []byte("metadata")

	// keySchemaVersion stores the schema version inside bucketMetadata.
	keySchemaVersion = []byte("schema_version")
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

	// Create buckets and validate the on-disk schema version. A database written
	// by a newer BujiCoder must not be silently downgraded.
	if err := db.Update(func(tx *bolt.Tx) error {
		for _, name := range [][]byte{bucketConversations, bucketMessages, bucketMetadata} {
			if _, err := tx.CreateBucketIfNotExists(name); err != nil {
				return err
			}
		}
		return checkSchemaVersion(tx.Bucket(bucketMetadata))
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
	if id == "" {
		return fmt.Errorf("save conversation: empty id")
	}
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

	var ops []indexOp
	if err := s.db.Update(func(tx *bolt.Tx) error {
		// The transaction may be retried/rolled back, so index mutations are
		// staged here and only applied to Bleve after a successful commit.
		ops = ops[:0]
		cb := tx.Bucket(bucketConversations)
		mb := tx.Bucket(bucketMessages)

		// Delete existing messages for this conversation.
		if err := deleteConvMessages(mb, id, &ops); err != nil {
			return err
		}

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
			ops = append(ops, putIndexOp(id, i, msg))
		}
		return nil
	}); err != nil {
		return err
	}

	s.applyIndexOps(ops)
	return nil
}

// AppendMessages appends messages to a conversation, creating it if needed.
func (s *Store) AppendMessages(id, title string, msgs ...StoredMessage) error {
	if id == "" {
		return fmt.Errorf("append messages: empty id")
	}
	now := time.Now().UTC().Format(time.RFC3339)

	var ops []indexOp
	if err := s.db.Update(func(tx *bolt.Tx) error {
		ops = ops[:0]
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
		count := countMessages(mb, id)

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
			ops = append(ops, putIndexOp(id, seq, msg))
		}
		return nil
	}); err != nil {
		return err
	}

	s.applyIndexOps(ops)
	return nil
}

// ListConversations returns summaries sorted by UpdatedAt DESC.
func (s *Store) ListConversations(limit, offset int) ([]ConversationSummary, error) {
	var summaries []ConversationSummary

	if err := s.db.View(func(tx *bolt.Tx) error {
		cb := tx.Bucket(bucketConversations)
		return cb.ForEach(func(k, v []byte) error {
			var conv Conversation
			if err := json.Unmarshal(v, &conv); err != nil {
				// A corrupt record is skipped so the rest of the history stays
				// listable, but it must be visible rather than disappear.
				log.Warn().Err(err).Str("id", string(k)).Msg("store: skipping corrupt conversation record")
				return nil
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
	var ops []indexOp
	if err := s.db.Update(func(tx *bolt.Tx) error {
		ops = ops[:0]
		cb := tx.Bucket(bucketConversations)
		mb := tx.Bucket(bucketMessages)

		if err := cb.Delete([]byte(id)); err != nil {
			return err
		}
		return deleteConvMessages(mb, id, &ops)
	}); err != nil {
		return err
	}

	s.applyIndexOps(ops)
	return nil
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

		// Build snippet from content field (rune-safe truncation).
		snippet := ""
		if content, ok := hit.Fields["content"].(string); ok {
			snippet = truncateRunes(content, 200)
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
	newID, err := generateID()
	if err != nil {
		return "", err
	}
	now := time.Now().UTC().Format(time.RFC3339)

	var ops []indexOp
	if err := s.db.Update(func(tx *bolt.Tx) error {
		ops = ops[:0]
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

		// Collect messages up to atMessageSeq. Values must be copied and the
		// bucket must not be mutated while its cursor is live.
		prefix := []byte(fromID + "/")
		c := mb.Cursor()
		var values [][]byte
		for k, v := c.Seek(prefix); k != nil && hasPrefix(k, prefix); k, v = c.Next() {
			if len(values) > atMessageSeq {
				break
			}
			values = append(values, append([]byte(nil), v...))
		}

		for seq, v := range values {
			if err := mb.Put(msgKey(newID, seq), v); err != nil {
				return err
			}
			// Index the copied message.
			var msg StoredMessage
			if err := json.Unmarshal(v, &msg); err == nil {
				ops = append(ops, putIndexOp(newID, seq, msg))
			}
		}
		return nil
	}); err != nil {
		return "", err
	}

	s.applyIndexOps(ops)
	return newID, nil
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

// parseMsgKey extracts convID and seq from a message key. A key whose sequence
// suffix is not a number is not a message key, and reporting it as seq 0 would
// point a search hit at the wrong message.
func parseMsgKey(key string) (string, int) {
	idx := strings.LastIndex(key, "/")
	if idx < 0 {
		return "", 0
	}
	seq, err := strconv.Atoi(key[idx+1:])
	if err != nil {
		return "", 0
	}
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
func countMessages(mb *bolt.Bucket, convID string) int {
	prefix := []byte(convID + "/")
	count := 0
	c := mb.Cursor()
	for k, _ := c.Seek(prefix); k != nil && hasPrefix(k, prefix); k, _ = c.Next() {
		count++
	}
	return count
}

// deleteConvMessages deletes all messages for a conversation and stages the
// matching Bleve deletions in ops.
func deleteConvMessages(mb *bolt.Bucket, convID string, ops *[]indexOp) error {
	prefix := []byte(convID + "/")
	c := mb.Cursor()
	var toDelete [][]byte
	for k, _ := c.Seek(prefix); k != nil && hasPrefix(k, prefix); k, _ = c.Next() {
		key := make([]byte, len(k))
		copy(key, k)
		toDelete = append(toDelete, key)
	}
	for _, key := range toDelete {
		if err := mb.Delete(key); err != nil {
			return fmt.Errorf("delete message %s: %w", key, err)
		}
		*ops = append(*ops, indexOp{key: string(key)})
	}
	return nil
}

// indexOp is a staged Bleve mutation. A nil doc means "delete".
type indexOp struct {
	key string
	doc *bleveDoc
}

// putIndexOp stages the indexing of a message.
func putIndexOp(convID string, seq int, msg StoredMessage) indexOp {
	return indexOp{
		key: string(msgKey(convID, seq)),
		doc: &bleveDoc{
			ConversationID: convID,
			Role:           msg.Role,
			Content:        msg.Content,
		},
	}
}

// applyIndexOps applies staged Bleve mutations after the bbolt transaction has
// committed. Indexing is best-effort: a failure degrades search but never
// invalidates the committed data. Applying before commit would leave the index
// referencing messages that a rolled-back transaction never wrote.
func (s *Store) applyIndexOps(ops []indexOp) {
	for _, op := range ops {
		if op.doc == nil {
			_ = s.index.Delete(op.key)
			continue
		}
		_ = s.index.Index(op.key, *op.doc)
	}
}

// truncateRunes truncates s to at most maxRunes runes, appending an ellipsis
// when it had to cut. Truncating by bytes would split a multi-byte rune and
// produce invalid UTF-8.
func truncateRunes(s string, maxRunes int) string {
	if maxRunes <= 0 {
		return ""
	}
	count := 0
	for i := range s {
		if count == maxRunes {
			return s[:i] + "..."
		}
		count++
	}
	return s
}

// checkSchemaVersion records the current schema version, refusing to operate on
// a database written by a newer version of the store.
func checkSchemaVersion(mb *bolt.Bucket) error {
	raw := mb.Get(keySchemaVersion)
	if raw == nil {
		return mb.Put(keySchemaVersion, []byte(strconv.Itoa(schemaVersion)))
	}
	onDisk, err := strconv.Atoi(string(raw))
	if err != nil {
		return fmt.Errorf("unreadable schema version %q", raw)
	}
	if onDisk > schemaVersion {
		return fmt.Errorf("database schema version %d is newer than supported version %d: upgrade BujiCoder", onDisk, schemaVersion)
	}
	return nil
}

// generateID creates a collision-resistant identifier. A purely time-based ID
// collides when two conversations are created inside the same clock tick, which
// would silently overwrite the earlier one.
func generateID() (string, error) {
	var b [10]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("generate id: %w", err)
	}
	return fmt.Sprintf("%x-%x", time.Now().UTC().Unix(), b), nil
}
