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

// Package vtab implements SQLite virtual tables and supporting types for the
// qrdb-sqlite gateway. It bridges SQLite SQL statements to QRDB SDK calls,
// handling PRAGMA-based session configuration and virtual table registration.
package vtab

import "sync"

// Consistency level constants mirror the SDK transport package values so that
// callers in this package never import the transport package directly for
// string comparisons.
const (
	ConsistencyEventual     = "eventual"
	ConsistencyLinearizable = "linearizable"
	ConsistencyStrong       = "strong"
)

// Write mode constants mirror the SDK transport package values for the same
// reason.
const (
	WriteModeSync       = "sync"
	WriteModeWALDurable = "wal_durable"
	WriteModeBatched    = "batched"
)

// validConsistencyLevels is the ordered set of accepted consistency strings
// used both for validation and for error messages.
var validConsistencyLevels = []string{
	ConsistencyStrong,
	ConsistencyLinearizable,
	ConsistencyEventual,
}

// validWriteModes is the ordered set of accepted write mode strings used both
// for validation and for error messages.
var validWriteModes = []string{
	WriteModeSync,
	WriteModeWALDurable,
	WriteModeBatched,
}

// PragmaStore holds session-level PRAGMA settings read by virtual table
// callbacks when constructing SDK options. It is safe for concurrent access
// from multiple SQLite virtual table callbacks.
//
// Use NewPragmaStore to construct an instance with sensible defaults.
type PragmaStore struct {
	mu          sync.RWMutex
	consistency string
	writeMode   string
	mode        string // "embedded" or "client"
	server      string // server address, empty for embedded mode
	database    string // database name
}

// NewPragmaStore creates a PragmaStore with sensible production defaults:
// linearizable consistency and sync write mode. mode, server, and database
// are connection-level metadata surfaced through PRAGMA qrdb_status.
//
// NewPragmaStore is the exported constructor used by the main package to
// build a shared session store before registering virtual table modules.
func NewPragmaStore(mode, server, database string) *PragmaStore {
	return &PragmaStore{
		consistency: ConsistencyLinearizable,
		writeMode:   WriteModeSync,
		mode:        mode,
		server:      server,
		database:    database,
	}
}

// pragmaStore is an unexported type alias for PragmaStore retained so that
// existing internal code and tests that reference pragmaStore continue to
// compile without modification.
type pragmaStore = PragmaStore

// newPragmaStore is the package-internal constructor for pragmaStore (PragmaStore).
func newPragmaStore(mode, server, database string) *PragmaStore {
	return NewPragmaStore(mode, server, database)
}

// setConsistency validates level and, if accepted, atomically replaces the
// current consistency setting. Returns InvalidPragmaValueError for unrecognized
// values.
func (p *PragmaStore) setConsistency(level string) error {
	if !isValidConsistency(level) {
		return &InvalidPragmaValueError{
			Pragma: "qrdb_consistency",
			Value:  level,
			Valid:  validConsistencyLevels,
		}
	}
	p.mu.Lock()
	p.consistency = level
	p.mu.Unlock()
	return nil
}

// getConsistency returns the current consistency level.
func (p *PragmaStore) getConsistency() string {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.consistency
}

// setWriteMode validates mode and, if accepted, atomically replaces the
// current write mode setting. Returns InvalidPragmaValueError for unrecognized
// values.
func (p *PragmaStore) setWriteMode(mode string) error {
	if !isValidWriteMode(mode) {
		return &InvalidPragmaValueError{
			Pragma: "qrdb_write_mode",
			Value:  mode,
			Valid:  validWriteModes,
		}
	}
	p.mu.Lock()
	p.writeMode = mode
	p.mu.Unlock()
	return nil
}

// getWriteMode returns the current write mode.
func (p *PragmaStore) getWriteMode() string {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.writeMode
}

// status returns a snapshot of all session-level and connection-level settings
// as a string map. The returned map contains the keys: mode, server, database,
// consistency, and write_mode.
func (p *PragmaStore) status() map[string]string {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return map[string]string{
		"mode":        p.mode,
		"server":      p.server,
		"database":    p.database,
		"consistency": p.consistency,
		"write_mode":  p.writeMode,
	}
}

// handlePragma dispatches a PRAGMA statement by name. When value is non-empty
// the PRAGMA performs a set operation; otherwise it performs a get. The return
// map contains the current (post-set) value keyed by the pragma name. Unknown
// pragma names return nil, nil so SQLite can apply its own default handling.
//
// Recognized pragma names:
//
//	qrdb_consistency  — consistency level setting
//	qrdb_write_mode   — write mode setting
//	qrdb_status       — full session status (read-only)
func (p *PragmaStore) handlePragma(name, value string) (map[string]string, error) {
	switch name {
	case "qrdb_consistency":
		if value != "" {
			if err := p.setConsistency(value); err != nil {
				return nil, err
			}
		}
		return map[string]string{name: p.getConsistency()}, nil

	case "qrdb_write_mode":
		if value != "" {
			if err := p.setWriteMode(value); err != nil {
				return nil, err
			}
		}
		return map[string]string{name: p.getWriteMode()}, nil

	case "qrdb_status":
		return p.status(), nil

	default:
		return nil, nil
	}
}

// Consistency returns the current consistency level. It is the exported
// counterpart of getConsistency, enabling main-package adapters to read the
// session pragma without importing internal types.
func (p *PragmaStore) Consistency() string { return p.getConsistency() }

// WriteMode returns the current write mode. It is the exported counterpart of
// getWriteMode, enabling main-package adapters to read the session pragma
// without importing internal types.
func (p *PragmaStore) WriteMode() string { return p.getWriteMode() }

// SetConsistency is the exported counterpart of setConsistency. It validates
// level and replaces the current setting, returning an error for unrecognized
// values. The main package calls this to apply flag overrides before opening
// the SQLite database.
func (p *PragmaStore) SetConsistency(level string) error { return p.setConsistency(level) }

// SetWriteMode is the exported counterpart of setWriteMode. It validates mode
// and replaces the current setting, returning an error for unrecognized values.
// The main package calls this to apply flag overrides before opening the SQLite
// database.
func (p *PragmaStore) SetWriteMode(mode string) error { return p.setWriteMode(mode) }

// HandlePragma is the exported counterpart of handlePragma. It dispatches a
// custom qrdb_* PRAGMA by name and returns the resulting key/value pairs: the
// current value on a read, a confirmation on a write, or the full session
// status snapshot for qrdb_status. Callers outside this package (the shell's
// PRAGMA interceptor) use this to route qrdb_* pragmas here instead of
// forwarding them to the underlying SQLite engine, which silently ignores
// unrecognized pragma names.
func (p *PragmaStore) HandlePragma(name, value string) (map[string]string, error) {
	return p.handlePragma(name, value)
}

// isValidConsistency reports whether level is a recognized consistency string.
func isValidConsistency(level string) bool {
	for _, v := range validConsistencyLevels {
		if v == level {
			return true
		}
	}
	return false
}

// isValidWriteMode reports whether mode is a recognized write mode string.
func isValidWriteMode(mode string) bool {
	for _, v := range validWriteModes {
		if v == mode {
			return true
		}
	}
	return false
}
