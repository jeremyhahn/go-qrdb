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

package node

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/filesys"
	"github.com/jeremyhahn/go-qrdb/pkg/storage"
	"github.com/jeremyhahn/go-qrdb/pkg/storage/memory"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// newMemoryEngineNode boots a standalone node whose data plane is the
// in-memory storage engine.
func newMemoryEngineNode(t *testing.T) *Node {
	t.Helper()
	return newSeamNode(t, nil, WithStorageFactory(func(string, filesys.FS) (storage.Storage, error) {
		return memory.NewStorage(), nil
	}))
}

// kvVersionCAS writes value to key when key's version is still version, and
// records the failure branch in marker otherwise.
func kvVersionCAS(key, marker string, version int64, value string) *transport.TransactionRequest {
	return &transport.TransactionRequest{
		Compares: []transport.Compare{
			{Key: []byte(key), Target: transport.CompareVersion, Result: transport.CompareEqual, Version: version},
		},
		SuccessOps: []transport.Op{{Type: transport.OpPut, Key: []byte(key), Value: []byte(value)}},
		FailureOps: []transport.Op{{Type: transport.OpPut, Key: []byte(marker), Value: []byte("failure")}},
	}
}

// batchGetKV reads key through a one-op batch and returns what it read.
func batchGetKV(t *testing.T, batch transport.BatchClient, key string) *transport.MVCCKeyValue {
	t.Helper()
	resp, err := batch.ExecuteBatch(context.Background(), []transport.Op{{Type: transport.OpGet, Key: []byte(key)}})
	require.NoError(t, err)
	require.Len(t, resp.Responses, 1)
	require.Len(t, resp.Responses[0].Kvs, 1)
	return resp.Responses[0].Kvs[0]
}

// TestMemoryEngine_KVVersionsAgreeAcrossCalls shows that the three supported
// version reads -- PutWithLease with lease 0, GetRevision and a batch of get
// ops -- report one version on the memory engine, and that a transaction's
// version compare reads that same number.
func TestMemoryEngine_KVVersionsAgreeAcrossCalls(t *testing.T) {
	ctx := context.Background()
	c := newMemoryEngineNode(t).Client()
	const key, marker = "kvversion/key", "kvversion/branch"

	require.NoError(t, c.KV().Put(ctx, key, []byte("v1")))
	put, err := c.MVCC().PutWithLease(ctx, key, []byte("v2"), 0)
	require.NoError(t, err)
	require.Equal(t, int64(2), put.Version, "an overwrite increments the version")
	require.Zero(t, put.LeaseID)
	require.Greater(t, put.ModRevision, put.CreateRevision)

	got, err := c.MVCC().GetRevision(ctx, key)
	require.NoError(t, err)
	require.Equal(t, put, got, "GetRevision reports the put's version and revisions")
	require.Equal(t, put, batchGetKV(t, c.Batch(), key), "a batch get reports the put's version and revisions")

	resp, err := c.Transaction().ExecuteTransaction(ctx, kvVersionCAS(key, marker, put.Version, "v3"))
	require.NoError(t, err)
	require.True(t, resp.Succeeded, "the compare on the read version holds with no intervening write")
	read, err := c.MVCC().GetRevision(ctx, key)
	require.NoError(t, err)
	require.Equal(t, put.Version+1, read.Version)
	require.Equal(t, []byte("v3"), read.Value)
}

// TestMemoryEngine_KVVersionCompareFailsAfterAnInterveningWrite shows the
// read-then-compare-and-set takes the failure branch when a write lands
// between the read and the transaction.
func TestMemoryEngine_KVVersionCompareFailsAfterAnInterveningWrite(t *testing.T) {
	ctx := context.Background()
	c := newMemoryEngineNode(t).Client()
	const key, marker = "kvversion/key", "kvversion/branch"

	read, err := c.MVCC().PutWithLease(ctx, key, []byte("v1"), 0)
	require.NoError(t, err)
	require.Equal(t, int64(1), read.Version, "a created key's version is 1")
	require.NoError(t, c.KV().Put(ctx, key, []byte("intervening")))

	resp, err := c.Transaction().ExecuteTransaction(ctx, kvVersionCAS(key, marker, read.Version, "lost-update"))
	require.NoError(t, err)
	require.False(t, resp.Succeeded, "the compare on a stale version must fail")

	value, err := c.KV().Get(ctx, key)
	require.NoError(t, err)
	require.Equal(t, []byte("intervening"), value)
	branch, err := c.KV().Get(ctx, marker)
	require.NoError(t, err)
	require.Equal(t, []byte("failure"), branch)
	current := batchGetKV(t, c.Batch(), key)
	require.Equal(t, read.Version+1, current.Version)
}
