// Copyright (c) 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package audit

import (
	"context"
	"log/slog"
	"os"
	"sync"
	"time"
)

// fileLogger writes audit entries to a JSON log file using slog.
type fileLogger struct {
	file   *os.File
	logger *slog.Logger
	mu     sync.Mutex
}

// newFileLogger opens the file at path for append-only writing and
// returns a fileLogger that emits JSON-formatted slog records.
func newFileLogger(path string) (*fileLogger, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, &FileError{Path: path, Err: err}
	}

	handler := slog.NewJSONHandler(f, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	})

	return &fileLogger{
		file:   f,
		logger: slog.New(handler),
	}, nil
}

// Log writes a structured audit entry to the log file.
func (l *fileLogger) Log(_ context.Context, entry *Entry) error {
	if entry.Timestamp.IsZero() {
		entry.Timestamp = time.Now().UTC()
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	attrs := []slog.Attr{
		slog.String("tenant_id", entry.TenantID),
		slog.String("subject", entry.Subject),
		slog.String("action", entry.Action),
		slog.String("resource", entry.Resource),
		slog.String("result", entry.Result),
		slog.Time("event_time", entry.Timestamp),
	}

	if len(entry.Metadata) > 0 {
		metaAttrs := make([]any, 0, len(entry.Metadata))
		for k, v := range entry.Metadata {
			metaAttrs = append(metaAttrs, slog.String(k, v))
		}
		attrs = append(attrs, slog.Group("metadata", metaAttrs...))
	}

	l.logger.LogAttrs(context.Background(), slog.LevelInfo, "audit", attrs...)

	return nil
}

// Close flushes and closes the underlying file.
func (l *fileLogger) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.file.Close()
}
