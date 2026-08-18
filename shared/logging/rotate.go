package logging

import (
	"fmt"
	"os"
	"sync"
)

// rotatingWriter is a simple log file writer with size-based rotation.
// When the current file exceeds maxBytes, it rotates:
//
//	bujicoder.log   → bujicoder.log.1
//	bujicoder.log.1 → bujicoder.log.2
//	... (up to maxBackups, oldest deleted)
type rotatingWriter struct {
	mu         sync.Mutex
	path       string
	maxBytes   int64
	maxBackups int
	file       *os.File
	size       int64
	closed     bool
}

// newRotatingWriter creates a rotating writer. maxSizeMB is the max file size
// in megabytes before rotation. maxBackups is how many rotated files to keep.
func newRotatingWriter(path string, maxSizeMB, maxBackups int) *rotatingWriter {
	w := &rotatingWriter{
		path:       path,
		maxBytes:   int64(maxSizeMB) * 1024 * 1024,
		maxBackups: maxBackups,
	}
	// Open existing file (append) or create. A failure is tolerated: Write
	// retries the open, so an unwritable directory degrades instead of crashing.
	_ = w.open()
	return w
}

// Write implements io.Writer. Thread-safe. A failure to open or write the log
// file is reported as an error and never panics: logging must degrade, not take
// the application down with it.
func (w *rotatingWriter) Write(p []byte) (n int, err error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	if w.closed {
		return 0, os.ErrClosed
	}

	if w.file == nil {
		// Try to reopen. The file is opened for append, so its existing size
		// counts towards the rotation threshold — resetting size to 0 here would
		// let the file grow without bound.
		if err := w.open(); err != nil {
			return 0, err
		}
	}

	// Check if rotation is needed before writing. Rotating an already-empty file
	// would achieve nothing and, for a single record larger than maxBytes, would
	// rotate on every write and churn through the backups.
	if w.size > 0 && w.size+int64(len(p)) > w.maxBytes {
		w.rotate()
		if w.file == nil {
			// Rotation could not reopen the log file (unwritable directory).
			return 0, fmt.Errorf("log rotation failed for %s", w.path)
		}
	}

	n, err = w.file.Write(p)
	w.size += int64(n)
	return n, err
}

// open opens the log file for appending and syncs the tracked size with the
// file's actual size. Callers must hold w.mu.
func (w *rotatingWriter) open() error {
	f, err := os.OpenFile(w.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	w.file = f
	w.size = 0
	if info, err := f.Stat(); err == nil {
		w.size = info.Size()
	}
	return nil
}

// rotate moves the current file to .1, .1 to .2, etc.
func (w *rotatingWriter) rotate() {
	if w.file != nil {
		_ = w.file.Close()
		w.file = nil
	}

	// Shift existing backups: .4→delete, .3→.4, .2→.3, .1→.2
	for i := w.maxBackups; i >= 1; i-- {
		src := fmt.Sprintf("%s.%d", w.path, i)
		if i == w.maxBackups {
			_ = os.Remove(src)
		} else {
			dst := fmt.Sprintf("%s.%d", w.path, i+1)
			_ = os.Rename(src, dst)
		}
	}

	// Current → .1
	_ = os.Rename(w.path, w.path+".1")

	// Drop backups left over from a run configured with a larger maxBackups;
	// otherwise they would sit on disk forever and defeat the size bound.
	for i := w.maxBackups + 1; ; i++ {
		stale := fmt.Sprintf("%s.%d", w.path, i)
		if _, err := os.Stat(stale); err != nil {
			break
		}
		_ = os.Remove(stale)
	}

	// Open new file.
	f, err := os.OpenFile(w.path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err == nil {
		w.file = f
		w.size = 0
	}
}

// Close closes the underlying file. Later writes are rejected instead of
// silently reopening the file behind the caller's back.
func (w *rotatingWriter) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.closed = true
	if w.file != nil {
		f := w.file
		w.file = nil
		return f.Close()
	}
	return nil
}
