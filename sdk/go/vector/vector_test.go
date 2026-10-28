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

package vector_test

import (
	"context"
	"io"
	"log/slog"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/atrest"
	"github.com/jeremyhahn/go-qrdb/pkg/interfaces"
	"github.com/jeremyhahn/go-qrdb/pkg/logger"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/storage/memory"
	sdkvector "github.com/jeremyhahn/go-qrdb/sdk/go/vector"
)

// plaintextMarker is the metadata value every test vector carries.
const plaintextMarker = "sdk-vector-plaintext-marker-91c4e7a2b6d3"

// testNamespace is the namespace every vector in these tests is written to.
const testNamespace = "sdk-vector-ns"

// discardLogger returns a logger that writes nowhere.
func discardLogger(t *testing.T) interfaces.Logger {
	t.Helper()
	l, err := logger.NewWithHandler(slog.NewTextHandler(io.Discard, nil), logger.Default())
	if err != nil {
		t.Fatalf("logger: %v", err)
	}
	return logger.NewAdapter(l)
}

// barrierEncryptor returns an at-rest encryptor over a passphrase-sealed,
// initialized (and therefore unsealed) barrier on in-memory seal storage.
func barrierEncryptor(t *testing.T) *atrest.Encryptor {
	t.Helper()
	barrier, err := seal.NewStorageBarrier(slog.New(slog.NewTextHandler(io.Discard, nil)),
		seal.NewStorageAdapter(memory.NewStorage()),
		seal.BarrierConfig{RootKeyPath: "root-key"}, seal.NewDeferredSoftwareStrategy())
	if err != nil {
		t.Fatalf("NewStorageBarrier: %v", err)
	}
	creds := seal.MustCredentials(context.Background(), []byte("sdk-vector-test-passphrase"))
	if err := barrier.Initialize(context.Background(), creds); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	return atrest.New(barrier)
}

// openStore opens a vector store at dir with the encryptor option.
func openStore(t *testing.T, dir string, enc *atrest.Encryptor) *sdkvector.PebbleVectorStorage {
	t.Helper()
	opt := sdkvector.VectorStorageWithEncryptor(enc)
	if opt == nil {
		t.Fatal("VectorStorageWithEncryptor returned a nil option")
	}
	s, err := sdkvector.NewPebbleVectorStorage(sdkvector.DefaultVectorStorageConfig(dir), discardLogger(t), opt)
	if err != nil {
		t.Fatalf("NewPebbleVectorStorage: %v", err)
	}
	return s
}

// writeCloseReopen writes one vector carrying the marker, closes the store,
// reopens it under the same encryptor and checks the vector reads back.
func writeCloseReopen(t *testing.T, dir string, enc *atrest.Encryptor) {
	t.Helper()
	ctx := context.Background()
	s := openStore(t, dir, enc)
	if err := s.CreateNamespace(ctx, &sdkvector.VectorNamespaceInfo{Name: testNamespace, Dimension: 4}); err != nil {
		t.Fatalf("CreateNamespace: %v", err)
	}
	if err := s.UpsertVector(ctx, testNamespace, &sdkvector.VectorValue{
		ID:        1,
		Embedding: []float32{0.1, 0.2, 0.3, 0.4},
		Metadata:  map[string]string{"marker": plaintextMarker},
		Version:   1,
	}); err != nil {
		t.Fatalf("UpsertVector: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	s = openStore(t, dir, enc)
	defer func() { _ = s.Close() }()
	got, err := s.GetVector(ctx, testNamespace, 1)
	if err != nil {
		t.Fatalf("GetVector after reopen: %v", err)
	}
	if got.ID != 1 || got.Metadata["marker"] != plaintextMarker || len(got.Embedding) != 4 {
		t.Fatalf("GetVector after reopen = %+v, want vector 1 with the marker", got)
	}
}

// readsWithoutBarrier reports whether the store at dir opens, and returns
// vector 1 with its marker, through an encryptor with no barrier.
func readsWithoutBarrier(t *testing.T, dir string) bool {
	t.Helper()
	s, err := sdkvector.NewPebbleVectorStorage(sdkvector.DefaultVectorStorageConfig(dir), discardLogger(t),
		sdkvector.VectorStorageWithEncryptor(atrest.New(nil)))
	if err != nil {
		return false
	}
	defer func() { _ = s.Close() }()
	got, err := s.GetVector(context.Background(), testNamespace, 1)
	return err == nil && got != nil && got.Metadata["marker"] == plaintextMarker
}

// TestVectorStorageWithEncryptor_NoOpEncryptorIsPlaintext checks an
// encryptor without a barrier leaves the store readable, and readable with
// no barrier at all.
func TestVectorStorageWithEncryptor_NoOpEncryptorIsPlaintext(t *testing.T) {
	dir := t.TempDir()
	writeCloseReopen(t, dir, atrest.New(nil))
	if !readsWithoutBarrier(t, dir) {
		t.Fatal("a plaintext store does not read back without a barrier: the encrypted case below proves nothing")
	}
}

// TestVectorStorageWithEncryptor_BarrierEncryptsAtRest checks a barrier
// encryptor round-trips the vector across a reopen under the same barrier,
// while the directory does not read back without it.
func TestVectorStorageWithEncryptor_BarrierEncryptsAtRest(t *testing.T) {
	dir := t.TempDir()
	writeCloseReopen(t, dir, barrierEncryptor(t))
	if readsWithoutBarrier(t, dir) {
		t.Fatal("a barrier-encrypted store reads back without the barrier")
	}
}
