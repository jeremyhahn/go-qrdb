// Copyright 2026 Jeremy Hahn
// SPDX-License-Identifier: Apache-2.0
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

package pebble

import (
	"fmt"
	"log/slog"
	"os"

	"github.com/cockroachdb/pebble/v2"
)

// slogPebbleLogger adapts an *slog.Logger to pebble.Logger. Pebble's internal
// operational events (WAL discovery/creation, compactions, flushes, manifest
// rotation) arrive via Infof; they are pure operational chatter and are emitted
// at DEBUG so they stay out of default INFO-level logs — this is what stops a
// standalone store's startup from littering the log with "Found N WALs" and
// "[JOB N] WAL ..." lines. Errors are surfaced at ERROR. Fatalf preserves the
// terminating semantics of Pebble's DefaultLogger (it logs then exits): Pebble
// calls Fatalf only on unrecoverable corruption/IO failures.
type slogPebbleLogger struct {
	logger *slog.Logger
}

// Compile-time check.
var _ pebble.Logger = (*slogPebbleLogger)(nil)

// newSlogPebbleLogger builds the adapter, defaulting to slog.Default() when nil.
func newSlogPebbleLogger(logger *slog.Logger) *slogPebbleLogger {
	if logger == nil {
		logger = slog.Default()
	}
	return &slogPebbleLogger{logger: logger.With(slog.String("component", "pebble"))}
}

// NewLogger returns a pebble.Logger that routes Pebble's operational chatter
// (WAL/compaction/flush) to slog DEBUG and errors to slog ERROR, so callers that
// build their own *pebble.Options (vector store, time-series, Raft state-machine
// stores, re-encryption) keep the same quiet behavior as the shared
// ToPebbleOptions path. Pass nil for slog.Default(). Setting it on every
// pebble.Open call site is what keeps a standalone store off the default logger,
// which would otherwise print "Found N WALs" / "[JOB N] WAL ..." to stderr.
func NewLogger(logger *slog.Logger) pebble.Logger {
	return newSlogPebbleLogger(logger)
}

// Infof routes Pebble's operational chatter to DEBUG.
func (l *slogPebbleLogger) Infof(format string, args ...interface{}) {
	l.logger.Debug(fmt.Sprintf(format, args...))
}

// Errorf routes Pebble errors to ERROR.
func (l *slogPebbleLogger) Errorf(format string, args ...interface{}) {
	l.logger.Error(fmt.Sprintf(format, args...))
}

// Fatalf logs at ERROR and terminates, mirroring Pebble's DefaultLogger.Fatalf
// (Pebble invokes it only on unrecoverable conditions).
func (l *slogPebbleLogger) Fatalf(format string, args ...interface{}) {
	l.logger.Error(fmt.Sprintf(format, args...))
	os.Exit(1)
}
