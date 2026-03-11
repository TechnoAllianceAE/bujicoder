package logging

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestRotatingWriter_BasicWrite(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.log")

	w := newRotatingWriter(path, 1, 3) // 1MB max, 3 backups
	defer w.Close()

	_, err := w.Write([]byte("hello\n"))
	if err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "hello\n" {
		t.Fatalf("expected 'hello\\n', got %q", string(data))
	}
}

func TestRotatingWriter_RotatesOnSize(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.log")

	// Tiny max size: 100 bytes so rotation triggers quickly.
	w := &rotatingWriter{
		path:       path,
		maxBytes:   100,
		maxBackups: 3,
	}
	// Open file.
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	w.file = f

	// Write enough to trigger rotation.
	line := "this is a test line that is fairly long\n" // ~41 bytes
	for i := 0; i < 5; i++ {
		_, err := w.Write([]byte(line))
		if err != nil {
			t.Fatal(err)
		}
	}
	w.Close()

	// Check that .1 backup exists.
	if _, err := os.Stat(path + ".1"); os.IsNotExist(err) {
		t.Fatal("expected rotated file test.log.1 to exist")
	}
	// Current file should still exist with content.
	if _, err := os.Stat(path); os.IsNotExist(err) {
		t.Fatal("expected current log file to exist after rotation")
	}
}

func TestRotatingWriter_MaxBackups(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.log")

	w := &rotatingWriter{
		path:       path,
		maxBytes:   50, // tiny: rotate after ~50 bytes
		maxBackups: 2,  // keep only 2 backups
	}
	f, _ := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	w.file = f

	// Write enough to trigger multiple rotations.
	line := "abcdefghij1234567890abcdefghij1234567890abcdefghij\n" // 51 bytes
	for i := 0; i < 10; i++ {
		w.Write([]byte(line))
	}
	w.Close()

	// .1 and .2 should exist.
	for i := 1; i <= 2; i++ {
		p := fmt.Sprintf("%s.%d", path, i)
		if _, err := os.Stat(p); os.IsNotExist(err) {
			t.Fatalf("expected backup %s to exist", p)
		}
	}

	// .3 should NOT exist (maxBackups=2).
	if _, err := os.Stat(path + ".3"); !os.IsNotExist(err) {
		t.Fatal("backup .3 should not exist with maxBackups=2")
	}
}
