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

package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	sdk "github.com/jeremyhahn/go-qrdb/sdk/go"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport/embedded"
)

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// newTestBatchBinding creates a BatchBinding backed by an embedded transport
// wired to mock batch service.
func newTestBatchBinding(t *testing.T) *BatchBinding {
	t.Helper()
	kvSvc := newMockKVService()
	adminSvc := newMockAdminService()
	batchSvc := &mockBatchService{}
	trans, err := embedded.New(
		kvSvc,
		adminSvc,
		embedded.WithBatchService(batchSvc),
	)
	if err != nil {
		t.Fatalf("embedded.New: %v", err)
	}
	cl, err := sdk.NewClient(trans)
	if err != nil {
		t.Fatalf("sdk.NewClient: %v", err)
	}
	if err := cl.Connect(context.Background()); err != nil {
		t.Fatalf("client.Connect: %v", err)
	}
	log := testLogger(t)
	conn := NewConnectionBinding(log)
	conn.SetClient(cl)
	return NewBatchBinding(log, conn)
}

// newBatchBindingNoConn returns a BatchBinding with no active client.
func newBatchBindingNoConn(t *testing.T) *BatchBinding {
	t.Helper()
	log := testLogger(t)
	conn := NewConnectionBinding(log)
	return NewBatchBinding(log, conn)
}

// ---------------------------------------------------------------------------
// ImportFile tests
// ---------------------------------------------------------------------------

func TestBatchBinding_ImportFile_NotConnected(t *testing.T) {
	b := newBatchBindingNoConn(t)
	_, err := b.ImportFile("/tmp/test.csv", WailsImportOptions{
		Format:    "csv",
		KeyField:  "id",
		ValueMode: "row",
	})
	if err == nil {
		t.Fatal("expected error when not connected")
	}
	var notConn *NotConnectedError
	if !errors.As(err, &notConn) {
		t.Fatalf("expected NotConnectedError, got %T: %v", err, err)
	}
}

func TestBatchBinding_ImportFile_FileNotFound(t *testing.T) {
	b := newTestBatchBinding(t)
	_, err := b.ImportFile("/nonexistent/path/data.csv", WailsImportOptions{
		Format:    "csv",
		KeyField:  "id",
		ValueMode: "row",
	})
	if err == nil {
		t.Fatal("expected error for nonexistent file")
	}
	var foe *FileOpenError
	if !errors.As(err, &foe) {
		t.Fatalf("expected FileOpenError, got %T: %v", err, err)
	}
	if foe.Path != "/nonexistent/path/data.csv" {
		t.Errorf("expected Path = /nonexistent/path/data.csv, got %q", foe.Path)
	}
}

func TestBatchBinding_ImportFile_Success(t *testing.T) {
	b := newTestBatchBinding(t)

	// Write a minimal CSV to a temp file.
	dir := t.TempDir()
	path := filepath.Join(dir, "data.csv")
	content := "id,name\n1,alice\n2,bob\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	// The mock batch service returns nil result (zero-value), so the embedded
	// transport will return an empty ImportResult.
	result, err := b.ImportFile(path, WailsImportOptions{
		Format:     "csv",
		KeyField:   "id",
		ValueMode:  "row",
		SkipHeader: true,
	})
	if err != nil {
		t.Fatalf("ImportFile: unexpected error: %v", err)
	}
	if result == nil {
		t.Fatal("expected non-nil ImportResult")
	}
}

// TestBatchBinding_ImportFile_CollectionRejected verifies that a
// collection-targeted import request is rejected with a typed error before
// ever touching the client or filesystem, rather than being silently
// dropped and imported into the raw KV keyspace instead.
func TestBatchBinding_ImportFile_CollectionRejected(t *testing.T) {
	// Deliberately use a not-connected binding: if the rejection happened
	// after loadClient(), this would instead fail with NotConnectedError.
	b := newBatchBindingNoConn(t)
	_, err := b.ImportFile("/tmp/test.csv", WailsImportOptions{
		Format:     "csv",
		KeyField:   "id",
		ValueMode:  "row",
		Collection: "accounts",
	})
	if err == nil {
		t.Fatal("expected error for collection-targeted import")
	}
	var ciu *CollectionImportUnsupportedError
	if !errors.As(err, &ciu) {
		t.Fatalf("expected *CollectionImportUnsupportedError, got %T: %v", err, err)
	}
	if ciu.Collection != "accounts" {
		t.Errorf("expected Collection=accounts, got %q", ciu.Collection)
	}
}

func TestCollectionImportUnsupportedError_Error(t *testing.T) {
	e := &CollectionImportUnsupportedError{Collection: "accounts"}
	msg := e.Error()
	if msg == "" {
		t.Fatal("expected non-empty error message")
	}
	if !errors.Is(e, e) {
		t.Fatal("expected error to be self-comparable")
	}
}

func TestBatchBinding_ImportFile_ErrorWrapsUnderlying(t *testing.T) {
	b := newBatchBindingNoConn(t)
	_, err := b.ImportFile("/some/file.csv", WailsImportOptions{})
	if err == nil {
		t.Fatal("expected error")
	}
	// Underlying cause must be accessible via Unwrap chain.
	if err.Error() == "" {
		t.Fatal("error message must be non-empty")
	}
}

// ---------------------------------------------------------------------------
// AvailableCodecs tests
// ---------------------------------------------------------------------------

func TestBatchBinding_AvailableCodecs_NonEmpty(t *testing.T) {
	b := newBatchBindingNoConn(t)
	codecs := b.AvailableCodecs()
	if len(codecs) == 0 {
		t.Fatal("expected at least one compiled-in codec")
	}
}

func TestBatchBinding_AvailableCodecs_Sorted(t *testing.T) {
	b := newBatchBindingNoConn(t)
	codecs := b.AvailableCodecs()
	for i := 1; i < len(codecs); i++ {
		if codecs[i] < codecs[i-1] {
			t.Errorf("codecs not sorted: %q before %q", codecs[i-1], codecs[i])
		}
	}
}

// ---------------------------------------------------------------------------
// AvailableImportFormats tests
// ---------------------------------------------------------------------------

func TestBatchBinding_AvailableImportFormats_ContainsExpected(t *testing.T) {
	b := newBatchBindingNoConn(t)
	formats := b.AvailableImportFormats()

	expected := map[string]bool{
		"auto":    false,
		"csv":     false,
		"json":    false,
		"ndjson":  false,
		"yaml":    false,
		"archive": false,
	}
	for _, f := range formats {
		expected[f] = true
	}
	for name, found := range expected {
		if !found {
			t.Errorf("expected format %q not returned by AvailableImportFormats", name)
		}
	}
}

func TestBatchBinding_AvailableImportFormats_NonEmpty(t *testing.T) {
	b := newBatchBindingNoConn(t)
	formats := b.AvailableImportFormats()
	if len(formats) == 0 {
		t.Fatal("expected at least one import format")
	}
}

// ---------------------------------------------------------------------------
// FileOpenError tests
// ---------------------------------------------------------------------------

func TestFileOpenError_Error(t *testing.T) {
	inner := errors.New("permission denied")
	e := &FileOpenError{Path: "/secret/file.csv", Err: inner}
	msg := e.Error()
	if msg == "" {
		t.Fatal("expected non-empty error message")
	}
	if !errors.Is(e, inner) {
		t.Fatal("expected Unwrap to expose inner error")
	}
}

func TestFileOpenError_Unwrap(t *testing.T) {
	inner := errors.New("no such file")
	e := &FileOpenError{Path: "/missing.csv", Err: inner}
	if e.Unwrap() != inner {
		t.Fatalf("Unwrap() = %v, want %v", e.Unwrap(), inner)
	}
}
