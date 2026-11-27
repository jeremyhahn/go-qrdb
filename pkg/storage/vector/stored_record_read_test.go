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

package vector

import (
	"context"
	"errors"
	"io"
	"testing"

	"github.com/cockroachdb/pebble/v2"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/storage/vector/schema"
)

var errDiskFault = errors.New("injected disk fault")

// faultReader fails every Get with errDiskFault; the rest is the wrapped
// reader.
type faultReader struct{ pebble.Reader }

func (faultReader) Get([]byte) ([]byte, io.Closer, error) { return nil, nil, errDiskFault }

// faultStoredRecordReads makes deleteStoredIndexEntries read through a
// faultReader until the test ends.
func faultStoredRecordReads(t *testing.T) {
	t.Helper()
	storedRecordReaderHook = func(r pebble.Reader) pebble.Reader { return faultReader{r} }
	t.Cleanup(func() { storedRecordReaderHook = nil })
}

// upsertFns are the two write paths that replace a stored record.
var upsertFns = map[string]func(ctx context.Context, s *PebbleVectorStorage, ns string, v *VectorValue) error{
	"UpsertVector": func(ctx context.Context, s *PebbleVectorStorage, ns string, v *VectorValue) error {
		return s.UpsertVector(ctx, ns, v)
	},
	"BatchUpsertVectors": func(ctx context.Context, s *PebbleVectorStorage, ns string, v *VectorValue) error {
		return s.BatchUpsertVectors(ctx, ns, []*VectorValue{v})
	},
}

func taggedVector(tag string) *VectorValue {
	return &VectorValue{ID: 7, Version: 1, Embedding: []float32{1, 2}, Metadata: map[string]string{"tag": tag}}
}

func requireTagIndexed(t *testing.T, s *PebbleVectorStorage, ns, tag string, want int) {
	t.Helper()
	got, err := s.ScanByMetadata(context.Background(), ns, "tag", tag, nil)
	require.NoError(t, err)
	require.Len(t, got, want, "vectors indexed under tag=%s", tag)
}

// A Get error other than not-found on the replaced record fails the
// upsert before commit: the old record and its index entries stay, and
// the new ones are not written.
func TestUpsert_ReplacedRecordReadErrorAbortsBeforeCommit(t *testing.T) {
	for name, upsert := range upsertFns {
		t.Run(name, func(t *testing.T) {
			s, cleanup := setupStorage(t)
			defer cleanup()
			ctx := context.Background()
			require.NoError(t, upsert(ctx, s, "ns", taggedVector("a")))

			faultStoredRecordReads(t)
			err := upsert(ctx, s, "ns", taggedVector("b"))
			require.ErrorIs(t, err, errDiskFault)
			storedRecordReaderHook = nil

			got, err := s.GetVector(ctx, "ns", 7)
			require.NoError(t, err)
			require.Equal(t, "a", got.Metadata["tag"])
			requireTagIndexed(t, s, "ns", "a", 1)
			requireTagIndexed(t, s, "ns", "b", 0)
		})
	}
}

// A replaced record that does not decode fails the upsert before commit
// and is left as it was.
func TestUpsert_ReplacedRecordDecodeErrorAbortsBeforeCommit(t *testing.T) {
	for name, upsert := range upsertFns {
		t.Run(name, func(t *testing.T) {
			s, cleanup := setupStorage(t)
			defer cleanup()
			ctx := context.Background()
			key := schema.EncodeVectorKey("ns", 7, 1)
			corrupt := []byte{0xff, 0x00, 0x01}
			require.NoError(t, s.db.Set(key, corrupt, pebble.Sync))

			err := upsert(ctx, s, "ns", taggedVector("b"))
			require.Error(t, err)
			require.Contains(t, err.Error(), "decode the record version 1 of vector 7")

			raw, closer, err := s.db.Get(key)
			require.NoError(t, err)
			require.Equal(t, corrupt, raw)
			require.NoError(t, closer.Close())
			requireTagIndexed(t, s, "ns", "b", 0)
		})
	}
}

// A missing replaced record is the first write of the ID, not an error.
func TestDeleteStoredIndexEntries_NotFoundIsNil(t *testing.T) {
	s, cleanup := setupStorage(t)
	defer cleanup()
	batch := s.db.NewBatch()
	defer func() { _ = batch.Close() }()
	require.NoError(t, deleteStoredIndexEntries(s.db, batch, "ns", 7, 1))
	require.Zero(t, batch.Count())
}
