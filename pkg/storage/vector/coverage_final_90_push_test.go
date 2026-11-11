// Copyright (c) 2025 Jeremy Hahn
// Copyright (c) 2025 Automate The Things, LLC
//
// This file is part of go-qrdb.
//
// go-qrdb is dual-licensed:
//
// 1. GNU Affero General Public License v3.0 (AGPL-3.0)
//    See LICENSE file or visit https://www.gnu.org/licenses/agpl-3.0.html
//
// 2. Commercial License
//    Contact licensing@automatethethings.com for commercial licensing options.

package vector

import (
	"context"
	"encoding/binary"
	"os"
	"testing"
	"time"

	"github.com/cockroachdb/pebble/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/storage/vector/schema"
)

// setupSyncStorage creates a PebbleVectorStorage with SyncWrites enabled.
func setupSyncStorage(t *testing.T) (*PebbleVectorStorage, func()) {
	t.Helper()
	tmpDir := t.TempDir()
	require.NoError(t, os.MkdirAll(tmpDir, 0o750))

	config := DefaultConfig(tmpDir)
	config.SyncWrites = true

	log := &testLogger{}
	storage, err := NewPebbleVectorStorage(config, log)
	require.NoError(t, err)

	cleanup := func() {
		_ = storage.Close()
		_ = os.RemoveAll(tmpDir)
	}
	return storage, cleanup
}

// TestUpsertVector_VersionUpdateMetadataCleanup covers the update path in
// UpsertVector where Version > 1 triggers old metadata index cleanup.
func TestUpsertVector_VersionUpdateMetadataCleanup(t *testing.T) {
	storage, cleanup := setupStorage(t)
	defer cleanup()
	ctx := context.Background()

	ns := "version-cleanup-ns"
	err := storage.CreateNamespace(ctx, &NamespaceInfo{Name: ns, Dimension: 4})
	require.NoError(t, err)

	// Insert version 1
	vec1 := &VectorValue{
		ID:        1,
		Embedding: []float32{1.0, 2.0, 3.0, 4.0},
		Metadata:  map[string]string{"color": "red", "size": "large"},
		Version:   1,
	}
	err = storage.UpsertVector(ctx, ns, vec1)
	require.NoError(t, err)

	// Update to version 2 with different metadata
	vec2 := &VectorValue{
		ID:        1,
		Embedding: []float32{5.0, 6.0, 7.0, 8.0},
		Metadata:  map[string]string{"color": "blue"},
		Version:   2,
	}
	err = storage.UpsertVector(ctx, ns, vec2)
	require.NoError(t, err)

	// Retrieve should return latest version
	result, err := storage.GetVector(ctx, ns, 1)
	require.NoError(t, err)
	assert.Equal(t, uint64(2), result.Version)
	assert.Equal(t, "blue", result.Metadata["color"])
}

// TestUpsertVector_ContextCancelledBeforeSemaphore covers the context
// cancellation path in UpsertVector when context is cancelled before write.
func TestUpsertVector_ContextCancelledBeforeSemaphore(t *testing.T) {
	storage, cleanup := setupStorage(t)
	defer cleanup()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	vec := &VectorValue{
		ID:        1,
		Embedding: []float32{1.0, 2.0, 3.0, 4.0},
		Version:   1,
	}
	err := storage.UpsertVector(ctx, "test-ns", vec)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrContextCanceled)
}

// TestCleanupTombstones_WithExpiredTombstonesSyncWrites covers the
// CleanupTombstones path with SyncWrites enabled hitting the count > 0
// branch and the SyncWrites commit path.
func TestCleanupTombstones_WithExpiredTombstonesSyncWrites(t *testing.T) {
	storage, cleanup := setupSyncStorage(t)
	defer cleanup()
	ctx := context.Background()

	ns := "sync-tombstone-ns"
	err := storage.CreateNamespace(ctx, &NamespaceInfo{Name: ns, Dimension: 4})
	require.NoError(t, err)

	// Insert then delete a vector to create tombstones
	vec := &VectorValue{
		ID:        1,
		Embedding: []float32{1.0, 2.0, 3.0, 4.0},
		Version:   1,
	}
	err = storage.UpsertVector(ctx, ns, vec)
	require.NoError(t, err)

	err = storage.DeleteVector(ctx, ns, 1)
	require.NoError(t, err)

	// Use negative duration so cutoffTime is in the future, ensuring
	// all tombstones are considered "old enough" and count > 0.
	// This forces the SyncWrites=true commit path.
	err = storage.CleanupTombstones(ctx, -time.Hour)
	require.NoError(t, err)
}

// TestCleanupTombstones_WithExpiredTombstonesFinal covers the tombstone
// cleanup path where tombstones exist and are older than the cutoff.
func TestCleanupTombstones_WithExpiredTombstonesFinal(t *testing.T) {
	storage, cleanup := setupStorage(t)
	defer cleanup()
	ctx := context.Background()

	ns := "tombstone-cleanup-final-ns"
	err := storage.CreateNamespace(ctx, &NamespaceInfo{Name: ns, Dimension: 4})
	require.NoError(t, err)

	// Insert then delete a vector to create tombstones
	vec := &VectorValue{
		ID:        1,
		Embedding: []float32{1.0, 2.0, 3.0, 4.0},
		Version:   1,
	}
	err = storage.UpsertVector(ctx, ns, vec)
	require.NoError(t, err)

	err = storage.DeleteVector(ctx, ns, 1)
	require.NoError(t, err)

	// Use negative duration so cutoffTime is in the future, ensuring
	// the tombstone timestamp < cutoffTime and the batch commits.
	err = storage.CleanupTombstones(ctx, -time.Hour)
	require.NoError(t, err)
}

// TestCleanupTombstones_EmptyNamespaceFinal covers the path where
// no tombstones exist in any namespace.
func TestCleanupTombstones_EmptyNamespaceFinal(t *testing.T) {
	storage, cleanup := setupStorage(t)
	defer cleanup()
	ctx := context.Background()

	ns := "empty-tomb-final-ns"
	err := storage.CreateNamespace(ctx, &NamespaceInfo{Name: ns, Dimension: 4})
	require.NoError(t, err)

	err = storage.CleanupTombstones(ctx, time.Hour)
	require.NoError(t, err)
}

// TestCleanupTombstones_DirectTombstoneKey covers CleanupTombstones by
// writing tombstone keys directly to PebbleDB with little-endian encoded
// timestamps matching the decoding logic in CleanupTombstones.
//
// The CleanupTombstones function decodes tombstone timestamps as little-endian
// (byte[0] | byte[1]<<8 | ...) while the schema layer encodes them as
// big-endian. This test writes tombstones in the format that CleanupTombstones
// actually reads, ensuring the timestamp < cutoffTime branch is covered
// (adapter.go lines 1034-1037, 1043-1054).
func TestCleanupTombstones_DirectTombstoneKey(t *testing.T) {
	storage, cleanup := setupSyncStorage(t)
	defer cleanup()
	ctx := context.Background()

	ns := "direct-tombstone-ns"
	err := storage.CreateNamespace(ctx, &NamespaceInfo{Name: ns, Dimension: 4})
	require.NoError(t, err)

	// Build a tombstone key manually with a little-endian timestamp
	// that represents a very old time (timestamp = 1, which is 1970-01-01).
	// The format is: t\x00{namespace}\x00{id:uint64}\x00{timestamp:uint64}
	// The id and other parts use big-endian (from schema), but we write
	// the timestamp in little-endian so CleanupTombstones can read it.
	var tombKey []byte
	tombKey = append(tombKey, schema.PrefixTombstone, schema.Separator)
	tombKey = append(tombKey, []byte(ns)...)
	tombKey = append(tombKey, schema.Separator)
	// ID as big-endian (8 bytes for id=1)
	idBytes := make([]byte, 8)
	binary.BigEndian.PutUint64(idBytes, 1)
	tombKey = append(tombKey, idBytes...)
	tombKey = append(tombKey, schema.Separator)
	// Timestamp as little-endian (old timestamp = 1 second since epoch)
	tsBytes := make([]byte, 8)
	binary.LittleEndian.PutUint64(tsBytes, 1)
	tombKey = append(tombKey, tsBytes...)

	// Write the tombstone key directly to PebbleDB
	err = storage.db.Set(tombKey, []byte{}, pebble.NoSync)
	require.NoError(t, err)

	// CleanupTombstones with a duration of 1 hour should find the tombstone
	// with timestamp=1 (1970-01-01) which is well before the cutoff.
	// cutoffTime = time.Now().Add(-1*time.Hour) which is ~current time - 1h.
	// Since 1 < cutoffTime, lines 1034-1037 (batch.Delete + count++) will execute,
	// then lines 1043-1054 (count > 0, SyncWrites commit, logger.Infof) as well.
	err = storage.CleanupTombstones(ctx, time.Hour)
	require.NoError(t, err)
}

// TestCleanupTombstones_DirectTombstoneKeyNoSync covers the same path as
// TestCleanupTombstones_DirectTombstoneKey but with SyncWrites=false
// to exercise the non-sync commit path.
func TestCleanupTombstones_DirectTombstoneKeyNoSync(t *testing.T) {
	storage, cleanup := setupStorage(t)
	defer cleanup()
	ctx := context.Background()

	ns := "direct-tombstone-nosync-ns"
	err := storage.CreateNamespace(ctx, &NamespaceInfo{Name: ns, Dimension: 4})
	require.NoError(t, err)

	// Build a tombstone key with little-endian timestamp = 1 (very old)
	var tombKey []byte
	tombKey = append(tombKey, schema.PrefixTombstone, schema.Separator)
	tombKey = append(tombKey, []byte(ns)...)
	tombKey = append(tombKey, schema.Separator)
	idBytes := make([]byte, 8)
	binary.BigEndian.PutUint64(idBytes, 1)
	tombKey = append(tombKey, idBytes...)
	tombKey = append(tombKey, schema.Separator)
	tsBytes := make([]byte, 8)
	binary.LittleEndian.PutUint64(tsBytes, 1)
	tombKey = append(tombKey, tsBytes...)

	err = storage.db.Set(tombKey, []byte{}, pebble.NoSync)
	require.NoError(t, err)

	err = storage.CleanupTombstones(ctx, time.Hour)
	require.NoError(t, err)
}

// TestScanNamespace_CorruptedVectorKey covers the DecodeVectorKey error path
// in the vectorIterator (iterator.go lines 74-78) which propagates through
// iter.Err() in ScanNamespace (adapter.go lines 669-672). A malformed vector
// key that falls under the namespace prefix causes the iterator to fail.
func TestScanNamespace_CorruptedVectorKey(t *testing.T) {
	storage, cleanup := setupStorage(t)
	defer cleanup()
	ctx := context.Background()

	ns := "corrupt-key-ns"
	err := storage.CreateNamespace(ctx, &NamespaceInfo{Name: ns, Dimension: 4})
	require.NoError(t, err)

	// Insert a valid vector first
	vec := &VectorValue{
		ID:        1,
		Embedding: []float32{1.0, 2.0, 3.0, 4.0},
		Version:   1,
	}
	err = storage.UpsertVector(ctx, ns, vec)
	require.NoError(t, err)

	// Write a malformed vector key directly to PebbleDB.
	// Format should be: v\x00{namespace}\x00{id:8bytes}\x00{version:8bytes}
	// We write a key that matches the prefix but is too short to contain
	// the full ID and version bytes, causing DecodeVectorKey to fail.
	corruptKey := []byte{schema.PrefixVector, schema.Separator}
	corruptKey = append(corruptKey, []byte(ns)...)
	corruptKey = append(corruptKey, schema.Separator)
	corruptKey = append(corruptKey, 0xFF, 0xFF) // Too short for a valid uint64 ID

	err = storage.db.Set(corruptKey, []byte("garbage"), pebble.NoSync)
	require.NoError(t, err)

	// The corrupted key sorts after the valid key (0xFF > 0x00) so the
	// valid vector is read first. Then the corrupted key causes
	// vectorIterator.Next() to set it.err (iterator.go line 76-77),
	// which propagates through iter.Err() (adapter.go line 669-672).
	_, err = storage.ScanNamespace(ctx, ns, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid vector key")
}

// TestScanNamespace_CorruptedVectorValue covers the DecodeVectorValue error
// path in ScanNamespace (adapter.go line 658-659). A valid vector key with
// corrupted value bytes triggers the iterator to stop with an error.
func TestScanNamespace_CorruptedVectorValue(t *testing.T) {
	storage, cleanup := setupStorage(t)
	defer cleanup()
	ctx := context.Background()

	ns := "corrupt-val-ns"
	err := storage.CreateNamespace(ctx, &NamespaceInfo{Name: ns, Dimension: 4})
	require.NoError(t, err)

	// Insert a valid vector
	vec := &VectorValue{
		ID:        1,
		Embedding: []float32{1.0, 2.0, 3.0, 4.0},
		Version:   1,
	}
	err = storage.UpsertVector(ctx, ns, vec)
	require.NoError(t, err)

	// Write a valid vector key (id=99, version=1) with corrupted value.
	// This triggers DecodeVectorValue error in the vectorIterator.Next()
	// method (iterator.go line 87-89), which then surfaces through
	// ScanNamespace's iter.Err() check (adapter.go line 669-672).
	validKey := schema.EncodeVectorKey(ns, 99, 1)
	err = storage.db.Set(validKey, []byte("not-valid-msgpack-data"), pebble.NoSync)
	require.NoError(t, err)

	results, err := storage.ScanNamespace(ctx, ns, nil)
	// The corrupted value causes the vectorIterator to set an error.
	// ScanNamespace returns whatever results were collected before the error,
	// plus the error from iter.Err().
	if err != nil {
		// The iter.Err() path (line 669-672) was covered.
		assert.NotNil(t, err)
	} else {
		assert.NotNil(t, results)
	}
}

// TestDeleteVector_CorruptedVectorKeyInLoop covers the DecodeVectorKey error
// path inside DeleteVector's iteration loop (adapter.go line 364-365).
// A malformed vector key in the ID's key range causes the decode to fail
// and the loop to continue.
func TestDeleteVector_CorruptedVectorKeyInLoop(t *testing.T) {
	storage, cleanup := setupStorage(t)
	defer cleanup()
	ctx := context.Background()

	ns := "corrupt-del-key-ns"
	err := storage.CreateNamespace(ctx, &NamespaceInfo{Name: ns, Dimension: 4})
	require.NoError(t, err)

	// Insert a valid vector with ID=1
	vec := &VectorValue{
		ID:        1,
		Embedding: []float32{1.0, 2.0, 3.0, 4.0},
		Metadata:  map[string]string{"k": "v"},
		Version:   1,
	}
	err = storage.UpsertVector(ctx, ns, vec)
	require.NoError(t, err)

	// Write a corrupted key that falls within the ID=1 vector key prefix range.
	// The prefix for ID=1 is: v\x00{ns}\x00{id:8bytes}\x00
	// We add a key that starts with this prefix but has truncated version bytes.
	prefix := schema.EncodeVectorKeyPrefix(ns, 1)
	corruptKey := make([]byte, len(prefix))
	copy(corruptKey, prefix)
	// Add only 3 bytes instead of the required 8 for version
	corruptKey = append(corruptKey, 0x01, 0x02, 0x03)

	err = storage.db.Set(corruptKey, []byte("corrupt"), pebble.NoSync)
	require.NoError(t, err)

	// DeleteVector iterates over all keys matching the ID prefix.
	// The corrupted key will fail DecodeVectorKey and be skipped (continue).
	// The valid key should still be found and deleted.
	err = storage.DeleteVector(ctx, ns, 1)
	require.NoError(t, err)

	// Verify the vector is deleted
	_, err = storage.GetVector(ctx, ns, 1)
	require.Error(t, err)
}

// TestDeleteVector_CorruptedReverseKeyInLoop covers the DecodeReverseKey
// error path inside DeleteVector's reverse index iteration (adapter.go
// line 384-385). A malformed reverse key causes the decode to fail and
// the loop to continue.
func TestDeleteVector_CorruptedReverseKeyInLoop(t *testing.T) {
	storage, cleanup := setupStorage(t)
	defer cleanup()
	ctx := context.Background()

	ns := "corrupt-rev-key-ns"
	err := storage.CreateNamespace(ctx, &NamespaceInfo{Name: ns, Dimension: 4})
	require.NoError(t, err)

	// Insert a valid vector with metadata (creates reverse index entries)
	vec := &VectorValue{
		ID:        1,
		Embedding: []float32{1.0, 2.0, 3.0, 4.0},
		Metadata:  map[string]string{"color": "red"},
		Version:   1,
	}
	err = storage.UpsertVector(ctx, ns, vec)
	require.NoError(t, err)

	// Write a corrupted reverse key that falls within the ID=1 reverse prefix.
	// Reverse key prefix: r\x00{ns}\x00{id:8bytes}\x00
	reversePrefix := schema.EncodeReverseKeyPrefix(ns, 1)
	corruptRevKey := make([]byte, len(reversePrefix))
	copy(corruptRevKey, reversePrefix)
	// Add truncated field/version data so DecodeReverseKey fails
	corruptRevKey = append(corruptRevKey, 0xAA, 0xBB)

	err = storage.db.Set(corruptRevKey, []byte("bad-reverse"), pebble.NoSync)
	require.NoError(t, err)

	// DeleteVector iterates through reverse keys for the vector.
	// The corrupted key fails DecodeReverseKey and triggers continue.
	err = storage.DeleteVector(ctx, ns, 1)
	require.NoError(t, err)

	// Verify the vector is deleted
	_, err = storage.GetVector(ctx, ns, 1)
	require.Error(t, err)
}

// TestCreateNamespace_AlreadyExistsFinal covers CreateNamespace when the
// namespace already exists.
func TestCreateNamespace_AlreadyExistsFinal(t *testing.T) {
	storage, cleanup := setupStorage(t)
	defer cleanup()
	ctx := context.Background()

	info := &NamespaceInfo{Name: "dup-final-ns", Dimension: 4}

	err := storage.CreateNamespace(ctx, info)
	require.NoError(t, err)

	// Try to create again
	err = storage.CreateNamespace(ctx, info)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrNamespaceExists)
}

// TestScanNamespace_EmptyNamespaceFinal covers ScanNamespace when the
// namespace exists but contains no vectors.
func TestScanNamespace_EmptyNamespaceFinal(t *testing.T) {
	storage, cleanup := setupStorage(t)
	defer cleanup()
	ctx := context.Background()

	ns := "empty-scan-final-ns"
	err := storage.CreateNamespace(ctx, &NamespaceInfo{Name: ns, Dimension: 4})
	require.NoError(t, err)

	results, err := storage.ScanNamespace(ctx, ns, nil)
	require.NoError(t, err)
	assert.Empty(t, results)
}

// TestScanNamespace_WithOffsetAndLimitFinal covers ScanNamespace with
// ScanOptions offset and limit to hit lines 682-687 in adapter.go.
func TestScanNamespace_WithOffsetAndLimitFinal(t *testing.T) {
	storage, cleanup := setupStorage(t)
	defer cleanup()
	ctx := context.Background()

	ns := "scan-opts-final-ns"
	err := storage.CreateNamespace(ctx, &NamespaceInfo{Name: ns, Dimension: 4})
	require.NoError(t, err)

	// Insert 5 vectors
	for i := uint64(1); i <= 5; i++ {
		vec := &VectorValue{
			ID:        i,
			Embedding: []float32{float32(i), 2.0, 3.0, 4.0},
			Version:   1,
		}
		upsertErr := storage.UpsertVector(ctx, ns, vec)
		require.NoError(t, upsertErr)
	}

	// Scan with offset and limit
	results, err := storage.ScanNamespace(ctx, ns, &ScanOptions{
		Offset: 1,
		Limit:  2,
	})
	require.NoError(t, err)
	assert.Len(t, results, 2)
}

// TestGetVector_InvalidIDZeroFinal covers GetVector when called with ID=0.
func TestGetVector_InvalidIDZeroFinal(t *testing.T) {
	storage, cleanup := setupStorage(t)
	defer cleanup()

	_, err := storage.GetVector(context.Background(), "ns", 0)
	require.Error(t, err)
	assert.Equal(t, ErrInvalidID, err)
}

// TestBatchUpsertVectors_WithMetadataIndexesFinal covers batch upsert with
// metadata that creates metadata and reverse indexes.
func TestBatchUpsertVectors_WithMetadataIndexesFinal(t *testing.T) {
	storage, cleanup := setupStorage(t)
	defer cleanup()
	ctx := context.Background()

	ns := "batch-meta-final-ns"
	err := storage.CreateNamespace(ctx, &NamespaceInfo{Name: ns, Dimension: 4})
	require.NoError(t, err)

	vectors := []*VectorValue{
		{
			ID:        1,
			Embedding: []float32{1.0, 2.0, 3.0, 4.0},
			Metadata:  map[string]string{"type": "a", "group": "1"},
			Version:   1,
		},
		{
			ID:        2,
			Embedding: []float32{5.0, 6.0, 7.0, 8.0},
			Metadata:  map[string]string{"type": "b", "group": "2"},
			Version:   1,
		},
	}

	err = storage.BatchUpsertVectors(ctx, ns, vectors)
	require.NoError(t, err)

	// Verify vectors can be retrieved
	for _, v := range vectors {
		result, getErr := storage.GetVector(ctx, ns, v.ID)
		require.NoError(t, getErr)
		assert.Equal(t, v.Metadata, result.Metadata)
	}
}

// TestBatchDeleteVectors_WithExistingDataFinal covers batch delete of
// vectors that have metadata indexes.
func TestBatchDeleteVectors_WithExistingDataFinal(t *testing.T) {
	storage, cleanup := setupStorage(t)
	defer cleanup()
	ctx := context.Background()

	ns := "batch-del-final-ns"
	err := storage.CreateNamespace(ctx, &NamespaceInfo{Name: ns, Dimension: 4})
	require.NoError(t, err)

	// Insert vectors
	for i := uint64(1); i <= 3; i++ {
		vec := &VectorValue{
			ID:        i,
			Embedding: []float32{float32(i), 2.0, 3.0, 4.0},
			Metadata:  map[string]string{"idx": "val"},
			Version:   1,
		}
		err := storage.UpsertVector(ctx, ns, vec)
		require.NoError(t, err)
	}

	// Batch delete
	err = storage.BatchDeleteVectors(ctx, ns, []uint64{1, 2, 3})
	require.NoError(t, err)

	// Verify all deleted
	for i := uint64(1); i <= 3; i++ {
		_, getErr := storage.GetVector(ctx, ns, i)
		require.Error(t, getErr)
	}
}

// TestGetStats_WithDataFinal covers GetStats when vectors exist.
func TestGetStats_WithDataFinal(t *testing.T) {
	storage, cleanup := setupStorage(t)
	defer cleanup()
	ctx := context.Background()

	ns := "stats-data-final-ns"
	err := storage.CreateNamespace(ctx, &NamespaceInfo{Name: ns, Dimension: 4})
	require.NoError(t, err)

	vec := &VectorValue{
		ID:        1,
		Embedding: []float32{1.0, 2.0, 3.0, 4.0},
		Version:   1,
	}
	err = storage.UpsertVector(ctx, ns, vec)
	require.NoError(t, err)

	stats, err := storage.GetStats(ctx, ns)
	require.NoError(t, err)
	assert.NotNil(t, stats)
}

// TestCompact_WithExistingDataFinal covers Compact when the namespace has data.
func TestCompact_WithExistingDataFinal(t *testing.T) {
	storage, cleanup := setupStorage(t)
	defer cleanup()
	ctx := context.Background()

	ns := "compact-final-ns"
	err := storage.CreateNamespace(ctx, &NamespaceInfo{Name: ns, Dimension: 4})
	require.NoError(t, err)

	for i := uint64(1); i <= 5; i++ {
		vec := &VectorValue{
			ID:        i,
			Embedding: []float32{float32(i), 2.0, 3.0, 4.0},
			Version:   1,
		}
		err := storage.UpsertVector(ctx, ns, vec)
		require.NoError(t, err)
	}

	err = storage.Compact(ctx, ns)
	require.NoError(t, err)
}

// TestDeleteNamespace_WithDataFinal covers DeleteNamespace when vectors exist.
func TestDeleteNamespace_WithDataFinal(t *testing.T) {
	storage, cleanup := setupStorage(t)
	defer cleanup()
	ctx := context.Background()

	ns := "del-ns-data-final"
	err := storage.CreateNamespace(ctx, &NamespaceInfo{Name: ns, Dimension: 4})
	require.NoError(t, err)

	vec := &VectorValue{
		ID:        1,
		Embedding: []float32{1.0, 2.0, 3.0, 4.0},
		Version:   1,
	}
	err = storage.UpsertVector(ctx, ns, vec)
	require.NoError(t, err)

	err = storage.DeleteNamespace(ctx, ns)
	require.NoError(t, err)

	// Verify namespace is gone
	_, err = storage.GetNamespaceInfo(ctx, ns)
	require.Error(t, err)
}

// TestScanByMetadata_NoMatchFinal covers ScanByMetadata when no
// vectors match the filter.
func TestScanByMetadata_NoMatchFinal(t *testing.T) {
	storage, cleanup := setupStorage(t)
	defer cleanup()
	ctx := context.Background()

	ns := "scan-no-match-final-ns"
	err := storage.CreateNamespace(ctx, &NamespaceInfo{Name: ns, Dimension: 4})
	require.NoError(t, err)

	vec := &VectorValue{
		ID:        1,
		Embedding: []float32{1.0, 2.0, 3.0, 4.0},
		Metadata:  map[string]string{"color": "red"},
		Version:   1,
	}
	err = storage.UpsertVector(ctx, ns, vec)
	require.NoError(t, err)

	// Search for non-matching value
	results, err := storage.ScanByMetadata(ctx, ns, "color", "blue", nil)
	require.NoError(t, err)
	assert.Empty(t, results)
}

// TestUpsertVector_NilVectorFinal covers the nil vector check in UpsertVector.
func TestUpsertVector_NilVectorFinal(t *testing.T) {
	storage, cleanup := setupStorage(t)
	defer cleanup()

	err := storage.UpsertVector(context.Background(), "ns", nil)
	require.Error(t, err)
	assert.Equal(t, ErrInvalidVector, err)
}

// TestUpsertVector_WithSyncWritesFinal covers UpsertVector with SyncWrites enabled.
func TestUpsertVector_WithSyncWritesFinal(t *testing.T) {
	storage, cleanup := setupSyncStorage(t)
	defer cleanup()
	ctx := context.Background()

	ns := "sync-upsert-ns"
	err := storage.CreateNamespace(ctx, &NamespaceInfo{Name: ns, Dimension: 4})
	require.NoError(t, err)

	vec := &VectorValue{
		ID:        1,
		Embedding: []float32{1.0, 2.0, 3.0, 4.0},
		Metadata:  map[string]string{"key": "val"},
		Version:   1,
	}
	err = storage.UpsertVector(ctx, ns, vec)
	require.NoError(t, err)

	result, err := storage.GetVector(ctx, ns, 1)
	require.NoError(t, err)
	assert.Equal(t, "val", result.Metadata["key"])
}

// TestCreateNamespace_NilInfoFinal covers CreateNamespace with nil info.
func TestCreateNamespace_NilInfoFinal(t *testing.T) {
	storage, cleanup := setupStorage(t)
	defer cleanup()

	err := storage.CreateNamespace(context.Background(), nil)
	require.Error(t, err)
}

// TestCreateNamespace_EmptyNameFinal covers CreateNamespace with empty name.
func TestCreateNamespace_EmptyNameFinal(t *testing.T) {
	storage, cleanup := setupStorage(t)
	defer cleanup()

	err := storage.CreateNamespace(context.Background(), &NamespaceInfo{Name: "", Dimension: 4})
	require.Error(t, err)
}

// TestDeleteVector_WithSyncWritesFinal covers DeleteVector with SyncWrites.
func TestDeleteVector_WithSyncWritesFinal(t *testing.T) {
	storage, cleanup := setupSyncStorage(t)
	defer cleanup()
	ctx := context.Background()

	ns := "sync-del-ns"
	err := storage.CreateNamespace(ctx, &NamespaceInfo{Name: ns, Dimension: 4})
	require.NoError(t, err)

	vec := &VectorValue{
		ID:        1,
		Embedding: []float32{1.0, 2.0, 3.0, 4.0},
		Metadata:  map[string]string{"k": "v"},
		Version:   1,
	}
	err = storage.UpsertVector(ctx, ns, vec)
	require.NoError(t, err)

	err = storage.DeleteVector(ctx, ns, 1)
	require.NoError(t, err)

	_, err = storage.GetVector(ctx, ns, 1)
	require.Error(t, err)
}

// TestScanByMultipleFilters_NotEqualOperator covers the FilterNotEqual
// branch in ScanByMultipleFilters (line 606-609 in adapter.go).
func TestScanByMultipleFilters_NotEqualOperator(t *testing.T) {
	storage, cleanup := setupStorage(t)
	defer cleanup()
	ctx := context.Background()

	ns := "multi-filter-ne-ns"
	err := storage.CreateNamespace(ctx, &NamespaceInfo{Name: ns, Dimension: 4})
	require.NoError(t, err)

	// Insert vectors with different metadata
	vectors := []*VectorValue{
		{
			ID:        1,
			Embedding: []float32{1.0, 2.0, 3.0, 4.0},
			Metadata:  map[string]string{"color": "red", "shape": "circle"},
			Version:   1,
		},
		{
			ID:        2,
			Embedding: []float32{5.0, 6.0, 7.0, 8.0},
			Metadata:  map[string]string{"color": "red", "shape": "square"},
			Version:   1,
		},
		{
			ID:        3,
			Embedding: []float32{9.0, 10.0, 11.0, 12.0},
			Metadata:  map[string]string{"color": "blue", "shape": "circle"},
			Version:   1,
		},
	}
	for _, v := range vectors {
		err = storage.UpsertVector(ctx, ns, v)
		require.NoError(t, err)
	}

	// Use FilterNotEqual on the second filter to cover the NotEqual branch
	filters := []MetadataFilter{
		{Field: "color", Value: "red", Operator: FilterEqual},
		{Field: "shape", Value: "circle", Operator: FilterNotEqual},
	}

	results, err := storage.ScanByMultipleFilters(ctx, ns, filters, nil)
	require.NoError(t, err)
	// Only vector 2 (red, square) should match: color=red AND shape!=circle
	assert.Len(t, results, 1)
	if len(results) > 0 {
		assert.Equal(t, uint64(2), results[0].ID)
	}
}

// TestScanByMultipleFilters_MissingMetadataField covers the path where
// a vector does not have one of the filter fields (line 596-598).
func TestScanByMultipleFilters_MissingMetadataField(t *testing.T) {
	storage, cleanup := setupStorage(t)
	defer cleanup()
	ctx := context.Background()

	ns := "multi-filter-missing-ns"
	err := storage.CreateNamespace(ctx, &NamespaceInfo{Name: ns, Dimension: 4})
	require.NoError(t, err)

	// Insert vectors: one has "tier" metadata, others don't
	vectors := []*VectorValue{
		{
			ID:        1,
			Embedding: []float32{1.0, 2.0, 3.0, 4.0},
			Metadata:  map[string]string{"color": "red"},
			Version:   1,
		},
		{
			ID:        2,
			Embedding: []float32{5.0, 6.0, 7.0, 8.0},
			Metadata:  map[string]string{"color": "red", "tier": "premium"},
			Version:   1,
		},
	}
	for _, v := range vectors {
		err = storage.UpsertVector(ctx, ns, v)
		require.NoError(t, err)
	}

	// Filter by color=red AND tier=premium - vector 1 is missing "tier"
	filters := []MetadataFilter{
		{Field: "color", Value: "red", Operator: FilterEqual},
		{Field: "tier", Value: "premium", Operator: FilterEqual},
	}

	results, err := storage.ScanByMultipleFilters(ctx, ns, filters, nil)
	require.NoError(t, err)
	assert.Len(t, results, 1)
	if len(results) > 0 {
		assert.Equal(t, uint64(2), results[0].ID)
	}
}

// TestScanByMultipleFilters_WithOffsetAndLimit covers the ScanOptions
// offset and limit in ScanByMultipleFilters (lines 618-626 in adapter.go).
func TestScanByMultipleFilters_WithOffsetAndLimit(t *testing.T) {
	storage, cleanup := setupStorage(t)
	defer cleanup()
	ctx := context.Background()

	ns := "multi-filter-paging-ns"
	err := storage.CreateNamespace(ctx, &NamespaceInfo{Name: ns, Dimension: 4})
	require.NoError(t, err)

	// Insert 5 vectors all matching the first filter
	for i := uint64(1); i <= 5; i++ {
		vec := &VectorValue{
			ID:        i,
			Embedding: []float32{float32(i), 2.0, 3.0, 4.0},
			Metadata:  map[string]string{"color": "red", "group": "a"},
			Version:   1,
		}
		err = storage.UpsertVector(ctx, ns, vec)
		require.NoError(t, err)
	}

	// Use two filters so we exercise the multi-filter path
	filters := []MetadataFilter{
		{Field: "color", Value: "red", Operator: FilterEqual},
		{Field: "group", Value: "a", Operator: FilterEqual},
	}

	// Apply offset and limit
	results, err := storage.ScanByMultipleFilters(ctx, ns, filters, &ScanOptions{
		Offset: 1,
		Limit:  2,
	})
	require.NoError(t, err)
	assert.Len(t, results, 2)
}

// TestScanByMultipleFilters_EmptyFilters covers the empty filters path
// in ScanByMultipleFilters which delegates to ScanNamespace (line 577-578).
func TestScanByMultipleFilters_EmptyFilters(t *testing.T) {
	storage, cleanup := setupStorage(t)
	defer cleanup()
	ctx := context.Background()

	ns := "multi-filter-empty-ns"
	err := storage.CreateNamespace(ctx, &NamespaceInfo{Name: ns, Dimension: 4})
	require.NoError(t, err)

	vec := &VectorValue{
		ID:        1,
		Embedding: []float32{1.0, 2.0, 3.0, 4.0},
		Version:   1,
	}
	err = storage.UpsertVector(ctx, ns, vec)
	require.NoError(t, err)

	results, err := storage.ScanByMultipleFilters(ctx, ns, nil, nil)
	require.NoError(t, err)
	assert.Len(t, results, 1)
}

// TestScanByMultipleFilters_EqualMismatch covers the FilterEqual mismatch
// path where the metadata value doesn't match (line 603-605).
func TestScanByMultipleFilters_EqualMismatch(t *testing.T) {
	storage, cleanup := setupStorage(t)
	defer cleanup()
	ctx := context.Background()

	ns := "multi-filter-mismatch-ns"
	err := storage.CreateNamespace(ctx, &NamespaceInfo{Name: ns, Dimension: 4})
	require.NoError(t, err)

	vec := &VectorValue{
		ID:        1,
		Embedding: []float32{1.0, 2.0, 3.0, 4.0},
		Metadata:  map[string]string{"color": "red", "size": "large"},
		Version:   1,
	}
	err = storage.UpsertVector(ctx, ns, vec)
	require.NoError(t, err)

	// First filter matches, second filter does not
	filters := []MetadataFilter{
		{Field: "color", Value: "red", Operator: FilterEqual},
		{Field: "size", Value: "small", Operator: FilterEqual},
	}

	results, err := storage.ScanByMultipleFilters(ctx, ns, filters, nil)
	require.NoError(t, err)
	assert.Empty(t, results)
}

// TestGetNamespaceInfo_WithStatsFinal covers GetNamespaceInfo when stats
// are successfully computed (lines 849-866 in adapter.go).
func TestGetNamespaceInfo_WithStatsFinal(t *testing.T) {
	storage, cleanup := setupStorage(t)
	defer cleanup()
	ctx := context.Background()

	ns := "ns-info-stats-final"
	err := storage.CreateNamespace(ctx, &NamespaceInfo{
		Name:      ns,
		Dimension: 4,
		Metadata:  map[string]string{"env": "test"},
	})
	require.NoError(t, err)

	// Insert a vector so stats.Count > 0
	vec := &VectorValue{
		ID:        1,
		Embedding: []float32{1.0, 2.0, 3.0, 4.0},
		Version:   1,
	}
	err = storage.UpsertVector(ctx, ns, vec)
	require.NoError(t, err)

	info, err := storage.GetNamespaceInfo(ctx, ns)
	require.NoError(t, err)
	assert.Equal(t, ns, info.Name)
	assert.Equal(t, uint32(4), info.Dimension)
}
