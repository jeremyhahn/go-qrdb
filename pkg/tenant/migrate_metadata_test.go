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

package tenant

import (
	"context"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/seal"
)

// seedTenantMeta writes a raw TenantInfo record under the metadata prefix.
func seedTenantMeta(t *testing.T, store seal.StorageBackend, id string) {
	t.Helper()
	if err := store.Put(context.Background(), metadataPrefix+id, []byte(`{"id":"`+id+`","name":"`+id+`"}`)); err != nil {
		t.Fatalf("seed %s: %v", id, err)
	}
}

// TestMigrateMetadata_CopiesAbsentRecords verifies that records present in the
// source but absent from the destination are copied and counted.
func TestMigrateMetadata_CopiesAbsentRecords(t *testing.T) {
	src := seal.NewMemoryBackend()
	dst := seal.NewMemoryBackend()
	seedTenantMeta(t, src, "t1")
	seedTenantMeta(t, src, "t2")

	n, err := MigrateMetadata(context.Background(), src, dst, testLogger())
	if err != nil {
		t.Fatalf("MigrateMetadata: %v", err)
	}
	if n != 2 {
		t.Fatalf("migrated = %d, want 2", n)
	}
	for _, id := range []string{"t1", "t2"} {
		if _, gerr := dst.Get(context.Background(), metadataPrefix+id); gerr != nil {
			t.Fatalf("dst missing %s: %v", id, gerr)
		}
	}
}

// TestMigrateMetadata_EmptySourceNoop verifies the fresh-cluster case where the
// source holds no tenant metadata: nothing is migrated.
func TestMigrateMetadata_EmptySourceNoop(t *testing.T) {
	src := seal.NewMemoryBackend()
	dst := seal.NewMemoryBackend()

	n, err := MigrateMetadata(context.Background(), src, dst, testLogger())
	if err != nil {
		t.Fatalf("MigrateMetadata: %v", err)
	}
	if n != 0 {
		t.Fatalf("migrated = %d, want 0", n)
	}
}

// TestMigrateMetadata_SkipsExistingRecords verifies records already present in
// the destination are left untouched and not recounted.
func TestMigrateMetadata_SkipsExistingRecords(t *testing.T) {
	src := seal.NewMemoryBackend()
	dst := seal.NewMemoryBackend()
	seedTenantMeta(t, src, "t1")
	seedTenantMeta(t, src, "t2")
	// t1 already exists in dst with a distinct value that must survive.
	if err := dst.Put(context.Background(), metadataPrefix+"t1", []byte(`{"id":"t1","name":"existing"}`)); err != nil {
		t.Fatalf("seed dst t1: %v", err)
	}

	n, err := MigrateMetadata(context.Background(), src, dst, testLogger())
	if err != nil {
		t.Fatalf("MigrateMetadata: %v", err)
	}
	if n != 1 {
		t.Fatalf("migrated = %d, want 1 (only t2)", n)
	}
	got, gerr := dst.Get(context.Background(), metadataPrefix+"t1")
	if gerr != nil {
		t.Fatalf("dst get t1: %v", gerr)
	}
	if string(got) != `{"id":"t1","name":"existing"}` {
		t.Fatalf("t1 overwritten: %s", got)
	}
}

// TestMigrateMetadata_NilStore verifies nil source or destination is rejected.
func TestMigrateMetadata_NilStore(t *testing.T) {
	if _, err := MigrateMetadata(context.Background(), nil, seal.NewMemoryBackend(), testLogger()); !errors.Is(err, ErrNilStore) {
		t.Fatalf("nil src: got %v, want ErrNilStore", err)
	}
	if _, err := MigrateMetadata(context.Background(), seal.NewMemoryBackend(), nil, testLogger()); !errors.Is(err, ErrNilStore) {
		t.Fatalf("nil dst: got %v, want ErrNilStore", err)
	}
}

// TestMigrateMetadata_SourceListError surfaces a source List failure as a
// StoreError so the caller can log it.
func TestMigrateMetadata_SourceListError(t *testing.T) {
	src := &failingBackend{inner: seal.NewMemoryBackend(), failLst: true}
	dst := seal.NewMemoryBackend()

	n, err := MigrateMetadata(context.Background(), src, dst, testLogger())
	if err == nil {
		t.Fatal("expected error on source list failure")
	}
	var se *StoreError
	if !errors.As(err, &se) {
		t.Fatalf("got %T, want *StoreError", err)
	}
	if n != 0 {
		t.Fatalf("migrated = %d, want 0", n)
	}
}

// TestMigrateMetadata_WriteFailureNonFatal verifies a destination write failure
// is logged and skipped (non-fatal) rather than aborting the migration.
func TestMigrateMetadata_WriteFailureNonFatal(t *testing.T) {
	src := seal.NewMemoryBackend()
	seedTenantMeta(t, src, "t1")
	dst := &failingBackend{inner: seal.NewMemoryBackend(), failPut: true}

	n, err := MigrateMetadata(context.Background(), src, dst, testLogger())
	if err != nil {
		t.Fatalf("MigrateMetadata returned fatal error: %v", err)
	}
	if n != 0 {
		t.Fatalf("migrated = %d, want 0 (write failed)", n)
	}
}

// TestMigrateMetadata_NilLoggerDefaults verifies a nil logger is tolerated.
func TestMigrateMetadata_NilLoggerDefaults(t *testing.T) {
	src := seal.NewMemoryBackend()
	dst := seal.NewMemoryBackend()
	seedTenantMeta(t, src, "t1")

	n, err := MigrateMetadata(context.Background(), src, dst, nil)
	if err != nil {
		t.Fatalf("MigrateMetadata: %v", err)
	}
	if n != 1 {
		t.Fatalf("migrated = %d, want 1", n)
	}
}
