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

package backupsink_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/service/backupsink"
	"github.com/jeremyhahn/go-qrdb/pkg/storage"
	"github.com/jeremyhahn/go-qrdb/pkg/storage/objstore"
)

// failStorage is a test-only storage.Storage that delegates to an inner store
// but injects configurable errors for specific operation types. All injection
// fields are inspected before delegating; zero values mean "no injection".
type failStorage struct {
	inner storage.Storage

	// Get injection: getErr is returned instead of calling inner.Get.
	getErr error

	// Set injection: setErr is returned on the setFailAt-th call (1-indexed).
	// setFailAt == 0 means fail on every call.
	setErr    error
	setFailAt int
	setCalls  int

	// Delete injection: deleteErr is returned on the deleteFailAt-th call.
	deleteErr    error
	deleteFailAt int
	deleteCalls  int

	// List injection: listErr is returned instead of calling inner.List.
	listErr error
}

func (f *failStorage) Get(key []byte) ([]byte, error) {
	if f.getErr != nil {
		return nil, f.getErr
	}
	return f.inner.Get(key)
}

func (f *failStorage) Set(key, value []byte) error {
	f.setCalls++
	if f.setErr != nil && (f.setFailAt == 0 || f.setCalls == f.setFailAt) {
		return f.setErr
	}
	return f.inner.Set(key, value)
}

func (f *failStorage) Delete(key []byte) error {
	f.deleteCalls++
	if f.deleteErr != nil && (f.deleteFailAt == 0 || f.deleteCalls == f.deleteFailAt) {
		return f.deleteErr
	}
	return f.inner.Delete(key)
}

func (f *failStorage) List(prefix []byte) ([][]byte, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	return f.inner.List(prefix)
}

func (f *failStorage) Exists(key []byte) (bool, error) { return f.inner.Exists(key) }
func (f *failStorage) Batch() storage.Batch            { return f.inner.Batch() }
func (f *failStorage) NewIterator(opts *storage.IteratorOptions) storage.Iterator {
	return f.inner.NewIterator(opts)
}
func (f *failStorage) Snapshot() (storage.Snapshot, error) { return f.inner.Snapshot() }
func (f *failStorage) Close() error                        { return f.inner.Close() }

// newFailStore returns a memory-backed objstore engine together with a
// failStorage wrapper around it so tests can inject errors while still
// inspecting the underlying state.
func newFailStore(t *testing.T) (*objstore.Engine, *failStorage) {
	t.Helper()
	inner, err := objstore.New("memory", map[string]string{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = inner.Close() })
	return inner, &failStorage{inner: inner}
}

// --- errors.go: EncodeError and DecodeError ---

func TestEncodeError_ErrorAndUnwrap(t *testing.T) {
	cause := errors.New("json encode failed")
	e := &backupsink.EncodeError{ID: "backup-1", Err: cause}

	msg := e.Error()
	assert.Contains(t, msg, "backup-1", "Error() must include the backup ID")
	assert.Contains(t, msg, "json encode failed", "Error() must include the cause message")
	assert.Equal(t, cause, e.Unwrap(), "Unwrap must return the wrapped cause")
	assert.True(t, errors.Is(e, cause), "errors.Is must traverse Unwrap chain")
}

func TestDecodeError_ErrorAndUnwrap(t *testing.T) {
	cause := errors.New("gob decode failed")
	e := &backupsink.DecodeError{ID: "backup-2", Err: cause}

	msg := e.Error()
	assert.Contains(t, msg, "backup-2", "Error() must include the backup ID")
	assert.Contains(t, msg, "gob decode failed", "Error() must include the cause message")
	assert.Equal(t, cause, e.Unwrap(), "Unwrap must return the wrapped cause")
	assert.True(t, errors.Is(e, cause), "errors.Is must traverse Unwrap chain")
}

// --- Write: storage error branches ---

func TestSink_Write_SetDataFails(t *testing.T) {
	ctx := context.Background()
	_, fail := newFailStore(t)
	fail.setErr = errors.New("storage write error")
	fail.setFailAt = 1 // fail on the first Set (data key)

	sink := backupsink.New(fail)
	err := sink.Write(ctx, service.BackupInfo{ID: "b1"}, map[uint64][]byte{1: []byte("payload")})
	require.Error(t, err, "Write must propagate a storage error on the data key Set")
}

func TestSink_Write_SetMetaFailsRollsBackData(t *testing.T) {
	ctx := context.Background()
	inner, fail := newFailStore(t)
	fail.setErr = errors.New("meta write error")
	fail.setFailAt = 2 // second Set call (meta key) fails

	sink := backupsink.New(fail)
	err := sink.Write(ctx, service.BackupInfo{ID: "b2"}, map[uint64][]byte{1: []byte("payload")})
	require.Error(t, err, "Write must propagate a storage error on the meta key Set")

	// The data key must have been deleted by the best-effort rollback.
	v, getErr := inner.Get([]byte("data/b2"))
	require.NoError(t, getErr)
	assert.Nil(t, v, "data key must be rolled back when the meta write fails")
}

// --- ReadShards: not-found, corrupt data, storage error ---

func TestSink_ReadShards_Missing(t *testing.T) {
	sink := newMemSink(t)
	_, err := sink.ReadShards(context.Background(), "nonexistent")

	var nf *service.BackupNotFoundError
	require.ErrorAs(t, err, &nf)
	assert.Equal(t, "nonexistent", nf.ID)
}

func TestSink_ReadShards_CorruptData(t *testing.T) {
	ctx := context.Background()
	inner, err := objstore.New("memory", map[string]string{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = inner.Close() })

	// Place non-gob bytes at the data key so gob.Decode fails.
	require.NoError(t, inner.Set([]byte("data/myid"), []byte("not-gob-encoded-data")))

	sink := backupsink.New(inner)
	_, err = sink.ReadShards(ctx, "myid")

	var decErr *backupsink.DecodeError
	require.ErrorAs(t, err, &decErr)
	assert.Equal(t, "myid", decErr.ID)
}

func TestSink_ReadShards_StoreError(t *testing.T) {
	ctx := context.Background()
	_, fail := newFailStore(t)
	fail.getErr = errors.New("get failure")

	sink := backupsink.New(fail)
	_, err := sink.ReadShards(ctx, "anyid")
	require.ErrorIs(t, err, fail.getErr, "ReadShards must propagate a storage.Get error")
}

// --- ReadInfo: storage error, corrupt meta ---

func TestSink_ReadInfo_StoreError(t *testing.T) {
	ctx := context.Background()
	_, fail := newFailStore(t)
	fail.getErr = errors.New("get failure")

	sink := backupsink.New(fail)
	_, err := sink.ReadInfo(ctx, "anyid")
	require.ErrorIs(t, err, fail.getErr, "ReadInfo must propagate a storage.Get error")
}

func TestSink_ReadInfo_CorruptMeta(t *testing.T) {
	ctx := context.Background()
	inner, err := objstore.New("memory", map[string]string{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = inner.Close() })

	// Place invalid JSON at the meta key so json.Unmarshal fails.
	require.NoError(t, inner.Set([]byte("meta/myid"), []byte("not-json")))

	sink := backupsink.New(inner)
	_, err = sink.ReadInfo(ctx, "myid")

	var decErr *backupsink.DecodeError
	require.ErrorAs(t, err, &decErr)
	assert.Equal(t, "myid", decErr.ID)
}

// --- List: storage error, sort comparator, ReadInfo error during iteration ---

func TestSink_List_StorageError(t *testing.T) {
	ctx := context.Background()
	_, fail := newFailStore(t)
	fail.listErr = errors.New("list failure")

	sink := backupsink.New(fail)
	_, err := sink.List(ctx)
	require.ErrorIs(t, err, fail.listErr, "List must propagate a storage.List error")
}

func TestSink_List_SortNewestFirst(t *testing.T) {
	ctx := context.Background()
	sink := newMemSink(t)
	now := time.Now()

	older := service.BackupInfo{
		ID:        "backup-older",
		Timestamp: now.Add(-time.Hour),
		Status:    service.BackupStatusComplete,
	}
	newer := service.BackupInfo{
		ID:        "backup-newer",
		Timestamp: now,
		Status:    service.BackupStatusComplete,
	}

	require.NoError(t, sink.Write(ctx, older, map[uint64][]byte{1: []byte("a")}))
	require.NoError(t, sink.Write(ctx, newer, map[uint64][]byte{1: []byte("b")}))

	infos, err := sink.List(ctx)
	require.NoError(t, err)
	require.Len(t, infos, 2)
	// Newest must be first — exercises the sort comparator.
	assert.Equal(t, "backup-newer", infos[0].ID)
	assert.Equal(t, "backup-older", infos[1].ID)
}

func TestSink_List_ReadInfoError(t *testing.T) {
	ctx := context.Background()
	inner, err := objstore.New("memory", map[string]string{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = inner.Close() })

	// Place corrupt JSON under a meta key so ReadInfo fails during List iteration.
	require.NoError(t, inner.Set([]byte("meta/badid"), []byte("not-json")))

	sink := backupsink.New(inner)
	_, err = sink.List(ctx)

	var decErr *backupsink.DecodeError
	require.ErrorAs(t, err, &decErr)
}

// --- Delete: meta-key failure, data-key failure ---

func TestSink_Delete_MetaFails(t *testing.T) {
	ctx := context.Background()
	_, fail := newFailStore(t)
	fail.deleteErr = errors.New("delete meta failure")
	fail.deleteFailAt = 1 // fail on the first Delete (meta key)

	sink := backupsink.New(fail)
	err := sink.Delete(ctx, "someid")
	require.ErrorIs(t, err, fail.deleteErr, "Delete must propagate a meta-key deletion error")
}

func TestSink_Delete_DataFails(t *testing.T) {
	ctx := context.Background()
	_, fail := newFailStore(t)
	fail.deleteErr = errors.New("delete data failure")
	fail.deleteFailAt = 2 // fail on the second Delete (data key)

	sink := backupsink.New(fail)
	err := sink.Delete(ctx, "someid")
	require.ErrorIs(t, err, fail.deleteErr, "Delete must propagate a data-key deletion error")
}
