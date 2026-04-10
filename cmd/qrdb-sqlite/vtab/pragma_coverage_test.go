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

//go:build sqlite_vtable

package vtab

import (
	"errors"
	"testing"
)

// ---------------------------------------------------------------------------
// Exported PragmaStore methods — Consistency, WriteMode, SetConsistency, SetWriteMode
// ---------------------------------------------------------------------------

func TestPragmaStore_Consistency_ReturnsCurrentLevel(t *testing.T) {
	p := NewPragmaStore("client", "localhost:7070", "db")
	// Default should be linearizable.
	if got := p.Consistency(); got != ConsistencyLinearizable {
		t.Errorf("Consistency() = %q, want %q", got, ConsistencyLinearizable)
	}

	if err := p.SetConsistency(ConsistencyStrong); err != nil {
		t.Fatalf("SetConsistency: %v", err)
	}
	if got := p.Consistency(); got != ConsistencyStrong {
		t.Errorf("Consistency() after set = %q, want %q", got, ConsistencyStrong)
	}
}

func TestPragmaStore_Consistency_Invalid(t *testing.T) {
	p := NewPragmaStore("client", "host", "db")
	err := p.SetConsistency("eventual_consistency")
	if err == nil {
		t.Fatal("expected error for invalid consistency, got nil")
	}
	var pErr *InvalidPragmaValueError
	if !errors.As(err, &pErr) {
		t.Fatalf("want *InvalidPragmaValueError, got %T", err)
	}
}

func TestPragmaStore_WriteMode_ReturnsCurrentMode(t *testing.T) {
	p := NewPragmaStore("embedded", "", "local")
	// Default should be sync.
	if got := p.WriteMode(); got != WriteModeSync {
		t.Errorf("WriteMode() = %q, want %q", got, WriteModeSync)
	}

	if err := p.SetWriteMode(WriteModeBatched); err != nil {
		t.Fatalf("SetWriteMode: %v", err)
	}
	if got := p.WriteMode(); got != WriteModeBatched {
		t.Errorf("WriteMode() after set = %q, want %q", got, WriteModeBatched)
	}
}

func TestPragmaStore_WriteMode_Invalid(t *testing.T) {
	p := NewPragmaStore("embedded", "", "local")
	err := p.SetWriteMode("turbo")
	if err == nil {
		t.Fatal("expected error for invalid write mode, got nil")
	}
	var pErr *InvalidPragmaValueError
	if !errors.As(err, &pErr) {
		t.Fatalf("want *InvalidPragmaValueError, got %T", err)
	}
}

func TestPragmaStore_SetConsistency_AllValidValues(t *testing.T) {
	p := NewPragmaStore("test", "host", "db")
	for _, level := range []string{ConsistencyStrong, ConsistencyLinearizable, ConsistencyEventual} {
		if err := p.SetConsistency(level); err != nil {
			t.Errorf("SetConsistency(%q): unexpected error: %v", level, err)
		}
		if got := p.Consistency(); got != level {
			t.Errorf("Consistency() = %q, want %q after SetConsistency", got, level)
		}
	}
}

func TestPragmaStore_SetWriteMode_AllValidValues(t *testing.T) {
	p := NewPragmaStore("test", "host", "db")
	for _, mode := range []string{WriteModeSync, WriteModeWALDurable, WriteModeBatched} {
		if err := p.SetWriteMode(mode); err != nil {
			t.Errorf("SetWriteMode(%q): unexpected error: %v", mode, err)
		}
		if got := p.WriteMode(); got != mode {
			t.Errorf("WriteMode() = %q, want %q after SetWriteMode", got, mode)
		}
	}
}
