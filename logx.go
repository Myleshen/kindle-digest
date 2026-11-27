package main

import (
	"fmt"
	"io"
	"os"
	"sync"
	"time"
)

// logger writes plain-text lines to stdout (or stderr for errors) and, when
// configured, mirrors them to a log file.
type logger struct {
	mu   sync.Mutex
	file *os.File
}

func newLogger(path string) (*logger, error) {
	l := &logger{}
	if path == "" {
		return l, nil
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil, fmt.Errorf("opening log file %s: %w", path, err)
	}
	l.file = f
	return l, nil
}

func (l *logger) close() {
	if l.file != nil {
		_ = l.file.Close()
	}
}

func (l *logger) write(w io.Writer, format string, args ...any) {
	line := fmt.Sprintf("%s %s\n", time.Now().Format("2006-01-02 15:04:05"), fmt.Sprintf(format, args...))
	l.mu.Lock()
	defer l.mu.Unlock()
	_, _ = io.WriteString(w, line)
	if l.file != nil {
		_, _ = io.WriteString(l.file, line)
	}
}

func (l *logger) info(format string, args ...any) {
	l.write(os.Stdout, format, args...)
}

func (l *logger) error(format string, args ...any) {
	l.write(os.Stderr, format, args...)
}

// fileOnly records a fatal error in the log file; main already reports it on
// stderr to avoid printing the same line twice.
func (l *logger) fileOnly(format string, args ...any) {
	if l.file == nil {
		return
	}
	line := fmt.Sprintf("%s %s\n", time.Now().Format("2006-01-02 15:04:05"), fmt.Sprintf(format, args...))
	l.mu.Lock()
	defer l.mu.Unlock()
	_, _ = io.WriteString(l.file, line)
}
