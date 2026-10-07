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

	qrdbconfig "github.com/jeremyhahn/go-qrdb/pkg/config"
	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// newStandaloneNodeForTX boots a real standalone node with no protocol
// servers, so the assertions below drive the in-process transport a consumer
// gets from Client() -- the exact surface go-xkms holds.
func newStandaloneNodeForTX(t *testing.T) *Node {
	t.Helper()
	return newStandaloneNodeOnBackend(t, database.BackendPebble)
}

func newStandaloneNodeOnBackend(t *testing.T, backend database.BackendType) *Node {
	t.Helper()
	cfg := qrdbconfig.DefaultConfig()
	cfg.Auth.Enabled = qrdbconfig.BoolPtr(false) // not an authorization test; see testhelpers_test.go
	cfg.Raft.DataDir = t.TempDir()
	cfg.Databases = []qrdbconfig.DatabaseConfig{{Name: "default", Backend: string(backend)}}
	cfg.Seal.Enabled = true
	cfg.Seal.Strategy = "none"
	cfg.Protocols.REST = false
	cfg.Protocols.GRPC = false
	cfg.Protocols.QUIC = false
	cfg.Protocols.Unix = false
	cfg.Protocols.MCP = false

	n, err := NewStandaloneNode(context.Background(), cfg, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = n.Close() })
	return n
}

// putIfAbsent expresses the guarded create as a transaction, the way a
// consumer's key store does: "create revision is 0" is the precondition that
// means "this key does not exist".
func putIfAbsent(ctx context.Context, tx transport.TransactionClient, key, value string) (*transport.TransactionResponse, error) {
	return tx.ExecuteTransaction(ctx, &transport.TransactionRequest{
		Compares: []transport.Compare{{
			Key:            []byte(key),
			Target:         transport.CompareCreateRevision,
			Result:         transport.CompareEqual,
			CreateRevision: 0,
		}},
		SuccessOps: []transport.Op{{
			Type:  transport.OpPut,
			Key:   []byte(key),
			Value: []byte(value),
		}},
	})
}

// TestStandaloneNode_ServesCompareAndSwap is the verification go-qrdb#62 asks
// for, and the owner ruling of 2026-08-23 that resolved it.
//
// buildEmbeddedOpts gated the transaction service behind `if !standalone`, so
// a consumer holding node.Client() from NewStandaloneNode had NO
// compare-and-swap primitive at all: ExecuteTransaction returned
// "embedded: TransactionServicer must not be nil". go-xkms therefore failed to
// boot on every `storage.backend: qrdb` configuration, because its CA key
// store reaches PutIfAbsent on the first write of a fresh node.
//
// The ruling: a standalone node is engine-direct and single-process, so a
// compare-and-swap there is already linearizable -- consensus is what makes it
// linearizable across REPLICAS, and a standalone node has none.
//
// The second call is what makes this a test of the GUARD rather than of the
// wiring. A transaction service that ignored its preconditions would satisfy
// the first assertion and still be useless: the answer to "did I win the
// claim?" is exactly what a key store depends on.
func TestStandaloneNode_ServesCompareAndSwap(t *testing.T) {
	n := newStandaloneNodeForTX(t)
	ctx := context.Background()

	tx := n.Client().Transaction()
	require.NotNil(t, tx, "standalone Client() exposes no transaction client")

	resp, err := putIfAbsent(ctx, tx, "keys/ca.key", "first")
	require.NoError(t, err, "a standalone node must serve compare-and-swap")
	require.True(t, resp.Succeeded, "the guarded create of an ABSENT key must succeed")

	// The key now exists, so the create-revision-is-zero precondition must
	// fail and the value must be left alone.
	resp, err = putIfAbsent(ctx, tx, "keys/ca.key", "second")
	require.NoError(t, err, "a losing guarded create is a result, not an error")
	require.False(t, resp.Succeeded, "the guarded create of a PRESENT key must fail")

	got, err := n.Client().KV().Get(ctx, "keys/ca.key")
	require.NoError(t, err)
	require.Equal(t, "first", string(got),
		"the losing writer overwrote the winner -- the precondition was not enforced")
}

// TestStandaloneNode_CoordinationFamiliesServed: MVCC and lease are served on
// a standalone node alongside transactions (go-qrdb#582). A missing key is
// the typed not-found, not a capability refusal.
func TestStandaloneNode_CoordinationFamiliesServed(t *testing.T) {
	n := newStandaloneNodeForTX(t)
	ctx := context.Background()
	c := n.Client()

	_, err := c.MVCC().GetRevision(ctx, "k")
	var notFound *transport.KeyNotFoundError
	require.ErrorAs(t, err, &notFound, "MVCC is served in standalone; a missing key is not-found")

	l, err := c.Lease().LeaseGrant(ctx, 10)
	require.NoError(t, err, "lease is served in standalone")
	require.Equal(t, int64(10), l.GrantedTTL)
}

// TestStandaloneNode_FileBackendServesCompareAndSwap: every backend composes
// the MVCC engine, so the file backend serves the guarded create exactly as
// memory and pebble do, and the loser of the claim sees a failed compare.
func TestStandaloneNode_FileBackendServesCompareAndSwap(t *testing.T) {
	n := newStandaloneNodeOnBackend(t, database.BackendFile)
	ctx := context.Background()

	tx := n.Client().Transaction()
	require.NotNil(t, tx, "the transaction client is wired on every backend")

	resp, err := putIfAbsent(ctx, tx, "keys/ca.key", "first")
	require.NoError(t, err)
	require.True(t, resp.Succeeded, "the guarded create of an ABSENT key must succeed")

	resp, err = putIfAbsent(ctx, tx, "keys/ca.key", "second")
	require.NoError(t, err)
	require.False(t, resp.Succeeded, "the guarded create of a PRESENT key must fail")

	got, err := n.Client().KV().Get(ctx, "keys/ca.key")
	require.NoError(t, err)
	require.Equal(t, "first", string(got))
}
