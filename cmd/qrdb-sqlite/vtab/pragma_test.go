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

package vtab

import (
	"errors"
	"sync"
	"testing"
)

// newTestStore returns a PragmaStore wired with fixed connection metadata so
// tests can assert stable status output.
func newTestStore() *PragmaStore {
	return newPragmaStore("client", "localhost:7070", "testdb")
}

// ---------------------------------------------------------------------------
// setConsistency
// ---------------------------------------------------------------------------

func TestPragma_SetConsistency_Eventual(t *testing.T) {
	p := newTestStore()
	if err := p.setConsistency(ConsistencyEventual); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := p.getConsistency(); got != ConsistencyEventual {
		t.Errorf("consistency = %q, want %q", got, ConsistencyEventual)
	}
}

func TestPragma_SetConsistency_Linearizable(t *testing.T) {
	p := newTestStore()
	// Start from a different value to confirm the write takes effect.
	_ = p.setConsistency(ConsistencyEventual)
	if err := p.setConsistency(ConsistencyLinearizable); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := p.getConsistency(); got != ConsistencyLinearizable {
		t.Errorf("consistency = %q, want %q", got, ConsistencyLinearizable)
	}
}

func TestPragma_SetConsistency_Strong(t *testing.T) {
	p := newTestStore()
	if err := p.setConsistency(ConsistencyStrong); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := p.getConsistency(); got != ConsistencyStrong {
		t.Errorf("consistency = %q, want %q", got, ConsistencyStrong)
	}
}

func TestPragma_SetConsistency_Invalid(t *testing.T) {
	p := newTestStore()
	err := p.setConsistency("read_your_writes")
	if err == nil {
		t.Fatal("expected error for invalid consistency level, got nil")
	}
	var pErr *InvalidPragmaValueError
	if !errors.As(err, &pErr) {
		t.Fatalf("error type = %T, want *InvalidPragmaValueError", err)
	}
	if pErr.Pragma != "qrdb_consistency" {
		t.Errorf("Pragma = %q, want %q", pErr.Pragma, "qrdb_consistency")
	}
	if pErr.Value != "read_your_writes" {
		t.Errorf("Value = %q, want %q", pErr.Value, "read_your_writes")
	}
	if len(pErr.Valid) == 0 {
		t.Error("Valid slice must not be empty")
	}
}

// ---------------------------------------------------------------------------
// setWriteMode
// ---------------------------------------------------------------------------

func TestPragma_SetWriteMode_Sync(t *testing.T) {
	p := newTestStore()
	// Start from a different value to confirm the write takes effect.
	_ = p.setWriteMode(WriteModeWALDurable)
	if err := p.setWriteMode(WriteModeSync); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := p.getWriteMode(); got != WriteModeSync {
		t.Errorf("writeMode = %q, want %q", got, WriteModeSync)
	}
}

func TestPragma_SetWriteMode_WALDurable(t *testing.T) {
	p := newTestStore()
	if err := p.setWriteMode(WriteModeWALDurable); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := p.getWriteMode(); got != WriteModeWALDurable {
		t.Errorf("writeMode = %q, want %q", got, WriteModeWALDurable)
	}
}

func TestPragma_SetWriteMode_Batched(t *testing.T) {
	p := newTestStore()
	if err := p.setWriteMode(WriteModeBatched); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := p.getWriteMode(); got != WriteModeBatched {
		t.Errorf("writeMode = %q, want %q", got, WriteModeBatched)
	}
}

func TestPragma_SetWriteMode_Invalid(t *testing.T) {
	p := newTestStore()
	err := p.setWriteMode("turbo_write")
	if err == nil {
		t.Fatal("expected error for invalid write mode, got nil")
	}
	var pErr *InvalidPragmaValueError
	if !errors.As(err, &pErr) {
		t.Fatalf("error type = %T, want *InvalidPragmaValueError", err)
	}
	if pErr.Pragma != "qrdb_write_mode" {
		t.Errorf("Pragma = %q, want %q", pErr.Pragma, "qrdb_write_mode")
	}
	if pErr.Value != "turbo_write" {
		t.Errorf("Value = %q, want %q", pErr.Value, "turbo_write")
	}
	if len(pErr.Valid) == 0 {
		t.Error("Valid slice must not be empty")
	}
}

// ---------------------------------------------------------------------------
// getters and defaults
// ---------------------------------------------------------------------------

func TestPragma_GetConsistency(t *testing.T) {
	p := newTestStore()
	_ = p.setConsistency(ConsistencyStrong)
	if got := p.getConsistency(); got != ConsistencyStrong {
		t.Errorf("getConsistency = %q, want %q", got, ConsistencyStrong)
	}
}

func TestPragma_GetWriteMode(t *testing.T) {
	p := newTestStore()
	_ = p.setWriteMode(WriteModeBatched)
	if got := p.getWriteMode(); got != WriteModeBatched {
		t.Errorf("getWriteMode = %q, want %q", got, WriteModeBatched)
	}
}

func TestPragma_GetConsistency_Default(t *testing.T) {
	p := newTestStore()
	if got := p.getConsistency(); got != ConsistencyLinearizable {
		t.Errorf("default consistency = %q, want %q", got, ConsistencyLinearizable)
	}
}

func TestPragma_GetWriteMode_Default(t *testing.T) {
	p := newTestStore()
	if got := p.getWriteMode(); got != WriteModeSync {
		t.Errorf("default writeMode = %q, want %q", got, WriteModeSync)
	}
}

// ---------------------------------------------------------------------------
// status
// ---------------------------------------------------------------------------

func TestPragma_Status(t *testing.T) {
	p := newPragmaStore("embedded", "127.0.0.1:9090", "mydb")
	_ = p.setConsistency(ConsistencyStrong)
	_ = p.setWriteMode(WriteModeBatched)

	got := p.status()

	expect := map[string]string{
		"mode":        "embedded",
		"server":      "127.0.0.1:9090",
		"database":    "mydb",
		"consistency": ConsistencyStrong,
		"write_mode":  WriteModeBatched,
	}
	for key, want := range expect {
		if got[key] != want {
			t.Errorf("status[%q] = %q, want %q", key, got[key], want)
		}
	}
}

// ---------------------------------------------------------------------------
// handlePragma
// ---------------------------------------------------------------------------

func TestPragma_HandlePragma_SetConsistency(t *testing.T) {
	p := newTestStore()
	result, err := p.handlePragma("qrdb_consistency", ConsistencyEventual)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result == nil {
		t.Fatal("result map must not be nil")
	}
	if got := result["qrdb_consistency"]; got != ConsistencyEventual {
		t.Errorf("result[qrdb_consistency] = %q, want %q", got, ConsistencyEventual)
	}
	if p.getConsistency() != ConsistencyEventual {
		t.Errorf("store consistency = %q, want %q", p.getConsistency(), ConsistencyEventual)
	}
}

func TestPragma_HandlePragma_GetConsistency(t *testing.T) {
	p := newTestStore()
	_ = p.setConsistency(ConsistencyStrong)

	result, err := p.handlePragma("qrdb_consistency", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result == nil {
		t.Fatal("result map must not be nil")
	}
	if got := result["qrdb_consistency"]; got != ConsistencyStrong {
		t.Errorf("result[qrdb_consistency] = %q, want %q", got, ConsistencyStrong)
	}
}

func TestPragma_HandlePragma_SetWriteMode(t *testing.T) {
	p := newTestStore()
	result, err := p.handlePragma("qrdb_write_mode", WriteModeBatched)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result == nil {
		t.Fatal("result map must not be nil")
	}
	if got := result["qrdb_write_mode"]; got != WriteModeBatched {
		t.Errorf("result[qrdb_write_mode] = %q, want %q", got, WriteModeBatched)
	}
	if p.getWriteMode() != WriteModeBatched {
		t.Errorf("store writeMode = %q, want %q", p.getWriteMode(), WriteModeBatched)
	}
}

func TestPragma_HandlePragma_GetWriteMode(t *testing.T) {
	p := newTestStore()
	_ = p.setWriteMode(WriteModeWALDurable)

	result, err := p.handlePragma("qrdb_write_mode", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result == nil {
		t.Fatal("result map must not be nil")
	}
	if got := result["qrdb_write_mode"]; got != WriteModeWALDurable {
		t.Errorf("result[qrdb_write_mode] = %q, want %q", got, WriteModeWALDurable)
	}
}

func TestPragma_HandlePragma_Status(t *testing.T) {
	p := newPragmaStore("client", "db.example.com:7070", "prod")
	result, err := p.handlePragma("qrdb_status", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result == nil {
		t.Fatal("result map must not be nil")
	}
	requiredKeys := []string{"mode", "server", "database", "consistency", "write_mode"}
	for _, key := range requiredKeys {
		if _, ok := result[key]; !ok {
			t.Errorf("status map missing key %q", key)
		}
	}
	if result["mode"] != "client" {
		t.Errorf("mode = %q, want %q", result["mode"], "client")
	}
	if result["server"] != "db.example.com:7070" {
		t.Errorf("server = %q, want %q", result["server"], "db.example.com:7070")
	}
	if result["database"] != "prod" {
		t.Errorf("database = %q, want %q", result["database"], "prod")
	}
}

func TestPragma_HandlePragma_Unknown(t *testing.T) {
	p := newTestStore()
	result, err := p.handlePragma("journal_mode", "wal")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != nil {
		t.Errorf("expected nil result for unknown PRAGMA, got %v", result)
	}
}

func TestPragma_HandlePragma_InvalidConsistency(t *testing.T) {
	p := newTestStore()
	_, err := p.handlePragma("qrdb_consistency", "bogus")
	if err == nil {
		t.Fatal("expected error for invalid value, got nil")
	}
	var pErr *InvalidPragmaValueError
	if !errors.As(err, &pErr) {
		t.Fatalf("error type = %T, want *InvalidPragmaValueError", err)
	}
}

func TestPragma_HandlePragma_InvalidWriteMode(t *testing.T) {
	p := newTestStore()
	_, err := p.handlePragma("qrdb_write_mode", "bogus")
	if err == nil {
		t.Fatal("expected error for invalid value, got nil")
	}
	var pErr *InvalidPragmaValueError
	if !errors.As(err, &pErr) {
		t.Fatalf("error type = %T, want *InvalidPragmaValueError", err)
	}
}

// ---------------------------------------------------------------------------
// Concurrent access — run with go test -race to exercise the detector
// ---------------------------------------------------------------------------

func TestPragma_ConcurrentAccess(t *testing.T) {
	p := newTestStore()

	const goroutines = 50
	var wg sync.WaitGroup
	wg.Add(goroutines * 2)

	// Writers alternate between two valid consistency levels.
	for i := range goroutines {
		i := i
		go func() {
			defer wg.Done()
			level := ConsistencyEventual
			if i%2 == 0 {
				level = ConsistencyStrong
			}
			if err := p.setConsistency(level); err != nil {
				t.Errorf("setConsistency: %v", err)
			}
		}()
	}

	// Readers call getConsistency and handlePragma concurrently.
	for i := range goroutines {
		i := i
		go func() {
			defer wg.Done()
			if i%3 == 0 {
				_, _ = p.handlePragma("qrdb_status", "")
			} else {
				_ = p.getConsistency()
			}
		}()
	}

	wg.Wait()

	// Final state must still be a valid consistency level.
	final := p.getConsistency()
	if !isValidConsistency(final) {
		t.Errorf("final consistency %q is not a valid level", final)
	}
}
