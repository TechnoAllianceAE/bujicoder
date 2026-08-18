package store

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/rs/zerolog/log"
	bolt "go.etcd.io/bbolt"
)

// jsonConversationFile mirrors the old localstore JSON format.
type jsonConversationFile struct {
	ID        string          `json:"id"`
	Title     string          `json:"title"`
	CreatedAt time.Time       `json:"created_at"`
	UpdatedAt time.Time       `json:"updated_at"`
	Messages  []StoredMessage `json:"messages"`
}

// migratedKey is the metadata key recording that jsonDir was already imported.
func migratedKey(jsonDir string) []byte {
	return []byte("migrated_json:" + jsonDir)
}

// MigrateFromJSON migrates all conversations from the old JSON directory
// into the new bbolt+Bleve store. After successful migration, the JSON
// directory is renamed to jsonDir+".bak".
//
// It is safe to call on every start: completion is recorded durably in the
// store, so an already-migrated directory is never re-imported over newer
// conversation data.
func MigrateFromJSON(jsonDir string, s *Store) error {
	done, err := s.migrationDone(jsonDir)
	if err != nil {
		return err
	}
	if done {
		return nil
	}

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

		path := filepath.Join(jsonDir, entry.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("read %s: %w", path, err)
		}

		var conv jsonConversationFile
		if err := json.Unmarshal(data, &conv); err != nil {
			continue // skip corrupt files
		}
		if conv.ID == "" {
			// Without an ID the record cannot be keyed; fall back to the file
			// name so the messages are not silently dropped.
			conv.ID = entry.Name()[:len(entry.Name())-len(".json")]
			if conv.ID == "" {
				continue
			}
		}

		if err := s.SaveConversation(conv.ID, conv.Title, conv.Messages); err != nil {
			return fmt.Errorf("migrate conversation %s: %w", conv.ID, err)
		}
		migrated++
	}

	// Record completion before touching the source directory: if the rename
	// fails (or the process dies right after it), the next run must not import
	// the stale JSON on top of conversations that have since been updated.
	if err := s.markMigrated(jsonDir); err != nil {
		return err
	}

	if migrated > 0 {
		// Rename the old directory to .bak. Non-fatal: the data is migrated and
		// the marker prevents a re-import, so the only consequence is that the
		// legacy directory stays on disk.
		if err := os.Rename(jsonDir, jsonDir+".bak"); err != nil {
			log.Warn().Err(err).Str("dir", jsonDir).Msg("store: migrated conversations but could not archive the legacy JSON directory")
			return nil
		}
	}
	return nil
}

// migrationDone reports whether jsonDir has already been imported into s.
func (s *Store) migrationDone(jsonDir string) (bool, error) {
	done := false
	err := s.db.View(func(tx *bolt.Tx) error {
		done = tx.Bucket(bucketMetadata).Get(migratedKey(jsonDir)) != nil
		return nil
	})
	if err != nil {
		return false, fmt.Errorf("read migration marker: %w", err)
	}
	return done, nil
}

// markMigrated durably records that jsonDir has been imported.
func (s *Store) markMigrated(jsonDir string) error {
	if err := s.db.Update(func(tx *bolt.Tx) error {
		return tx.Bucket(bucketMetadata).Put(migratedKey(jsonDir), []byte(time.Now().UTC().Format(time.RFC3339)))
	}); err != nil {
		return fmt.Errorf("record migration marker: %w", err)
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
