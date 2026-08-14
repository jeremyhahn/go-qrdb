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

//go:build qrdb_embedded && !integration

package main

import (
	"errors"
	"testing"
)

// newTestIndexBinding starts an embedded memory server and returns an
// IndexBinding wired to it. The caller must defer esb.StopEmbedded().
func newTestIndexBinding(t *testing.T) (*IndexBinding, *EmbeddedServerBinding) {
	t.Helper()
	esb, conn := startEmbeddedLocal(t, "pebble")
	log := testLogger(t)
	b := NewIndexBinding(log, conn)
	return b, esb
}

// ---------------------------------------------------------------------------
// NewIndexBinding constructor
// ---------------------------------------------------------------------------

func TestNewIndexBinding(t *testing.T) {
	log := testLogger(t)
	conn := NewConnectionBinding(log)
	b := NewIndexBinding(log, conn)
	if b == nil {
		t.Fatal("expected non-nil IndexBinding")
	}
}

func TestNewIndexBinding_Logger(t *testing.T) {
	log := testLogger(t)
	conn := NewConnectionBinding(log)
	b := NewIndexBinding(log, conn)
	if b.logger != log {
		t.Error("expected logger to be set on IndexBinding")
	}
}

// ---------------------------------------------------------------------------
// IndexBinding.Create
// ---------------------------------------------------------------------------

func TestIndexBinding_Create_NotConnected(t *testing.T) {
	log := testLogger(t)
	conn := NewConnectionBinding(log)
	b := NewIndexBinding(log, conn)

	err := b.Create(IndexCreateParams{Name: "idx", FieldName: "field"})
	if err == nil {
		t.Fatal("expected error from Create when not connected")
	}
	var nce *NotConnectedError
	if !errors.As(err, &nce) {
		t.Fatalf("expected NotConnectedError, got %T: %v", err, err)
	}
}

func TestIndexBinding_Create_Success(t *testing.T) {
	b, esb := newTestIndexBinding(t)
	defer func() { _ = esb.StopEmbedded() }()

	err := b.Create(IndexCreateParams{
		Name:      "name-idx",
		FieldName: "name",
		Unique:    false,
		Sparse:    false,
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
}

func TestIndexBinding_Create_UniqueIndex(t *testing.T) {
	b, esb := newTestIndexBinding(t)
	defer func() { _ = esb.StopEmbedded() }()

	err := b.Create(IndexCreateParams{
		Name:      "email-idx",
		FieldName: "email",
		Unique:    true,
		Sparse:    false,
	})
	if err != nil {
		t.Fatalf("Create unique index: %v", err)
	}
}

// ---------------------------------------------------------------------------
// IndexBinding.Drop
// ---------------------------------------------------------------------------

func TestIndexBinding_Drop_NotConnected(t *testing.T) {
	log := testLogger(t)
	conn := NewConnectionBinding(log)
	b := NewIndexBinding(log, conn)

	err := b.Drop("any-index")
	if err == nil {
		t.Fatal("expected error from Drop when not connected")
	}
	var nce *NotConnectedError
	if !errors.As(err, &nce) {
		t.Fatalf("expected NotConnectedError, got %T: %v", err, err)
	}
}

func TestIndexBinding_Drop_Success(t *testing.T) {
	b, esb := newTestIndexBinding(t)
	defer func() { _ = esb.StopEmbedded() }()

	if err := b.Create(IndexCreateParams{
		Name:      "drop-idx",
		FieldName: "field",
	}); err != nil {
		t.Fatalf("Create before Drop: %v", err)
	}

	if err := b.Drop("drop-idx"); err != nil {
		t.Fatalf("Drop: %v", err)
	}
}

// ---------------------------------------------------------------------------
// IndexBinding.Query
// ---------------------------------------------------------------------------

func TestIndexBinding_Query_NotConnected(t *testing.T) {
	log := testLogger(t)
	conn := NewConnectionBinding(log)
	b := NewIndexBinding(log, conn)

	_, err := b.Query("name-idx", "alice")
	if err == nil {
		t.Fatal("expected error from Query when not connected")
	}
	var nce *NotConnectedError
	if !errors.As(err, &nce) {
		t.Fatalf("expected NotConnectedError, got %T: %v", err, err)
	}
}

func TestIndexBinding_Query_Success(t *testing.T) {
	b, esb := newTestIndexBinding(t)
	defer func() { _ = esb.StopEmbedded() }()

	if err := b.Create(IndexCreateParams{
		Name:      "query-idx",
		FieldName: "status",
	}); err != nil {
		t.Fatalf("Create: %v", err)
	}

	resp, err := b.Query("query-idx", "active")
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if resp == nil {
		t.Fatal("expected non-nil IndexQueryResponse")
	}
}

// ---------------------------------------------------------------------------
// IndexBinding.Scan
// ---------------------------------------------------------------------------

func TestIndexBinding_Scan_NotConnected(t *testing.T) {
	log := testLogger(t)
	conn := NewConnectionBinding(log)
	b := NewIndexBinding(log, conn)

	_, err := b.Scan("name-idx", "a", "z")
	if err == nil {
		t.Fatal("expected error from Scan when not connected")
	}
	var nce *NotConnectedError
	if !errors.As(err, &nce) {
		t.Fatalf("expected NotConnectedError, got %T: %v", err, err)
	}
}

func TestIndexBinding_Scan_Success(t *testing.T) {
	b, esb := newTestIndexBinding(t)
	defer func() { _ = esb.StopEmbedded() }()

	if err := b.Create(IndexCreateParams{
		Name:      "scan-idx",
		FieldName: "score",
	}); err != nil {
		t.Fatalf("Create: %v", err)
	}

	resp, err := b.Scan("scan-idx", "0", "100")
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if resp == nil {
		t.Fatal("expected non-nil IndexScanResponse")
	}
}

// ---------------------------------------------------------------------------
// IndexBinding.List
// ---------------------------------------------------------------------------

func TestIndexBinding_List_NotConnected(t *testing.T) {
	log := testLogger(t)
	conn := NewConnectionBinding(log)
	b := NewIndexBinding(log, conn)

	_, err := b.List("")
	if err == nil {
		t.Fatal("expected error from List when not connected")
	}
	var nce *NotConnectedError
	if !errors.As(err, &nce) {
		t.Fatalf("expected NotConnectedError, got %T: %v", err, err)
	}
}

func TestIndexBinding_List_Empty(t *testing.T) {
	b, esb := newTestIndexBinding(t)
	defer func() { _ = esb.StopEmbedded() }()

	resp, err := b.List("")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if resp == nil {
		t.Fatal("expected non-nil IndexListResponse")
	}
	if len(resp.Names) != 0 {
		t.Errorf("expected 0 indexes, got %d", len(resp.Names))
	}
}

func TestIndexBinding_List_Success(t *testing.T) {
	b, esb := newTestIndexBinding(t)
	defer func() { _ = esb.StopEmbedded() }()

	for _, name := range []string{"idx-a", "idx-b"} {
		if err := b.Create(IndexCreateParams{
			Name:      name,
			FieldName: "field",
		}); err != nil {
			t.Fatalf("Create(%q): %v", name, err)
		}
	}

	resp, err := b.List("")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(resp.Names) != 2 {
		t.Errorf("expected 2 indexes, got %d", len(resp.Names))
	}
}
