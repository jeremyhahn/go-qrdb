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
	"bytes"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/storage"
)

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

// openTestStorage creates a PebbleStorage in a temp directory for testing.
func openTestStorage(t *testing.T) *PebbleStorage {
	t.Helper()
	s, err := New(TestConfig(t.TempDir()))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// ---------------------------------------------------------------------------
// Config tests
// ---------------------------------------------------------------------------

func TestConfig_Validate_MissingDataDir(t *testing.T) {
	cfg := &Config{}
	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected error for missing DataDir")
	}
	var ce *ConfigError
	if !errors.As(err, &ce) {
		t.Fatalf("expected *ConfigError, got %T", err)
	}
	if ce.Field != "DataDir" {
		t.Fatalf("expected field DataDir, got %s", ce.Field)
	}
}

func TestConfig_Validate_NegativeCacheSize(t *testing.T) {
	cfg := &Config{DataDir: "/tmp/test", CacheSize: -1}
	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected error for negative CacheSize")
	}
	var ce *ConfigError
	if !errors.As(err, &ce) {
		t.Fatalf("expected *ConfigError, got %T", err)
	}
	if ce.Field != "CacheSize" {
		t.Fatalf("expected field CacheSize, got %s", ce.Field)
	}
}

func TestConfig_Validate_NegativeFields(t *testing.T) {
	tests := []struct {
		name  string
		cfg   Config
		field string
	}{
		{"MaxOpenFiles", Config{DataDir: "/tmp", MaxOpenFiles: -1}, "MaxOpenFiles"},
		{"MaxConcurrentCompactions", Config{DataDir: "/tmp", MaxConcurrentCompactions: -1}, "MaxConcurrentCompactions"},
		{"BytesPerSync", Config{DataDir: "/tmp", BytesPerSync: -1}, "BytesPerSync"},
		{"MemTableSize", Config{DataDir: "/tmp", MemTableSize: -1}, "MemTableSize"},
		{"MemTableStopWritesThreshold", Config{DataDir: "/tmp", MemTableStopWritesThreshold: -1}, "MemTableStopWritesThreshold"},
		{"L0CompactionThreshold", Config{DataDir: "/tmp", L0CompactionThreshold: -1}, "L0CompactionThreshold"},
		{"L0StopWritesThreshold", Config{DataDir: "/tmp", L0StopWritesThreshold: -1}, "L0StopWritesThreshold"},
		{"BlockSize", Config{DataDir: "/tmp", BlockSize: -1}, "BlockSize"},
		{"MaxConcurrentReads", Config{DataDir: "/tmp", MaxConcurrentReads: -1}, "MaxConcurrentReads"},
		{"MaxConcurrentWrites", Config{DataDir: "/tmp", MaxConcurrentWrites: -1}, "MaxConcurrentWrites"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.cfg.Validate()
			if err == nil {
				t.Fatalf("expected error for negative %s", tc.field)
			}
			var ce *ConfigError
			if !errors.As(err, &ce) {
				t.Fatalf("expected *ConfigError, got %T", err)
			}
			if ce.Field != tc.field {
				t.Fatalf("expected field %s, got %s", tc.field, ce.Field)
			}
		})
	}
}

func TestConfig_Validate_InvalidCompression(t *testing.T) {
	cfg := &Config{DataDir: "/tmp/test", Compression: "lz4"}
	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected error for invalid compression")
	}
	var ce *ConfigError
	if !errors.As(err, &ce) {
		t.Fatalf("expected *ConfigError, got %T", err)
	}
	if ce.Field != "Compression" {
		t.Fatalf("expected field Compression, got %s", ce.Field)
	}
}

func TestConfig_Validate_ValidCompression(t *testing.T) {
	for _, ct := range []CompressionType{CompressionNone, CompressionSnappy, CompressionZstd, ""} {
		cfg := &Config{DataDir: "/tmp/test", Compression: ct}
		if err := cfg.Validate(); err != nil {
			t.Fatalf("unexpected error for compression %q: %v", ct, err)
		}
	}
}

func TestConfig_WithDefaults(t *testing.T) {
	cfg := (&Config{DataDir: "/tmp/test"}).WithDefaults()

	if cfg.CacheSize == 0 {
		t.Fatal("CacheSize should have a default")
	}
	if cfg.MaxOpenFiles == 0 {
		t.Fatal("MaxOpenFiles should have a default")
	}
	if cfg.MaxConcurrentCompactions == 0 {
		t.Fatal("MaxConcurrentCompactions should have a default")
	}
	if cfg.Compression == "" {
		t.Fatal("Compression should have a default")
	}
	if cfg.MaxConcurrentReads == 0 {
		t.Fatal("MaxConcurrentReads should have a default")
	}
	if cfg.MaxConcurrentWrites == 0 {
		t.Fatal("MaxConcurrentWrites should have a default")
	}
}

func TestConfig_WithDefaults_PreservesExplicitValues(t *testing.T) {
	cfg := (&Config{
		DataDir:            "/tmp/test",
		CacheSize:          42,
		MaxConcurrentReads: 7,
	}).WithDefaults()

	if cfg.CacheSize != 42 {
		t.Fatalf("CacheSize: got %d, want 42", cfg.CacheSize)
	}
	if cfg.MaxConcurrentReads != 7 {
		t.Fatalf("MaxConcurrentReads: got %d, want 7", cfg.MaxConcurrentReads)
	}
}

func TestConfig_ToPebbleOptions(t *testing.T) {
	cfg := TestConfig(t.TempDir())
	opts := cfg.ToPebbleOptions()
	if opts == nil {
		t.Fatal("ToPebbleOptions returned nil")
	}
	if opts.MaxOpenFiles != cfg.MaxOpenFiles {
		t.Fatalf("MaxOpenFiles: got %d, want %d", opts.MaxOpenFiles, cfg.MaxOpenFiles)
	}
}

func TestDefaultConfig(t *testing.T) {
	cfg := DefaultConfig("/tmp/test")
	if err := cfg.Validate(); err != nil {
		t.Fatalf("DefaultConfig is invalid: %v", err)
	}
	if cfg.CacheSize == 0 {
		t.Fatal("DefaultConfig CacheSize should be set")
	}
}

func TestTestConfig(t *testing.T) {
	cfg := TestConfig("/tmp/test")
	if err := cfg.Validate(); err != nil {
		t.Fatalf("TestConfig is invalid: %v", err)
	}
	if cfg.CacheSize == 0 {
		t.Fatal("TestConfig CacheSize should be set")
	}
}

func TestConfigError_Error(t *testing.T) {
	e := &ConfigError{Field: "DataDir", Message: "is required"}
	want := "pebble config: DataDir: is required"
	if e.Error() != want {
		t.Fatalf("Error(): got %q, want %q", e.Error(), want)
	}
}

// ---------------------------------------------------------------------------
// New / Open tests
// ---------------------------------------------------------------------------

func TestNew_NilConfig(t *testing.T) {
	_, err := New(nil)
	if err == nil {
		t.Fatal("expected error for nil config")
	}
	var ce *ConfigError
	if !errors.As(err, &ce) {
		t.Fatalf("expected *ConfigError, got %T", err)
	}
}

func TestNew_InvalidConfig(t *testing.T) {
	_, err := New(&Config{})
	if err == nil {
		t.Fatal("expected error for empty DataDir")
	}
}

func TestNew_InvalidPath(t *testing.T) {
	// Use a path through /dev/null (a character device, not a directory).
	// The OS cannot create subdirectories under a device file, so this
	// fails for both root and non-root users.
	_, err := New(&Config{DataDir: "/dev/null/impossible/path/db"})
	if err == nil {
		t.Fatal("expected error for invalid path")
	}
	var opErr *storage.OpError
	if !errors.As(err, &opErr) {
		t.Fatalf("expected *storage.OpError, got %T", err)
	}
}

func TestNew_Success(t *testing.T) {
	s := openTestStorage(t)
	if s == nil {
		t.Fatal("expected non-nil storage")
	}
}

// ---------------------------------------------------------------------------
// Get / Set / Delete
// ---------------------------------------------------------------------------

func TestGetSet(t *testing.T) {
	s := openTestStorage(t)

	if err := s.Set([]byte("key1"), []byte("value1")); err != nil {
		t.Fatalf("Set: %v", err)
	}

	got, err := s.Get([]byte("key1"))
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if string(got) != "value1" {
		t.Fatalf("Get: got %q, want %q", got, "value1")
	}
}

func TestGet_NotFound(t *testing.T) {
	s := openTestStorage(t)

	got, err := s.Get([]byte("missing"))
	if err != nil {
		t.Fatalf("Get missing: unexpected error: %v", err)
	}
	if got != nil {
		t.Fatalf("Get missing: got %v, want nil", got)
	}
}

func TestGet_EmptyKey(t *testing.T) {
	s := openTestStorage(t)

	_, err := s.Get([]byte{})
	if !errors.Is(err, storage.ErrEmptyKey) {
		t.Fatalf("Get empty key: got %v, want ErrEmptyKey", err)
	}
}

func TestGet_NilKey(t *testing.T) {
	s := openTestStorage(t)

	_, err := s.Get(nil)
	if !errors.Is(err, storage.ErrEmptyKey) {
		t.Fatalf("Get nil key: got %v, want ErrEmptyKey", err)
	}
}

func TestSet_EmptyKey(t *testing.T) {
	s := openTestStorage(t)

	err := s.Set([]byte{}, []byte("value"))
	if !errors.Is(err, storage.ErrEmptyKey) {
		t.Fatalf("Set empty key: got %v, want ErrEmptyKey", err)
	}
}

func TestSet_Overwrite(t *testing.T) {
	s := openTestStorage(t)

	if err := s.Set([]byte("key"), []byte("v1")); err != nil {
		t.Fatalf("Set v1: %v", err)
	}
	if err := s.Set([]byte("key"), []byte("v2")); err != nil {
		t.Fatalf("Set v2: %v", err)
	}

	got, err := s.Get([]byte("key"))
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if string(got) != "v2" {
		t.Fatalf("Get: got %q, want %q", got, "v2")
	}
}

func TestSet_EmptyValue(t *testing.T) {
	s := openTestStorage(t)

	if err := s.Set([]byte("key"), []byte{}); err != nil {
		t.Fatalf("Set empty value: %v", err)
	}

	got, err := s.Get([]byte("key"))
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("Get: got %v, want empty", got)
	}
}

func TestSet_NilValue(t *testing.T) {
	s := openTestStorage(t)

	if err := s.Set([]byte("key"), nil); err != nil {
		t.Fatalf("Set nil value: %v", err)
	}

	got, err := s.Get([]byte("key"))
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("Get: got %v, want empty", got)
	}
}

func TestDelete(t *testing.T) {
	s := openTestStorage(t)

	if err := s.Set([]byte("key1"), []byte("value1")); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if err := s.Delete([]byte("key1")); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	got, err := s.Get([]byte("key1"))
	if err != nil {
		t.Fatalf("Get after Delete: %v", err)
	}
	if got != nil {
		t.Fatalf("Get after Delete: got %v, want nil", got)
	}
}

func TestDelete_NonExistent(t *testing.T) {
	s := openTestStorage(t)

	if err := s.Delete([]byte("nonexistent")); err != nil {
		t.Fatalf("Delete nonexistent: unexpected error: %v", err)
	}
}

func TestDelete_EmptyKey(t *testing.T) {
	s := openTestStorage(t)

	err := s.Delete([]byte{})
	if !errors.Is(err, storage.ErrEmptyKey) {
		t.Fatalf("Delete empty key: got %v, want ErrEmptyKey", err)
	}
}

// ---------------------------------------------------------------------------
// List
// ---------------------------------------------------------------------------

func TestPebbleStorage_List(t *testing.T) {
	s := openTestStorage(t)

	// Populate keys across two prefixes and one standalone.
	data := map[string]string{
		"app/user/1": "u1",
		"app/user/2": "u2",
		"app/user/3": "u3",
		"app/role/1": "r1",
		"other/key":  "o1",
	}
	for k, v := range data {
		if err := s.Set([]byte(k), []byte(v)); err != nil {
			t.Fatalf("Set %q: %v", k, err)
		}
	}

	// List with prefix returns only matching keys.
	keys, err := s.List([]byte("app/user/"))
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(keys) != 3 {
		t.Fatalf("List app/user/: got %d keys, want 3", len(keys))
	}
	for _, k := range keys {
		if !bytes.HasPrefix(k, []byte("app/user/")) {
			t.Fatalf("unexpected key %q", k)
		}
	}

	// List with nil prefix returns all keys.
	allKeys, err := s.List(nil)
	if err != nil {
		t.Fatalf("List nil: %v", err)
	}
	if len(allKeys) != len(data) {
		t.Fatalf("List nil: got %d keys, want %d", len(allKeys), len(data))
	}

	// List with empty prefix returns all keys.
	allKeys2, err := s.List([]byte{})
	if err != nil {
		t.Fatalf("List empty: %v", err)
	}
	if len(allKeys2) != len(data) {
		t.Fatalf("List empty: got %d keys, want %d", len(allKeys2), len(data))
	}

	// List with prefix that matches nothing returns nil slice.
	noKeys, err := s.List([]byte("nonexistent/"))
	if err != nil {
		t.Fatalf("List nonexistent: %v", err)
	}
	if noKeys != nil {
		t.Fatalf("List nonexistent: got %v, want nil", noKeys)
	}
}

func TestPebbleStorage_List_Closed(t *testing.T) {
	s := openTestStorage(t)
	_ = s.Close()

	_, err := s.List([]byte("prefix"))
	var ce *storage.ClosedError
	if !errors.As(err, &ce) {
		t.Fatalf("List after close: got %T, want *storage.ClosedError", err)
	}
}

// ---------------------------------------------------------------------------
// Exists
// ---------------------------------------------------------------------------

func TestPebbleStorage_Exists(t *testing.T) {
	s := openTestStorage(t)

	if err := s.Set([]byte("present"), []byte("val")); err != nil {
		t.Fatalf("Set: %v", err)
	}

	// Key that exists.
	exists, err := s.Exists([]byte("present"))
	if err != nil {
		t.Fatalf("Exists present: %v", err)
	}
	if !exists {
		t.Fatal("Exists present: got false, want true")
	}

	// Key that does not exist returns false without error.
	exists, err = s.Exists([]byte("missing"))
	if err != nil {
		t.Fatalf("Exists missing: %v", err)
	}
	if exists {
		t.Fatal("Exists missing: got true, want false")
	}
}

func TestPebbleStorage_Exists_EmptyKey(t *testing.T) {
	s := openTestStorage(t)

	_, err := s.Exists([]byte{})
	if !errors.Is(err, storage.ErrEmptyKey) {
		t.Fatalf("Exists empty key: got %v, want ErrEmptyKey", err)
	}

	_, err = s.Exists(nil)
	if !errors.Is(err, storage.ErrEmptyKey) {
		t.Fatalf("Exists nil key: got %v, want ErrEmptyKey", err)
	}
}

func TestPebbleStorage_Exists_Closed(t *testing.T) {
	s := openTestStorage(t)
	_ = s.Close()

	_, err := s.Exists([]byte("key"))
	var ce *storage.ClosedError
	if !errors.As(err, &ce) {
		t.Fatalf("Exists after close: got %T, want *storage.ClosedError", err)
	}
}

// ---------------------------------------------------------------------------
// Get returns copy
// ---------------------------------------------------------------------------

func TestGet_ReturnsCopy(t *testing.T) {
	s := openTestStorage(t)

	if err := s.Set([]byte("key"), []byte("original")); err != nil {
		t.Fatalf("Set: %v", err)
	}

	got1, err := s.Get([]byte("key"))
	if err != nil {
		t.Fatalf("Get1: %v", err)
	}

	// Mutate the returned slice.
	got1[0] = 'X'

	got2, err := s.Get([]byte("key"))
	if err != nil {
		t.Fatalf("Get2: %v", err)
	}
	if string(got2) != "original" {
		t.Fatalf("data isolation: stored data was mutated, got %q", got2)
	}
}

// ---------------------------------------------------------------------------
// Closed storage errors
// ---------------------------------------------------------------------------

func TestClosed_Get(t *testing.T) {
	s := openTestStorage(t)
	_ = s.Close()

	_, err := s.Get([]byte("key"))
	var ce *storage.ClosedError
	if !errors.As(err, &ce) {
		t.Fatalf("Get after close: got %T, want *storage.ClosedError", err)
	}
}

func TestClosed_Set(t *testing.T) {
	s := openTestStorage(t)
	_ = s.Close()

	err := s.Set([]byte("key"), []byte("val"))
	var ce *storage.ClosedError
	if !errors.As(err, &ce) {
		t.Fatalf("Set after close: got %T, want *storage.ClosedError", err)
	}
}

func TestClosed_Delete(t *testing.T) {
	s := openTestStorage(t)
	_ = s.Close()

	err := s.Delete([]byte("key"))
	var ce *storage.ClosedError
	if !errors.As(err, &ce) {
		t.Fatalf("Delete after close: got %T, want *storage.ClosedError", err)
	}
}

func TestClosed_Snapshot(t *testing.T) {
	s := openTestStorage(t)
	_ = s.Close()

	_, err := s.Snapshot()
	var ce *storage.ClosedError
	if !errors.As(err, &ce) {
		t.Fatalf("Snapshot after close: got %T, want *storage.ClosedError", err)
	}
}

func TestClosed_Compact(t *testing.T) {
	s := openTestStorage(t)
	_ = s.Close()

	err := s.Compact(nil, nil)
	var ce *storage.ClosedError
	if !errors.As(err, &ce) {
		t.Fatalf("Compact after close: got %T, want *storage.ClosedError", err)
	}
}

func TestClosed_Flush(t *testing.T) {
	s := openTestStorage(t)
	_ = s.Close()

	err := s.Flush()
	var ce *storage.ClosedError
	if !errors.As(err, &ce) {
		t.Fatalf("Flush after close: got %T, want *storage.ClosedError", err)
	}
}

func TestClose_Idempotent(t *testing.T) {
	s := openTestStorage(t)

	if err := s.Close(); err != nil {
		t.Fatalf("Close 1: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close 2: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Batch tests
// ---------------------------------------------------------------------------

func TestBatch_SetCommit(t *testing.T) {
	s := openTestStorage(t)

	b := s.Batch()
	if err := b.Set([]byte("k1"), []byte("v1")); err != nil {
		t.Fatalf("Batch.Set: %v", err)
	}
	if err := b.Set([]byte("k2"), []byte("v2")); err != nil {
		t.Fatalf("Batch.Set: %v", err)
	}
	if err := b.Commit(); err != nil {
		t.Fatalf("Batch.Commit: %v", err)
	}

	got, err := s.Get([]byte("k1"))
	if err != nil {
		t.Fatalf("Get k1: %v", err)
	}
	if string(got) != "v1" {
		t.Fatalf("Get k1: got %q, want %q", got, "v1")
	}

	got, err = s.Get([]byte("k2"))
	if err != nil {
		t.Fatalf("Get k2: %v", err)
	}
	if string(got) != "v2" {
		t.Fatalf("Get k2: got %q, want %q", got, "v2")
	}
}

func TestBatch_DeleteCommit(t *testing.T) {
	s := openTestStorage(t)

	if err := s.Set([]byte("k1"), []byte("v1")); err != nil {
		t.Fatalf("Set: %v", err)
	}

	b := s.Batch()
	if err := b.Delete([]byte("k1")); err != nil {
		t.Fatalf("Batch.Delete: %v", err)
	}
	if err := b.Commit(); err != nil {
		t.Fatalf("Batch.Commit: %v", err)
	}

	got, err := s.Get([]byte("k1"))
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got != nil {
		t.Fatalf("Get after batch delete: got %v, want nil", got)
	}
}

func TestBatch_Rollback(t *testing.T) {
	s := openTestStorage(t)

	b := s.Batch()
	if err := b.Set([]byte("k1"), []byte("v1")); err != nil {
		t.Fatalf("Batch.Set: %v", err)
	}
	if err := b.Rollback(); err != nil {
		t.Fatalf("Batch.Rollback: %v", err)
	}

	got, err := s.Get([]byte("k1"))
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got != nil {
		t.Fatalf("Get after rollback: got %v, want nil", got)
	}
}

func TestBatch_Close_ImplicitRollback(t *testing.T) {
	s := openTestStorage(t)

	b := s.Batch()
	if err := b.Set([]byte("k1"), []byte("v1")); err != nil {
		t.Fatalf("Batch.Set: %v", err)
	}
	if err := b.Close(); err != nil {
		t.Fatalf("Batch.Close: %v", err)
	}

	got, err := s.Get([]byte("k1"))
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got != nil {
		t.Fatalf("Get after close (implicit rollback): got %v, want nil", got)
	}
}

func TestBatch_Close_Idempotent(t *testing.T) {
	s := openTestStorage(t)

	b := s.Batch()
	if err := b.Close(); err != nil {
		t.Fatalf("Close 1: %v", err)
	}
	if err := b.Close(); err != nil {
		t.Fatalf("Close 2: %v", err)
	}
}

func TestBatch_SetAfterCommit(t *testing.T) {
	s := openTestStorage(t)

	b := s.Batch()
	if err := b.Commit(); err != nil {
		t.Fatalf("Commit: %v", err)
	}

	err := b.Set([]byte("k1"), []byte("v1"))
	if err == nil {
		t.Fatal("expected error for Set after Commit")
	}
	var be *storage.BatchError
	if !errors.As(err, &be) {
		t.Fatalf("expected *storage.BatchError, got %T", err)
	}
}

func TestBatch_CommitAfterCommit(t *testing.T) {
	s := openTestStorage(t)

	b := s.Batch()
	if err := b.Commit(); err != nil {
		t.Fatalf("Commit 1: %v", err)
	}

	err := b.Commit()
	if err == nil {
		t.Fatal("expected error for double Commit")
	}
	var be *storage.BatchError
	if !errors.As(err, &be) {
		t.Fatalf("expected *storage.BatchError, got %T", err)
	}
}

func TestBatch_RollbackAfterCommit(t *testing.T) {
	s := openTestStorage(t)

	b := s.Batch()
	if err := b.Commit(); err != nil {
		t.Fatalf("Commit: %v", err)
	}

	err := b.Rollback()
	if err == nil {
		t.Fatal("expected error for Rollback after Commit")
	}
	var be *storage.BatchError
	if !errors.As(err, &be) {
		t.Fatalf("expected *storage.BatchError, got %T", err)
	}
}

func TestBatch_DeleteAfterRollback(t *testing.T) {
	s := openTestStorage(t)

	b := s.Batch()
	if err := b.Rollback(); err != nil {
		t.Fatalf("Rollback: %v", err)
	}

	err := b.Delete([]byte("k1"))
	if err == nil {
		t.Fatal("expected error for Delete after Rollback")
	}
	var be *storage.BatchError
	if !errors.As(err, &be) {
		t.Fatalf("expected *storage.BatchError, got %T", err)
	}
}

func TestBatch_Set_EmptyKey(t *testing.T) {
	s := openTestStorage(t)

	b := s.Batch()
	defer func() { _ = b.Close() }()

	err := b.Set([]byte{}, []byte("val"))
	if err == nil {
		t.Fatal("expected error for empty key")
	}
	var be *storage.BatchError
	if !errors.As(err, &be) {
		t.Fatalf("expected *storage.BatchError, got %T", err)
	}
}

func TestBatch_Delete_EmptyKey(t *testing.T) {
	s := openTestStorage(t)

	b := s.Batch()
	defer func() { _ = b.Close() }()

	err := b.Delete([]byte{})
	if err == nil {
		t.Fatal("expected error for empty key")
	}
	var be *storage.BatchError
	if !errors.As(err, &be) {
		t.Fatalf("expected *storage.BatchError, got %T", err)
	}
}

// ---------------------------------------------------------------------------
// Iterator tests
// ---------------------------------------------------------------------------

// TestNewFromDB_RoundTrip verifies that NewFromDB wraps the given *pebble.DB
// and that DB() returns the same instance.
func TestNewFromDB_RoundTrip(t *testing.T) {
	db := openTestStorage(t).DB()

	s := NewFromDB(db, true)
	if s == nil {
		t.Fatal("NewFromDB returned nil")
	}
	if got := s.DB(); got != db {
		t.Fatalf("DB(): got %p, want %p", got, db)
	}
}

func TestIterator_ForwardScan(t *testing.T) {
	s := openTestStorage(t)

	keys := []string{"a", "b", "c", "d", "e"}
	for _, k := range keys {
		if err := s.Set([]byte(k), []byte("val-"+k)); err != nil {
			t.Fatalf("Set %q: %v", k, err)
		}
	}

	iter := s.NewIterator(nil)
	defer func() { _ = iter.Close() }()

	var collected []string
	for ok := iter.SeekToFirst(); ok; ok = iter.Next() {
		collected = append(collected, string(iter.Key()))
	}
	if err := iter.Error(); err != nil {
		t.Fatalf("Iterator error: %v", err)
	}
	if len(collected) != len(keys) {
		t.Fatalf("forward scan: got %d keys, want %d", len(collected), len(keys))
	}
	for i, k := range keys {
		if collected[i] != k {
			t.Fatalf("forward scan[%d]: got %q, want %q", i, collected[i], k)
		}
	}
}

func TestIterator_ReverseScan(t *testing.T) {
	s := openTestStorage(t)

	keys := []string{"a", "b", "c"}
	for _, k := range keys {
		if err := s.Set([]byte(k), []byte("val")); err != nil {
			t.Fatalf("Set %q: %v", k, err)
		}
	}

	iter := s.NewIterator(nil)
	defer func() { _ = iter.Close() }()

	var collected []string
	for ok := iter.SeekToLast(); ok; ok = iter.Prev() {
		collected = append(collected, string(iter.Key()))
	}
	if err := iter.Error(); err != nil {
		t.Fatalf("Iterator error: %v", err)
	}
	if len(collected) != 3 {
		t.Fatalf("reverse scan: got %d keys, want 3", len(collected))
	}
	if collected[0] != "c" || collected[1] != "b" || collected[2] != "a" {
		t.Fatalf("reverse scan: got %v, want [c b a]", collected)
	}
}

func TestIterator_Seek(t *testing.T) {
	s := openTestStorage(t)

	for _, k := range []string{"a", "c", "e"} {
		if err := s.Set([]byte(k), []byte("val")); err != nil {
			t.Fatalf("Set: %v", err)
		}
	}

	iter := s.NewIterator(nil)
	defer func() { _ = iter.Close() }()

	// Seek to "b" should land on "c" (first key >= "b").
	if !iter.Seek([]byte("b")) {
		t.Fatal("Seek to b: expected valid position")
	}
	if string(iter.Key()) != "c" {
		t.Fatalf("Seek to b: got %q, want %q", iter.Key(), "c")
	}
}

func TestIterator_Bounds(t *testing.T) {
	s := openTestStorage(t)

	for _, k := range []string{"a", "b", "c", "d", "e"} {
		if err := s.Set([]byte(k), []byte("val")); err != nil {
			t.Fatalf("Set: %v", err)
		}
	}

	iter := s.NewIterator(&storage.IteratorOptions{
		LowerBound: []byte("b"),
		UpperBound: []byte("d"),
	})
	defer func() { _ = iter.Close() }()

	var collected []string
	for ok := iter.SeekToFirst(); ok; ok = iter.Next() {
		collected = append(collected, string(iter.Key()))
	}
	// "b" and "c" (UpperBound "d" is exclusive).
	if len(collected) != 2 {
		t.Fatalf("bounded scan: got %v, want [b c]", collected)
	}
	if collected[0] != "b" || collected[1] != "c" {
		t.Fatalf("bounded scan: got %v, want [b c]", collected)
	}
}

func TestIterator_Empty(t *testing.T) {
	s := openTestStorage(t)

	iter := s.NewIterator(nil)
	defer func() { _ = iter.Close() }()

	if iter.SeekToFirst() {
		t.Fatal("SeekToFirst on empty: expected false")
	}
	if iter.SeekToLast() {
		t.Fatal("SeekToLast on empty: expected false")
	}
	if iter.Valid() {
		t.Fatal("Valid on empty: expected false")
	}
	if iter.Key() != nil {
		t.Fatal("Key on empty: expected nil")
	}
	if iter.Value() != nil {
		t.Fatal("Value on empty: expected nil")
	}
}

func TestIterator_NilOpts(t *testing.T) {
	s := openTestStorage(t)
	if err := s.Set([]byte("k"), []byte("v")); err != nil {
		t.Fatalf("Set: %v", err)
	}

	iter := s.NewIterator(nil)
	defer func() { _ = iter.Close() }()

	if !iter.SeekToFirst() {
		t.Fatal("SeekToFirst: expected true")
	}
	if string(iter.Key()) != "k" {
		t.Fatalf("Key: got %q, want %q", iter.Key(), "k")
	}
}

func TestIterator_Value(t *testing.T) {
	s := openTestStorage(t)
	if err := s.Set([]byte("k"), []byte("myvalue")); err != nil {
		t.Fatalf("Set: %v", err)
	}

	iter := s.NewIterator(nil)
	defer func() { _ = iter.Close() }()

	if !iter.SeekToFirst() {
		t.Fatal("SeekToFirst: expected true")
	}
	if string(iter.Value()) != "myvalue" {
		t.Fatalf("Value: got %q, want %q", iter.Value(), "myvalue")
	}
}

func TestIterator_KeyValueCopy(t *testing.T) {
	s := openTestStorage(t)
	if err := s.Set([]byte("k1"), []byte("v1")); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if err := s.Set([]byte("k2"), []byte("v2")); err != nil {
		t.Fatalf("Set: %v", err)
	}

	iter := s.NewIterator(nil)
	defer func() { _ = iter.Close() }()

	iter.SeekToFirst()
	key1 := iter.Key()
	val1 := iter.Value()

	// Move to next entry.
	iter.Next()

	// key1 and val1 should still hold the original values.
	if string(key1) != "k1" {
		t.Fatalf("key copy: got %q, want %q", key1, "k1")
	}
	if string(val1) != "v1" {
		t.Fatalf("value copy: got %q, want %q", val1, "v1")
	}
}

func TestIterator_Close_Idempotent(t *testing.T) {
	s := openTestStorage(t)

	iter := s.NewIterator(nil)
	if err := iter.Close(); err != nil {
		t.Fatalf("Close 1: %v", err)
	}
	if err := iter.Close(); err != nil {
		t.Fatalf("Close 2: %v", err)
	}
}

func TestIterator_OperationsAfterClose(t *testing.T) {
	s := openTestStorage(t)
	if err := s.Set([]byte("k"), []byte("v")); err != nil {
		t.Fatalf("Set: %v", err)
	}

	iter := s.NewIterator(nil)
	_ = iter.Close()

	if iter.SeekToFirst() {
		t.Fatal("SeekToFirst after Close: expected false")
	}
	if iter.SeekToLast() {
		t.Fatal("SeekToLast after Close: expected false")
	}
	if iter.Seek([]byte("k")) {
		t.Fatal("Seek after Close: expected false")
	}
	if iter.Next() {
		t.Fatal("Next after Close: expected false")
	}
	if iter.Prev() {
		t.Fatal("Prev after Close: expected false")
	}
	if iter.Valid() {
		t.Fatal("Valid after Close: expected false")
	}
	if iter.Key() != nil {
		t.Fatal("Key after Close: expected nil")
	}
	if iter.Value() != nil {
		t.Fatal("Value after Close: expected nil")
	}
}

// ---------------------------------------------------------------------------
// Snapshot tests
// ---------------------------------------------------------------------------

func TestSnapshot_Isolation(t *testing.T) {
	s := openTestStorage(t)

	if err := s.Set([]byte("key"), []byte("before")); err != nil {
		t.Fatalf("Set before: %v", err)
	}

	snap, err := s.Snapshot()
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	defer func() { _ = snap.Close() }()

	// Write after snapshot.
	if err := s.Set([]byte("key"), []byte("after")); err != nil {
		t.Fatalf("Set after: %v", err)
	}

	// Snapshot sees old value.
	got, err := snap.Get([]byte("key"))
	if err != nil {
		t.Fatalf("Snapshot.Get: %v", err)
	}
	if string(got) != "before" {
		t.Fatalf("Snapshot.Get: got %q, want %q", got, "before")
	}

	// Live storage sees new value.
	got, err = s.Get([]byte("key"))
	if err != nil {
		t.Fatalf("Storage.Get: %v", err)
	}
	if string(got) != "after" {
		t.Fatalf("Storage.Get: got %q, want %q", got, "after")
	}
}

func TestSnapshot_NotFound(t *testing.T) {
	s := openTestStorage(t)

	snap, err := s.Snapshot()
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	defer func() { _ = snap.Close() }()

	got, err := snap.Get([]byte("missing"))
	if err != nil {
		t.Fatalf("Snapshot.Get missing: unexpected error: %v", err)
	}
	if got != nil {
		t.Fatalf("Snapshot.Get missing: got %v, want nil", got)
	}
}

func TestSnapshot_EmptyKey(t *testing.T) {
	s := openTestStorage(t)

	snap, err := s.Snapshot()
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	defer func() { _ = snap.Close() }()

	_, err = snap.Get([]byte{})
	if err == nil {
		t.Fatal("expected error for empty key")
	}
	var se *storage.SnapshotError
	if !errors.As(err, &se) {
		t.Fatalf("expected *storage.SnapshotError, got %T", err)
	}
}

func TestSnapshot_Iterator(t *testing.T) {
	s := openTestStorage(t)

	if err := s.Set([]byte("a"), []byte("1")); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if err := s.Set([]byte("b"), []byte("2")); err != nil {
		t.Fatalf("Set: %v", err)
	}

	snap, err := s.Snapshot()
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	defer func() { _ = snap.Close() }()

	// Add more data after snapshot.
	if err := s.Set([]byte("c"), []byte("3")); err != nil {
		t.Fatalf("Set: %v", err)
	}

	iter := snap.NewIterator(nil)
	defer func() { _ = iter.Close() }()

	var collected []string
	for ok := iter.SeekToFirst(); ok; ok = iter.Next() {
		collected = append(collected, string(iter.Key()))
	}

	// Snapshot should only see "a" and "b".
	if len(collected) != 2 {
		t.Fatalf("snapshot iterator: got %v, want [a b]", collected)
	}
	if collected[0] != "a" || collected[1] != "b" {
		t.Fatalf("snapshot iterator: got %v, want [a b]", collected)
	}
}

func TestSnapshot_Close_Idempotent(t *testing.T) {
	s := openTestStorage(t)

	snap, err := s.Snapshot()
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}

	if err := snap.Close(); err != nil {
		t.Fatalf("Close 1: %v", err)
	}
	if err := snap.Close(); err != nil {
		t.Fatalf("Close 2: %v", err)
	}
}

func TestSnapshot_GetAfterClose(t *testing.T) {
	s := openTestStorage(t)

	if err := s.Set([]byte("k"), []byte("v")); err != nil {
		t.Fatalf("Set: %v", err)
	}

	snap, err := s.Snapshot()
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	_ = snap.Close()

	_, err = snap.Get([]byte("k"))
	if err == nil {
		t.Fatal("expected error for Get after Close")
	}
	var se *storage.SnapshotError
	if !errors.As(err, &se) {
		t.Fatalf("expected *storage.SnapshotError, got %T", err)
	}
}

func TestSnapshot_IteratorAfterClose(t *testing.T) {
	s := openTestStorage(t)

	snap, err := s.Snapshot()
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	_ = snap.Close()

	iter := snap.NewIterator(nil)
	defer func() { _ = iter.Close() }()

	// Iterator created on closed snapshot should not yield results.
	if iter.SeekToFirst() {
		t.Fatal("expected false on closed snapshot iterator")
	}
}

func TestSnapshot_GetReturnsCopy(t *testing.T) {
	s := openTestStorage(t)

	if err := s.Set([]byte("key"), []byte("original")); err != nil {
		t.Fatalf("Set: %v", err)
	}

	snap, err := s.Snapshot()
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	defer func() { _ = snap.Close() }()

	got1, err := snap.Get([]byte("key"))
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	got1[0] = 'X'

	got2, err := snap.Get([]byte("key"))
	if err != nil {
		t.Fatalf("Get2: %v", err)
	}
	if string(got2) != "original" {
		t.Fatalf("snapshot data isolation: got %q, want %q", got2, "original")
	}
}

// ---------------------------------------------------------------------------
// Flush and Compact
// ---------------------------------------------------------------------------

func TestFlush(t *testing.T) {
	s := openTestStorage(t)

	if err := s.Set([]byte("k"), []byte("v")); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if err := s.Flush(); err != nil {
		t.Fatalf("Flush: %v", err)
	}

	// Data is still accessible after flush.
	got, err := s.Get([]byte("k"))
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if string(got) != "v" {
		t.Fatalf("Get: got %q, want %q", got, "v")
	}
}

func TestCompact(t *testing.T) {
	s := openTestStorage(t)

	for i := 0; i < 100; i++ {
		key := []byte{byte(i)}
		if err := s.Set(key, bytes.Repeat([]byte("v"), 100)); err != nil {
			t.Fatalf("Set: %v", err)
		}
	}

	if err := s.Compact([]byte{0}, []byte{255}); err != nil {
		t.Fatalf("Compact: %v", err)
	}
}

func TestMetrics(t *testing.T) {
	s := openTestStorage(t)

	m := s.Metrics()
	if m == nil {
		t.Fatal("Metrics returned nil")
	}
}

// ---------------------------------------------------------------------------
// Compile-time interface check
// ---------------------------------------------------------------------------

func TestInterfaceCompliance(t *testing.T) {
	// The var _ check at package level already ensures this, but an explicit
	// test makes the intent clear.
	s := openTestStorage(t)
	var _ storage.Storage = s
}

// ---------------------------------------------------------------------------
// Large value round-trip
// ---------------------------------------------------------------------------

func TestLargeValue(t *testing.T) {
	s := openTestStorage(t)

	bigVal := bytes.Repeat([]byte("x"), 1024*1024) // 1 MB
	if err := s.Set([]byte("big"), bigVal); err != nil {
		t.Fatalf("Set big: %v", err)
	}

	got, err := s.Get([]byte("big"))
	if err != nil {
		t.Fatalf("Get big: %v", err)
	}
	if !bytes.Equal(got, bigVal) {
		t.Fatalf("Get big: length %d, want %d", len(got), len(bigVal))
	}
}

// ---------------------------------------------------------------------------
// Batch atomicity
// ---------------------------------------------------------------------------

func TestBatch_Atomicity(t *testing.T) {
	s := openTestStorage(t)

	// Pre-populate.
	if err := s.Set([]byte("existing"), []byte("old")); err != nil {
		t.Fatalf("Set: %v", err)
	}

	b := s.Batch()
	if err := b.Set([]byte("new1"), []byte("val1")); err != nil {
		t.Fatalf("Batch.Set: %v", err)
	}
	if err := b.Set([]byte("new2"), []byte("val2")); err != nil {
		t.Fatalf("Batch.Set: %v", err)
	}
	if err := b.Delete([]byte("existing")); err != nil {
		t.Fatalf("Batch.Delete: %v", err)
	}
	if err := b.Commit(); err != nil {
		t.Fatalf("Batch.Commit: %v", err)
	}

	// Verify all operations applied.
	if got, _ := s.Get([]byte("new1")); string(got) != "val1" {
		t.Fatalf("new1: got %q, want %q", got, "val1")
	}
	if got, _ := s.Get([]byte("new2")); string(got) != "val2" {
		t.Fatalf("new2: got %q, want %q", got, "val2")
	}
	if got, _ := s.Get([]byte("existing")); got != nil {
		t.Fatalf("existing: got %v, want nil", got)
	}
}

// ---------------------------------------------------------------------------
// Snapshot iterator with bounds
// ---------------------------------------------------------------------------

func TestSnapshot_IteratorWithBounds(t *testing.T) {
	s := openTestStorage(t)

	for _, k := range []string{"a", "b", "c", "d"} {
		if err := s.Set([]byte(k), []byte("v")); err != nil {
			t.Fatalf("Set: %v", err)
		}
	}

	snap, err := s.Snapshot()
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	defer func() { _ = snap.Close() }()

	iter := snap.NewIterator(&storage.IteratorOptions{
		LowerBound: []byte("b"),
		UpperBound: []byte("d"),
	})
	defer func() { _ = iter.Close() }()

	var collected []string
	for ok := iter.SeekToFirst(); ok; ok = iter.Next() {
		collected = append(collected, string(iter.Key()))
	}
	if len(collected) != 2 || collected[0] != "b" || collected[1] != "c" {
		t.Fatalf("bounded snapshot iterator: got %v, want [b c]", collected)
	}
}

// ---------------------------------------------------------------------------
// Iterator Error method
// ---------------------------------------------------------------------------

func TestIterator_Error_NoError(t *testing.T) {
	s := openTestStorage(t)

	iter := s.NewIterator(nil)
	defer func() { _ = iter.Close() }()

	if err := iter.Error(); err != nil {
		t.Fatalf("Error on fresh iterator: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Config ToPebbleOptions with WALDir
// ---------------------------------------------------------------------------

func TestConfig_ToPebbleOptions_WithWALDir(t *testing.T) {
	cfg := TestConfig(t.TempDir())
	cfg.WALDir = "/tmp/wal"
	opts := cfg.ToPebbleOptions()
	if opts.WALDir != "/tmp/wal" {
		t.Fatalf("WALDir: got %q, want %q", opts.WALDir, "/tmp/wal")
	}
}

func TestConfig_ToPebbleOptions_CompressionTypes(t *testing.T) {
	for _, ct := range []CompressionType{CompressionNone, CompressionSnappy, CompressionZstd} {
		cfg := TestConfig(t.TempDir())
		cfg.Compression = ct
		opts := cfg.ToPebbleOptions()
		if opts.Levels[0].Compression == nil {
			t.Fatalf("Compression function nil for %s", ct)
		}
	}
}
