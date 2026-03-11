// Package logging provides structured, persistent logging for BujiCoder CLI.
// Logs are written as JSON lines to ~/.bujicoder/logs/ with automatic rotation.
package logging

import (
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/rs/zerolog"

	"github.com/TechnoAllianceAE/bujicoder/shared/buildinfo"
)

// Config controls logger behaviour.
type Config struct {
	Dir        string // Log directory (default: ~/.bujicoder/logs)
	MaxSizeMB  int    // Max size of each log file in MB before rotation (default: 10)
	MaxBackups int    // Number of rotated files to keep (default: 5)
	Level      string // Log level: trace, debug, info, warn, error (default: info)
	Verbose    bool   // If true, also write human-readable output to stderr
}

// defaults fills zero-valued fields with sensible defaults.
func (c *Config) defaults() {
	if c.Dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			home = os.TempDir()
		}
		c.Dir = filepath.Join(home, ".bujicoder", "logs")
	}
	if c.MaxSizeMB <= 0 {
		c.MaxSizeMB = 10
	}
	if c.MaxBackups <= 0 {
		c.MaxBackups = 5
	}
	if c.Level == "" {
		c.Level = "info"
	}
}

// New creates a zerolog.Logger that writes structured JSON to a log file with
// rotation, and optionally writes human-readable output to stderr.
func New(cfg Config) zerolog.Logger {
	cfg.defaults()

	// Override from env vars.
	if envLevel := os.Getenv("BUJICODER_LOG_LEVEL"); envLevel != "" {
		cfg.Level = envLevel
	}
	if envDir := os.Getenv("BUJICODER_LOG_DIR"); envDir != "" {
		cfg.Dir = envDir
	}

	// Ensure log directory exists.
	if err := os.MkdirAll(cfg.Dir, 0o755); err != nil {
		// Can't create log dir — fall back to nop.
		return zerolog.Nop()
	}

	logPath := filepath.Join(cfg.Dir, "bujicoder.log")
	fileWriter := newRotatingWriter(logPath, cfg.MaxSizeMB, cfg.MaxBackups)

	var writers []io.Writer
	writers = append(writers, fileWriter)

	if cfg.Verbose {
		writers = append(writers, zerolog.ConsoleWriter{Out: os.Stderr, TimeFormat: "15:04:05"})
	}

	level := parseLevel(cfg.Level)

	return zerolog.New(zerolog.MultiLevelWriter(writers...)).
		Level(level).
		With().
		Timestamp().
		Str("version", buildinfo.Version).
		Logger()
}

// parseLevel converts a string to a zerolog.Level.
func parseLevel(s string) zerolog.Level {
	switch strings.ToLower(s) {
	case "trace":
		return zerolog.TraceLevel
	case "debug":
		return zerolog.DebugLevel
	case "info":
		return zerolog.InfoLevel
	case "warn", "warning":
		return zerolog.WarnLevel
	case "error":
		return zerolog.ErrorLevel
	case "fatal":
		return zerolog.FatalLevel
	default:
		return zerolog.InfoLevel
	}
}

// LogDir returns the resolved log directory for the given config.
// Useful for displaying the path in the TUI.
func LogDir(cfg Config) string {
	cfg.defaults()
	if envDir := os.Getenv("BUJICODER_LOG_DIR"); envDir != "" {
		return envDir
	}
	return cfg.Dir
}
