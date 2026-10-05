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

package statemachine

import (
	"bytes"
	"errors"
	"log/slog"
	"strings"
	"testing"
)

// This file carries no build constraint: fail_set_storage_test.go, the
// double it uses, has none either (see that file's own comment), and
// FastBackend (backend_fast.go) has none.

// A key is caller-supplied content and can carry a secret or other
// sensitive data a client embedded in it (go-qrdb#592 Low); FastBackend's
// two write-failure log lines must identify the key by keyFingerprint, a
// non-reversible hash, never by the key's own bytes.

// TestFastBackend_PersistAsyncLogOmitsRawKey exercises Put's ASYNCHRONOUS
// failure path (persistAsync): the failure is logged, not returned, so the
// log line is the only record of it.
func TestFastBackend_PersistAsyncLogOmitsRawKey(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, nil))

	st := newFailSetStorage(errors.New("boom"))
	st.failing.Store(true)
	b := NewFastBackend(1, 1, st, logger)
	defer func() { _ = b.Close() }()

	secretKey := []byte("tenant-secret-credential-material")
	if err := b.Put(secretKey, []byte("v")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	b.wg.Wait()

	out := buf.String()
	if out == "" {
		t.Fatal("expected a log line for the failed async write, got none")
	}
	if strings.Contains(out, string(secretKey)) {
		t.Fatalf("log line names the raw key: %s", out)
	}
	if want := keyFingerprint(secretKey); !strings.Contains(out, want) {
		t.Fatalf("log line missing key_fingerprint %q: %s", want, out)
	}
}

// TestFastBackend_PersistDurableLogOmitsRawKey exercises PutDurable's
// SYNCHRONOUS failure path (persistDurable).
func TestFastBackend_PersistDurableLogOmitsRawKey(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, nil))

	st := newFailSetStorage(errors.New("boom"))
	st.failing.Store(true)
	b := NewFastBackend(1, 1, st, logger)
	defer func() { _ = b.Close() }()

	secretKey := []byte("tenant-secret-credential-material")
	if err := b.PutDurable(secretKey, []byte("v")); err == nil {
		t.Fatal("expected PutDurable to report the storage failure")
	}

	out := buf.String()
	if out == "" {
		t.Fatal("expected a log line for the failed durable write, got none")
	}
	if strings.Contains(out, string(secretKey)) {
		t.Fatalf("log line names the raw key: %s", out)
	}
	if want := keyFingerprint(secretKey); !strings.Contains(out, want) {
		t.Fatalf("log line missing key_fingerprint %q: %s", want, out)
	}
}
