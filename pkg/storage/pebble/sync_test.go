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

package pebble

import (
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/storage"
)

// TestPebbleStorage_SyncPersistsUnsyncedWrite: a write made with SyncWrites
// off is durable once Sync returns, and survives Close and reopen (#1618).
func TestPebbleStorage_SyncPersistsUnsyncedWrite(t *testing.T) {
	for _, disableWAL := range []bool{false, true} {
		dir := t.TempDir()
		cfg := TestConfig(dir)
		cfg.SyncWrites = false
		cfg.DisableWAL = disableWAL

		s, err := New(cfg)
		if err != nil {
			t.Fatalf("New (disableWAL=%v): %v", disableWAL, err)
		}
		if err := s.Set([]byte("k"), []byte("v")); err != nil {
			t.Fatalf("Set: %v", err)
		}
		if err := s.Sync(); err != nil {
			t.Fatalf("Sync (disableWAL=%v): %v", disableWAL, err)
		}
		if err := s.Close(); err != nil {
			t.Fatalf("Close: %v", err)
		}

		s2, err := New(cfg)
		if err != nil {
			t.Fatalf("reopen: %v", err)
		}
		got, err := s2.Get([]byte("k"))
		_ = s2.Close()
		if err != nil {
			t.Fatalf("Get after reopen: %v", err)
		}
		if string(got) != "v" {
			t.Fatalf("value after reopen (disableWAL=%v) = %q, want %q", disableWAL, got, "v")
		}
	}
}

// TestPebbleStorage_SyncClosed: Sync on a closed store fails like every other
// method does.
func TestPebbleStorage_SyncClosed(t *testing.T) {
	s, err := New(TestConfig(t.TempDir()))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	err = s.Sync()
	var closed *storage.ClosedError
	if !errors.As(err, &closed) {
		t.Fatalf("Sync on closed store = %v, want *storage.ClosedError", err)
	}
}
