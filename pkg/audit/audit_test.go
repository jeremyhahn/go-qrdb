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
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
)

// ---------------------------------------------------------------------------
// Test helpers
// ---------------------------------------------------------------------------

// memoryKVStore is an in-memory KVStore for testing. Thread-safe.
type memoryKVStore struct {
	mu   sync.RWMutex
	data map[string][]byte
}

func newMemoryKVStore() kvstore.KVStore {
	return &memoryKVStore{data: make(map[string][]byte)}
}

func (m *memoryKVStore) Put(_ context.Context, key string, value []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	v := make([]byte, len(value))
	copy(v, value)
	m.data[key] = v
	return nil
}

func (m *memoryKVStore) Get(_ context.Context, key string) ([]byte, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	v, ok := m.data[key]
	if !ok {
		return nil, nil
	}
	out := make([]byte, len(v))
	copy(out, v)
	return out, nil
}

func (m *memoryKVStore) Delete(_ context.Context, key string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.data, key)
	return nil
}

func (m *memoryKVStore) Scan(_ context.Context, prefix string, fn func(key string, value []byte) error) error {
	m.mu.RLock()
	var keys []string
	for k := range m.data {
		if strings.HasPrefix(k, prefix) {
			keys = append(keys, k)
		}
	}
	vals := make(map[string][]byte, len(keys))
	for _, k := range keys {
		v := m.data[k]
		vc := make([]byte, len(v))
		copy(vc, v)
		vals[k] = vc
	}
	m.mu.RUnlock()

	sort.Strings(keys)
	for _, k := range keys {
		if err := fn(k, vals[k]); err != nil {
			return err
		}
	}
	return nil
}

func (m *memoryKVStore) List(_ context.Context, prefix string) ([]string, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var out []string
	for k := range m.data {
		if strings.HasPrefix(k, prefix) {
			out = append(out, k)
		}
	}
	return out, nil
}

func (m *memoryKVStore) Exists(_ context.Context, key string) (bool, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	_, ok := m.data[key]
	return ok, nil
}

func (m *memoryKVStore) RegisterEntityIndexes(_ string, _ []kvstore.EntityIndex) error {
	return nil
}

func (m *memoryKVStore) QueryIndex(_ context.Context, _ string, _ []byte) ([][]byte, error) {
	return nil, nil
}

func (m *memoryKVStore) ScanIndex(_ context.Context, _ string, _, _ []byte) (map[string][][]byte, error) {
	return nil, nil
}

func (m *memoryKVStore) QueryCompoundIndex(_ context.Context, _ string, _ [][]byte) ([][]byte, error) {
	return nil, nil
}

func (m *memoryKVStore) ScanCompoundIndex(_ context.Context, _ string, _ [][]byte, _, _ []byte) (map[string][][]byte, error) {
	return nil, nil
}

var _ kvstore.KVStore = (*memoryKVStore)(nil)

// failingKVStore always returns an error on Put.
type failingKVStore struct {
	memoryKVStore
	putErr error
}

func (f *failingKVStore) Put(_ context.Context, _ string, _ []byte) error {
	return f.putErr
}

func (f *failingKVStore) Exists(_ context.Context, _ string) (bool, error) {
	return false, f.putErr
}

func (f *failingKVStore) RegisterEntityIndexes(_ string, _ []kvstore.EntityIndex) error {
	return nil
}

func (f *failingKVStore) QueryIndex(_ context.Context, _ string, _ []byte) ([][]byte, error) {
	return nil, nil
}

func (f *failingKVStore) ScanIndex(_ context.Context, _ string, _, _ []byte) (map[string][][]byte, error) {
	return nil, nil
}

func (f *failingKVStore) QueryCompoundIndex(_ context.Context, _ string, _ [][]byte) ([][]byte, error) {
	return nil, nil
}

func (f *failingKVStore) ScanCompoundIndex(_ context.Context, _ string, _ [][]byte, _, _ []byte) (map[string][][]byte, error) {
	return nil, nil
}

var _ kvstore.KVStore = (*failingKVStore)(nil)

// testEntry returns a fully populated Entry for testing.
func testEntry() *Entry {
	return &Entry{
		Timestamp: time.Date(2026, 1, 15, 10, 30, 0, 123456789, time.UTC),
		TenantID:  "tenant-1",
		Subject:   "alice",
		Action:    "user.create",
		Resource:  "user:bob",
		Result:    ResultSuccess,
		Metadata:  map[string]string{"ip": "192.168.1.1"},
	}
}

// testEntryNoMeta returns an Entry without metadata.
func testEntryNoMeta() *Entry {
	return &Entry{
		Timestamp: time.Date(2026, 2, 20, 14, 0, 0, 0, time.UTC),
		TenantID:  "tenant-2",
		Subject:   "admin",
		Action:    "cert.revoke",
		Resource:  "cert:serial123",
		Result:    ResultDenied,
	}
}

// ---------------------------------------------------------------------------
// New() constructor tests
// ---------------------------------------------------------------------------

func TestNew_Disabled(t *testing.T) {
	cfg := Config{Enabled: false}
	l, err := New(cfg, nil)
	if err != nil {
		t.Fatalf("New() error: %v", err)
	}
	if _, ok := l.(*noopLogger); !ok {
		t.Fatalf("expected *noopLogger, got %T", l)
	}
}

func TestNew_Disabled_IgnoresBackends(t *testing.T) {
	cfg := Config{Enabled: false, KVBackend: true, FileBackend: true}
	l, err := New(cfg, nil)
	if err != nil {
		t.Fatalf("New() error: %v", err)
	}
	if _, ok := l.(*noopLogger); !ok {
		t.Fatalf("expected *noopLogger when disabled, got %T", l)
	}
}

func TestNew_KVOnly(t *testing.T) {
	store := newMemoryKVStore()
	cfg := Config{Enabled: true, KVBackend: true, FileBackend: false}
	l, err := New(cfg, store)
	if err != nil {
		t.Fatalf("New() error: %v", err)
	}
	if _, ok := l.(*kvLogger); !ok {
		t.Fatalf("expected *kvLogger, got %T", l)
	}
	if err := l.Close(); err != nil {
		t.Fatalf("Close() error: %v", err)
	}
}

func TestNew_FileOnly(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.log")
	cfg := Config{Enabled: true, KVBackend: false, FileBackend: true, FilePath: path}
	l, err := New(cfg, nil)
	if err != nil {
		t.Fatalf("New() error: %v", err)
	}
	if _, ok := l.(*fileLogger); !ok {
		t.Fatalf("expected *fileLogger, got %T", l)
	}
	if err := l.Close(); err != nil {
		t.Fatalf("Close() error: %v", err)
	}
}

func TestNew_Both(t *testing.T) {
	store := newMemoryKVStore()
	path := filepath.Join(t.TempDir(), "audit.log")
	cfg := Config{Enabled: true, KVBackend: true, FileBackend: true, FilePath: path}
	l, err := New(cfg, store)
	if err != nil {
		t.Fatalf("New() error: %v", err)
	}
	if _, ok := l.(*multiLogger); !ok {
		t.Fatalf("expected *multiLogger, got %T", l)
	}
	if err := l.Close(); err != nil {
		t.Fatalf("Close() error: %v", err)
	}
}

func TestNew_KVNilStore(t *testing.T) {
	cfg := Config{Enabled: true, KVBackend: true}
	_, err := New(cfg, nil)
	if err == nil {
		t.Fatal("expected error when kvstore is nil")
	}
	var cfgErr *ConfigError
	if !errors.As(err, &cfgErr) {
		t.Fatalf("expected *ConfigError, got %T: %v", err, err)
	}
	if !strings.Contains(cfgErr.Detail, "kvstore is nil") {
		t.Fatalf("unexpected detail: %s", cfgErr.Detail)
	}
}

func TestNew_KVNilStore_ErrorMessage(t *testing.T) {
	cfg := Config{Enabled: true, KVBackend: true}
	_, err := New(cfg, nil)
	if err == nil {
		t.Fatal("expected error")
	}
	want := "audit: invalid config: kv_backend enabled but kvstore is nil"
	if err.Error() != want {
		t.Fatalf("Error() = %q, want %q", err.Error(), want)
	}
}

func TestNew_NoBackends(t *testing.T) {
	cfg := Config{Enabled: true, KVBackend: false, FileBackend: false}
	l, err := New(cfg, nil)
	if err != nil {
		t.Fatalf("New() error: %v", err)
	}
	if _, ok := l.(*noopLogger); !ok {
		t.Fatalf("expected *noopLogger when no backends, got %T", l)
	}
}

func TestNew_FileBackend_InvalidPath(t *testing.T) {
	cfg := Config{Enabled: true, FileBackend: true, FilePath: "/nonexistent/dir/audit.log"}
	_, err := New(cfg, nil)
	if err == nil {
		t.Fatal("expected error for invalid file path")
	}
	var fileErr *FileError
	if !errors.As(err, &fileErr) {
		t.Fatalf("expected *FileError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// kvLogger tests
// ---------------------------------------------------------------------------

func TestKVLogger_Log_Success(t *testing.T) {
	store := newMemoryKVStore()
	l := &kvLogger{store: store}

	entry := testEntry()
	if err := l.Log(context.Background(), entry); err != nil {
		t.Fatalf("Log() error: %v", err)
	}

	// Verify key was written.
	keys, err := store.List(context.Background(), "__sys:audit/")
	if err != nil {
		t.Fatalf("List() error: %v", err)
	}
	if len(keys) != 1 {
		t.Fatalf("expected 1 key, got %d", len(keys))
	}
}

func TestKVLogger_Log_VerifyKey(t *testing.T) {
	store := newMemoryKVStore()
	l := &kvLogger{store: store}

	entry := testEntry()
	if err := l.Log(context.Background(), entry); err != nil {
		t.Fatalf("Log() error: %v", err)
	}

	wantPrefix := "__sys:audit/tenant-1/"
	keys, err := store.List(context.Background(), wantPrefix)
	if err != nil {
		t.Fatalf("List() error: %v", err)
	}
	if len(keys) != 1 {
		t.Fatalf("expected 1 key with prefix %q, got %d", wantPrefix, len(keys))
	}

	key := keys[0]
	if !strings.HasPrefix(key, wantPrefix) {
		t.Fatalf("key %q does not have prefix %q", key, wantPrefix)
	}

	// The suffix should be a valid nanosecond timestamp.
	suffix := key[len(wantPrefix):]
	if len(suffix) == 0 {
		t.Fatal("key suffix (timestamp) is empty")
	}
}

func TestKVLogger_Log_VerifyPayload(t *testing.T) {
	store := newMemoryKVStore()
	l := &kvLogger{store: store}

	entry := testEntry()
	if err := l.Log(context.Background(), entry); err != nil {
		t.Fatalf("Log() error: %v", err)
	}

	// Read back the stored value.
	var storedValue []byte
	err := store.Scan(context.Background(), "__sys:audit/", func(_ string, value []byte) error {
		storedValue = value
		return nil
	})
	if err != nil {
		t.Fatalf("Scan() error: %v", err)
	}

	var decoded Entry
	if err := json.Unmarshal(storedValue, &decoded); err != nil {
		t.Fatalf("json.Unmarshal() error: %v", err)
	}

	if decoded.TenantID != "tenant-1" {
		t.Fatalf("TenantID = %q, want %q", decoded.TenantID, "tenant-1")
	}
	if decoded.Subject != "alice" {
		t.Fatalf("Subject = %q, want %q", decoded.Subject, "alice")
	}
	if decoded.Action != "user.create" {
		t.Fatalf("Action = %q, want %q", decoded.Action, "user.create")
	}
	if decoded.Resource != "user:bob" {
		t.Fatalf("Resource = %q, want %q", decoded.Resource, "user:bob")
	}
	if decoded.Result != ResultSuccess {
		t.Fatalf("Result = %q, want %q", decoded.Result, ResultSuccess)
	}
	if decoded.Metadata["ip"] != "192.168.1.1" {
		t.Fatalf("Metadata[ip] = %q, want %q", decoded.Metadata["ip"], "192.168.1.1")
	}
}

func TestKVLogger_Log_ZeroTimestamp(t *testing.T) {
	store := newMemoryKVStore()
	l := &kvLogger{store: store}

	entry := &Entry{
		TenantID: "tenant-zero",
		Subject:  "system",
		Action:   "startup",
		Resource: "server:main",
		Result:   ResultSuccess,
	}

	before := time.Now().UTC()
	if err := l.Log(context.Background(), entry); err != nil {
		t.Fatalf("Log() error: %v", err)
	}
	after := time.Now().UTC()

	if entry.Timestamp.IsZero() {
		t.Fatal("expected Timestamp to be set")
	}
	if entry.Timestamp.Before(before) || entry.Timestamp.After(after) {
		t.Fatalf("Timestamp %v not in range [%v, %v]", entry.Timestamp, before, after)
	}
}

func TestKVLogger_Log_PutError(t *testing.T) {
	putErr := errors.New("disk full")
	store := &failingKVStore{putErr: putErr}
	l := &kvLogger{store: store}

	entry := testEntry()
	err := l.Log(context.Background(), entry)
	if err == nil {
		t.Fatal("expected error from failing store")
	}

	var writeErr *WriteError
	if !errors.As(err, &writeErr) {
		t.Fatalf("expected *WriteError, got %T: %v", err, err)
	}
	if writeErr.Backend != "kv" {
		t.Fatalf("Backend = %q, want %q", writeErr.Backend, "kv")
	}
	if !errors.Is(err, putErr) {
		t.Fatal("expected wrapped putErr to be recoverable with errors.Is")
	}
}

func TestKVLogger_Close(t *testing.T) {
	store := newMemoryKVStore()
	l := &kvLogger{store: store}
	if err := l.Close(); err != nil {
		t.Fatalf("Close() error: %v", err)
	}
}

// ---------------------------------------------------------------------------
// fileLogger tests
// ---------------------------------------------------------------------------

func TestFileLogger_Log_Success(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.log")
	l, err := newFileLogger(path)
	if err != nil {
		t.Fatalf("newFileLogger() error: %v", err)
	}
	defer func() { _ = l.Close() }()

	entry := testEntry()
	if err := l.Log(context.Background(), entry); err != nil {
		t.Fatalf("Log() error: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() error: %v", err)
	}
	if len(data) == 0 {
		t.Fatal("expected non-empty log file")
	}
}

func TestFileLogger_Log_JSONFormat(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.log")
	l, err := newFileLogger(path)
	if err != nil {
		t.Fatalf("newFileLogger() error: %v", err)
	}
	defer func() { _ = l.Close() }()

	entry := testEntry()
	if err := l.Log(context.Background(), entry); err != nil {
		t.Fatalf("Log() error: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() error: %v", err)
	}

	var record map[string]any
	if err := json.Unmarshal(data, &record); err != nil {
		t.Fatalf("log line is not valid JSON: %v\nraw: %s", err, string(data))
	}

	// Verify slog fields.
	if record["tenant_id"] != "tenant-1" {
		t.Fatalf("tenant_id = %v, want %q", record["tenant_id"], "tenant-1")
	}
	if record["subject"] != "alice" {
		t.Fatalf("subject = %v, want %q", record["subject"], "alice")
	}
	if record["action"] != "user.create" {
		t.Fatalf("action = %v, want %q", record["action"], "user.create")
	}
	if record["resource"] != "user:bob" {
		t.Fatalf("resource = %v, want %q", record["resource"], "user:bob")
	}
	if record["result"] != ResultSuccess {
		t.Fatalf("result = %v, want %q", record["result"], ResultSuccess)
	}
	if record["msg"] != "audit" {
		t.Fatalf("msg = %v, want %q", record["msg"], "audit")
	}

	// Verify metadata group.
	meta, ok := record["metadata"].(map[string]any)
	if !ok {
		t.Fatalf("expected metadata group, got %T", record["metadata"])
	}
	if meta["ip"] != "192.168.1.1" {
		t.Fatalf("metadata.ip = %v, want %q", meta["ip"], "192.168.1.1")
	}
}

func TestFileLogger_Log_NoMetadata(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.log")
	l, err := newFileLogger(path)
	if err != nil {
		t.Fatalf("newFileLogger() error: %v", err)
	}
	defer func() { _ = l.Close() }()

	entry := testEntryNoMeta()
	if err := l.Log(context.Background(), entry); err != nil {
		t.Fatalf("Log() error: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() error: %v", err)
	}

	var record map[string]any
	if err := json.Unmarshal(data, &record); err != nil {
		t.Fatalf("log line is not valid JSON: %v", err)
	}

	if _, exists := record["metadata"]; exists {
		t.Fatal("expected no metadata key when entry has no metadata")
	}
	if record["result"] != ResultDenied {
		t.Fatalf("result = %v, want %q", record["result"], ResultDenied)
	}
}

func TestFileLogger_Log_ZeroTimestamp(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.log")
	l, err := newFileLogger(path)
	if err != nil {
		t.Fatalf("newFileLogger() error: %v", err)
	}
	defer func() { _ = l.Close() }()

	entry := &Entry{
		TenantID: "tenant-ts",
		Subject:  "system",
		Action:   "boot",
		Resource: "server:1",
		Result:   ResultSuccess,
	}

	before := time.Now().UTC()
	if err := l.Log(context.Background(), entry); err != nil {
		t.Fatalf("Log() error: %v", err)
	}
	after := time.Now().UTC()

	if entry.Timestamp.IsZero() {
		t.Fatal("expected Timestamp to be filled in")
	}
	if entry.Timestamp.Before(before) || entry.Timestamp.After(after) {
		t.Fatalf("Timestamp %v not in range [%v, %v]", entry.Timestamp, before, after)
	}
}

func TestFileLogger_Log_MultipleEntries(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.log")
	l, err := newFileLogger(path)
	if err != nil {
		t.Fatalf("newFileLogger() error: %v", err)
	}
	defer func() { _ = l.Close() }()

	for i := 0; i < 3; i++ {
		entry := testEntry()
		entry.Subject = "user-" + string(rune('a'+i))
		if err := l.Log(context.Background(), entry); err != nil {
			t.Fatalf("Log() iteration %d error: %v", i, err)
		}
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() error: %v", err)
	}

	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 3 {
		t.Fatalf("expected 3 log lines, got %d", len(lines))
	}

	for i, line := range lines {
		var record map[string]any
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			t.Fatalf("line %d is not valid JSON: %v", i, err)
		}
	}
}

func TestFileLogger_Close(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.log")
	l, err := newFileLogger(path)
	if err != nil {
		t.Fatalf("newFileLogger() error: %v", err)
	}
	if err := l.Close(); err != nil {
		t.Fatalf("Close() error: %v", err)
	}
}

func TestFileLogger_Close_DoubleClose(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.log")
	l, err := newFileLogger(path)
	if err != nil {
		t.Fatalf("newFileLogger() error: %v", err)
	}
	if err := l.Close(); err != nil {
		t.Fatalf("first Close() error: %v", err)
	}
	// Second close should return an error from the underlying file.
	if err := l.Close(); err == nil {
		t.Fatal("expected error on second Close()")
	}
}

func TestNewFileLogger_InvalidPath(t *testing.T) {
	_, err := newFileLogger("/nonexistent/dir/file.log")
	if err == nil {
		t.Fatal("expected error for invalid path")
	}
	var fileErr *FileError
	if !errors.As(err, &fileErr) {
		t.Fatalf("expected *FileError, got %T: %v", err, err)
	}
	if fileErr.Path != "/nonexistent/dir/file.log" {
		t.Fatalf("Path = %q, want %q", fileErr.Path, "/nonexistent/dir/file.log")
	}
}

// ---------------------------------------------------------------------------
// multiLogger tests
// ---------------------------------------------------------------------------

func TestMultiLogger_Log_BothSucceed(t *testing.T) {
	store := newMemoryKVStore()
	path := filepath.Join(t.TempDir(), "audit.log")
	fl, err := newFileLogger(path)
	if err != nil {
		t.Fatalf("newFileLogger() error: %v", err)
	}

	ml := &multiLogger{loggers: []Logger{
		&kvLogger{store: store},
		fl,
	}}
	defer func() { _ = ml.Close() }()

	entry := testEntry()
	if err := ml.Log(context.Background(), entry); err != nil {
		t.Fatalf("Log() error: %v", err)
	}

	// Verify KV backend wrote the entry.
	keys, err := store.List(context.Background(), "__sys:audit/")
	if err != nil {
		t.Fatalf("List() error: %v", err)
	}
	if len(keys) != 1 {
		t.Fatalf("expected 1 KV key, got %d", len(keys))
	}

	// Verify file backend wrote the entry.
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() error: %v", err)
	}
	if len(data) == 0 {
		t.Fatal("expected non-empty log file")
	}
}

func TestMultiLogger_Log_OneFailsReturnsError(t *testing.T) {
	putErr := errors.New("kv failure")
	failStore := &failingKVStore{putErr: putErr}
	path := filepath.Join(t.TempDir(), "audit.log")
	fl, err := newFileLogger(path)
	if err != nil {
		t.Fatalf("newFileLogger() error: %v", err)
	}

	ml := &multiLogger{loggers: []Logger{
		&kvLogger{store: failStore},
		fl,
	}}
	defer func() { _ = ml.Close() }()

	entry := testEntry()
	err = ml.Log(context.Background(), entry)
	if err == nil {
		t.Fatal("expected error from failing KV backend")
	}

	var writeErr *WriteError
	if !errors.As(err, &writeErr) {
		t.Fatalf("expected *WriteError, got %T: %v", err, err)
	}

	// File backend should still have succeeded.
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() error: %v", err)
	}
	if len(data) == 0 {
		t.Fatal("file backend should have succeeded despite KV failure")
	}
}

func TestMultiLogger_Close(t *testing.T) {
	store := newMemoryKVStore()
	path := filepath.Join(t.TempDir(), "audit.log")
	fl, err := newFileLogger(path)
	if err != nil {
		t.Fatalf("newFileLogger() error: %v", err)
	}

	ml := &multiLogger{loggers: []Logger{
		&kvLogger{store: store},
		fl,
	}}

	if err := ml.Close(); err != nil {
		t.Fatalf("Close() error: %v", err)
	}
}

func TestMultiLogger_Close_PropagatesError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.log")
	fl, err := newFileLogger(path)
	if err != nil {
		t.Fatalf("newFileLogger() error: %v", err)
	}
	// Close the file first to force an error on the multiLogger close.
	_ = fl.Close()

	ml := &multiLogger{loggers: []Logger{
		&noopLogger{},
		fl,
	}}

	err = ml.Close()
	if err == nil {
		t.Fatal("expected error from pre-closed fileLogger")
	}
}

// ---------------------------------------------------------------------------
// noopLogger tests
// ---------------------------------------------------------------------------

func TestNoopLogger_Log(t *testing.T) {
	l := &noopLogger{}
	entry := testEntry()
	if err := l.Log(context.Background(), entry); err != nil {
		t.Fatalf("Log() error: %v", err)
	}
}

func TestNoopLogger_Close(t *testing.T) {
	l := &noopLogger{}
	if err := l.Close(); err != nil {
		t.Fatalf("Close() error: %v", err)
	}
}

func TestNoopLogger_ImplementsLogger(t *testing.T) {
	var _ Logger = (*noopLogger)(nil)
}

// ---------------------------------------------------------------------------
// DefaultConfig tests
// ---------------------------------------------------------------------------

func TestDefaultConfig(t *testing.T) {
	cfg := DefaultConfig()

	if !cfg.Enabled {
		t.Fatal("Enabled: expected true")
	}
	if cfg.KVBackend {
		t.Fatal("KVBackend: expected false")
	}
	if !cfg.FileBackend {
		t.Fatal("FileBackend: expected true")
	}
	if cfg.FilePath != "audit.log" {
		t.Fatalf("FilePath = %q, want %q", cfg.FilePath, "audit.log")
	}
}

func TestDefaultConfig_IsValid(t *testing.T) {
	cfg := DefaultConfig()
	path := filepath.Join(t.TempDir(), cfg.FilePath)
	cfg.FilePath = path

	l, err := New(cfg, nil)
	if err != nil {
		t.Fatalf("New(DefaultConfig()) error: %v", err)
	}
	defer func() { _ = l.Close() }()

	if _, ok := l.(*fileLogger); !ok {
		t.Fatalf("expected *fileLogger from default config, got %T", l)
	}
}

// ---------------------------------------------------------------------------
// Entry tests
// ---------------------------------------------------------------------------

func TestEntry_WithMetadata(t *testing.T) {
	entry := testEntry()
	if len(entry.Metadata) != 1 {
		t.Fatalf("expected 1 metadata entry, got %d", len(entry.Metadata))
	}
	if entry.Metadata["ip"] != "192.168.1.1" {
		t.Fatalf("Metadata[ip] = %q, want %q", entry.Metadata["ip"], "192.168.1.1")
	}
}

func TestEntry_WithoutMetadata(t *testing.T) {
	entry := testEntryNoMeta()
	if entry.Metadata != nil {
		t.Fatalf("expected nil Metadata, got %v", entry.Metadata)
	}
}

func TestEntry_JSONRoundTrip(t *testing.T) {
	entry := testEntry()
	data, err := json.Marshal(entry)
	if err != nil {
		t.Fatalf("Marshal() error: %v", err)
	}

	var decoded Entry
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("Unmarshal() error: %v", err)
	}

	if !decoded.Timestamp.Equal(entry.Timestamp) {
		t.Fatalf("Timestamp mismatch: got %v, want %v", decoded.Timestamp, entry.Timestamp)
	}
	if decoded.TenantID != entry.TenantID {
		t.Fatalf("TenantID = %q, want %q", decoded.TenantID, entry.TenantID)
	}
	if decoded.Subject != entry.Subject {
		t.Fatalf("Subject = %q, want %q", decoded.Subject, entry.Subject)
	}
	if decoded.Action != entry.Action {
		t.Fatalf("Action = %q, want %q", decoded.Action, entry.Action)
	}
	if decoded.Resource != entry.Resource {
		t.Fatalf("Resource = %q, want %q", decoded.Resource, entry.Resource)
	}
	if decoded.Result != entry.Result {
		t.Fatalf("Result = %q, want %q", decoded.Result, entry.Result)
	}
	if decoded.Metadata["ip"] != entry.Metadata["ip"] {
		t.Fatalf("Metadata[ip] = %q, want %q", decoded.Metadata["ip"], entry.Metadata["ip"])
	}
}

func TestEntry_JSONOmitsEmptyMetadata(t *testing.T) {
	entry := testEntryNoMeta()
	data, err := json.Marshal(entry)
	if err != nil {
		t.Fatalf("Marshal() error: %v", err)
	}
	if strings.Contains(string(data), "metadata") {
		t.Fatalf("expected metadata to be omitted from JSON, got: %s", string(data))
	}
}

// ---------------------------------------------------------------------------
// Result constants tests
// ---------------------------------------------------------------------------

func TestResultConstants(t *testing.T) {
	if ResultSuccess != "success" {
		t.Fatalf("ResultSuccess = %q, want %q", ResultSuccess, "success")
	}
	if ResultDenied != "denied" {
		t.Fatalf("ResultDenied = %q, want %q", ResultDenied, "denied")
	}
}

func TestResultConstants_AreDistinct(t *testing.T) {
	if ResultSuccess == ResultDenied {
		t.Fatal("ResultSuccess and ResultDenied must be distinct")
	}
}

// ---------------------------------------------------------------------------
// Error type tests
// ---------------------------------------------------------------------------

func TestWriteError_Message(t *testing.T) {
	inner := errors.New("connection refused")
	err := &WriteError{Backend: "kv", Err: inner}
	want := "audit: write to kv failed: connection refused"
	if err.Error() != want {
		t.Fatalf("Error() = %q, want %q", err.Error(), want)
	}
}

func TestWriteError_Unwrap(t *testing.T) {
	inner := errors.New("timeout")
	err := &WriteError{Backend: "file", Err: inner}
	if !errors.Is(err, inner) {
		t.Fatal("errors.Is should match inner error")
	}

	var target *WriteError
	if !errors.As(err, &target) {
		t.Fatal("errors.As should match *WriteError")
	}
	if target.Backend != "file" {
		t.Fatalf("Backend = %q, want %q", target.Backend, "file")
	}
}

func TestFileError_Message(t *testing.T) {
	inner := errors.New("permission denied")
	err := &FileError{Path: "/var/log/audit.log", Err: inner}
	want := "audit: cannot open file /var/log/audit.log: permission denied"
	if err.Error() != want {
		t.Fatalf("Error() = %q, want %q", err.Error(), want)
	}
}

func TestFileError_Unwrap(t *testing.T) {
	inner := errors.New("no space left")
	err := &FileError{Path: "/tmp/test.log", Err: inner}
	if !errors.Is(err, inner) {
		t.Fatal("errors.Is should match inner error")
	}

	var target *FileError
	if !errors.As(err, &target) {
		t.Fatal("errors.As should match *FileError")
	}
	if target.Path != "/tmp/test.log" {
		t.Fatalf("Path = %q, want %q", target.Path, "/tmp/test.log")
	}
}

func TestConfigError_Message(t *testing.T) {
	err := &ConfigError{Detail: "missing required field"}
	want := "audit: invalid config: missing required field"
	if err.Error() != want {
		t.Fatalf("Error() = %q, want %q", err.Error(), want)
	}
}

func TestConfigError_As(t *testing.T) {
	var err error = &ConfigError{Detail: "test"}
	var target *ConfigError
	if !errors.As(err, &target) {
		t.Fatal("errors.As should match *ConfigError")
	}
	if target.Detail != "test" {
		t.Fatalf("Detail = %q, want %q", target.Detail, "test")
	}
}

// ---------------------------------------------------------------------------
// Interface compliance checks
// ---------------------------------------------------------------------------

func TestKVLogger_ImplementsLogger(t *testing.T) {
	var _ Logger = (*kvLogger)(nil)
}

func TestFileLogger_ImplementsLogger(t *testing.T) {
	var _ Logger = (*fileLogger)(nil)
}

func TestMultiLogger_ImplementsLogger(t *testing.T) {
	var _ Logger = (*multiLogger)(nil)
}
