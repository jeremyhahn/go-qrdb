// Copyright 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

//go:build gormconformance

package tests_test

import (
	"context"
	"os"
	"strings"
	"sync"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// sqlLogEnvVar names a file every statement the selected GORM driver issues
// is appended to, together with the error the target returned for it. It is
// unset in normal runs: the recorder exists so that qrdb's dialect coverage
// can be driven by the corpus the driver ACTUALLY emits against this suite
// rather than by guesses about the dialect in general, and re-deriving that
// corpus is a deliberate act, not something every run pays for.
//
//	GORM_QRDB_SQL_LOG=/tmp/pgsql-corpus.log \
//	GORM_QRDB_TARGET=pgsql go test -tags gormconformance -v -count=1 ./...
//
// Output is one record per statement:
//
//	<status>\t<single-line SQL>
//
// where status is "ok" or "err: <message>". The SQL is the driver's own
// statement text after gorm's Dialector.Explain has substituted bind values
// (that is the only form gorm's logger.Interface is handed), newline-folded
// so one statement is always one line and the file can be counted, sorted,
// and grepped with ordinary line tools.
const sqlLogEnvVar = "GORM_QRDB_SQL_LOG"

// sqlRecorder is a gorm logger.Interface that appends every traced statement
// to a file. One recorder is shared by every *gorm.DB the run opens (upstream
// tests open several), so the file is a complete, ordered record of the whole
// suite's traffic rather than one connection's.
type sqlRecorder struct {
	mu   sync.Mutex
	file *os.File
}

// newSQLRecorder opens path for appending and returns a recorder writing to
// it, or nil (and the error) if the file cannot be created.
func newSQLRecorder(path string) (*sqlRecorder, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return nil, err
	}
	return &sqlRecorder{file: f}, nil
}

// LogMode satisfies logger.Interface. The recorder has a single mode.
func (r *sqlRecorder) LogMode(logger.LogLevel) logger.Interface { return r }

// Info satisfies logger.Interface and discards; the recorder records
// statements only.
func (r *sqlRecorder) Info(context.Context, string, ...interface{}) {}

// Warn satisfies logger.Interface and discards.
func (r *sqlRecorder) Warn(context.Context, string, ...interface{}) {}

// Error satisfies logger.Interface and discards.
func (r *sqlRecorder) Error(context.Context, string, ...interface{}) {}

// Trace records one statement and the error the target returned for it.
func (r *sqlRecorder) Trace(_ context.Context, _ time.Time, fc func() (string, int64), err error) {
	sql, _ := fc()
	status := "ok"
	if err != nil {
		status = "err: " + strings.Join(strings.Fields(err.Error()), " ")
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	_, _ = r.file.WriteString(status + "\t" + strings.Join(strings.Fields(sql), " ") + "\n")
}

// Close flushes and closes the underlying file.
func (r *sqlRecorder) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.file.Close()
}

// recorder is the process-wide statement recorder, non-nil only when
// sqlLogEnvVar is set. TestMain installs it before opening DB and closes it
// after m.Run.
var recorder *sqlRecorder

// installSQLRecorder configures cfg to log through the process-wide recorder
// when one is active, leaving cfg untouched otherwise. A cfg that already
// carries its own Logger is left alone (tracer_test.go installs one and
// asserts on it), so a capture run behaves identically to a normal one and
// its pass/fail counts stay comparable; the cost is that that one test's
// handful of statements are absent from the corpus.
func installSQLRecorder(cfg *gorm.Config) {
	if recorder == nil || cfg == nil || cfg.Logger != nil {
		return
	}
	cfg.Logger = recorder
}
