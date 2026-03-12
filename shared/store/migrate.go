package store

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// jsonConversationFile mirrors the old localstore JSON format.
type jsonConversationFile struct {
	ID        string             `json:"id"`
	Title     string             `json:"title"`
	CreatedAt time.Time          `json:"created_at"`
	UpdatedAt time.Time          `json:"updated_at"`
	Messages  []jsonStoredMessage `json:"messages"`
}

type jsonStoredMessage struct {
	Role      string    `json:"role"`
	Content   string    `json:"content"`
	CreatedAt time.Time `json:"created_at"`
}

// MigrateFromJSON migrates all conversations from the old JSON directory
// into the new bbolt+Bleve store. After successful migration, the JSON
// directory is renamed to jsonDir+".bak".
func MigrateFromJSON(jsonDir string, s *Store) error {
	entries, err := os.ReadDir(jsonDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil // nothing to migrate
		}
		return fmt.Errorf("read json dir: %w", err)
	}

	migrated := 0
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}

		data, err := os.ReadFile(filepath.Join(jsonDir, entry.Name()))
		if err != nil {
			continue // skip unreadable files
		}

		var conv jsonConversationFile
		if err := json.Unmarshal(data, &conv); err != nil {
			continue // skip corrupt files
		}

		// Convert messages.
		msgs := make([]StoredMessage, len(conv.Messages))
		for i, m := range conv.Messages {
			msgs[i] = StoredMessage{
				Role:      m.Role,
				Content:   m.Content,
				CreatedAt: m.CreatedAt,
			}
		}

		if err := s.SaveConversation(conv.ID, conv.Title, msgs); err != nil {
			return fmt.Errorf("migrate conversation %s: %w", conv.ID, err)
		}
		migrated++
	}

	if migrated > 0 {
		// Rename the old directory to .bak.
		bakDir := jsonDir + ".bak"
		if err := os.Rename(jsonDir, bakDir); err != nil {
			// Not fatal — data is migrated, just couldn't rename.
			return nil
		}
	}
	return nil
}

// NeedsMigration checks whether the old JSON conversation directory exists
// and the bbolt database does not.
func NeedsMigration(jsonDir, dbPath string) bool {
	// JSON dir must exist.
	if _, err := os.Stat(jsonDir); os.IsNotExist(err) {
		return false
	}
	// DB must NOT exist.
	if _, err := os.Stat(dbPath); err == nil {
		return false
	}
	return true
}
