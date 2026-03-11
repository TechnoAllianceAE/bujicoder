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
}

// newRotatingWriter creates a rotating writer. maxSizeMB is the max file size
// in megabytes before rotation. maxBackups is how many rotated files to keep.
func newRotatingWriter(path string, maxSizeMB, maxBackups int) *rotatingWriter {
	w := &rotatingWriter{
		path:       path,
		maxBytes:   int64(maxSizeMB) * 1024 * 1024,
		maxBackups: maxBackups,
	}
	// Open existing file (append) or create.
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err == nil {
		w.file = f
		if info, err := f.Stat(); err == nil {
			w.size = info.Size()
		}
	}
	return w
}

// Write implements io.Writer. Thread-safe.
func (w *rotatingWriter) Write(p []byte) (n int, err error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	if w.file == nil {
		// Try to reopen.
		f, err := os.OpenFile(w.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
		if err != nil {
			return 0, err
		}
		w.file = f
		w.size = 0
	}

	// Check if rotation is needed before writing.
	if w.size+int64(len(p)) > w.maxBytes {
		w.rotate()
	}

	n, err = w.file.Write(p)
	w.size += int64(n)
	return n, err
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

	// Open new file.
	f, err := os.OpenFile(w.path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err == nil {
		w.file = f
		w.size = 0
	}
}

// Close closes the underlying file.
func (w *rotatingWriter) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.file != nil {
		return w.file.Close()
	}
	return nil
}
