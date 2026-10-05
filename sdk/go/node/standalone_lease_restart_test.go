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
	"time"

	"github.com/stretchr/testify/require"

	qrdbconfig "github.com/jeremyhahn/go-qrdb/pkg/config"
	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// standaloneRestartConfig is an in-process standalone pebble node whose state
// lives in dataDir, so a second node over the same directory is the first
// one's restart.
func standaloneRestartConfig(dataDir string) *qrdbconfig.Config {
	cfg := qrdbconfig.DefaultConfig()
	cfg.Auth.Enabled = qrdbconfig.BoolPtr(false) // not an authorization test; see testhelpers_test.go
	cfg.Raft.DataDir = dataDir
	cfg.Databases = []qrdbconfig.DatabaseConfig{{Name: "default", Backend: string(database.BackendPebble)}}
	cfg.Protocols.REST = false
	cfg.Protocols.GRPC = false
	cfg.Protocols.QUIC = false
	cfg.Protocols.Unix = false
	cfg.Protocols.MCP = false
	return cfg
}

// TestStandaloneNode_LeaseAndLockSurviveRestart: a standalone node keeps its
// leases across a process restart, and a lock held at shutdown is still held
// afterwards -- by the same holder, with a full TTL to renew -- rather than
// lost (and its key left behind forever) as an in-memory lease table did.
func TestStandaloneNode_LeaseAndLockSurviveRestart(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	dataDir := t.TempDir()

	first, err := NewStandaloneNode(ctx, standaloneRestartConfig(dataDir), nil)
	require.NoError(t, err)
	l, err := first.Client().Lease().LeaseGrant(ctx, 60)
	require.NoError(t, err)
	_, err = first.Client().MVCC().PutWithLease(ctx, "svc/registration", []byte("a"), l.ID)
	require.NoError(t, err)
	held, err := first.Client().Lock().LockTryLock(ctx, &transport.LockAcquireRequest{Key: "scheduler", HolderID: "node-a", TTL: 60})
	require.NoError(t, err)
	require.NoError(t, first.Close())

	second, err := NewStandaloneNode(ctx, standaloneRestartConfig(dataDir), nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = second.Close() })
	c := second.Client()

	got, err := c.Lease().LeaseTimeToLive(ctx, l.ID)
	require.NoError(t, err, "the lease did not survive the restart")
	require.Equal(t, int64(60), got.GrantedTTL)
	require.Greater(t, got.RemainingTTL, int64(55), "a recovered lease gets a full TTL")

	status, err := c.Lock().LockStatus(ctx, "scheduler")
	require.NoError(t, err)
	require.NotNil(t, status, "the lock held at shutdown is gone")
	require.Equal(t, "node-a", status.HolderID)

	// Revoking the recovered lock lease frees the lock and deletes its key.
	require.NoError(t, c.Lease().LeaseRevoke(ctx, held.LeaseID))
	next, err := c.Lock().LockTryLock(ctx, &transport.LockAcquireRequest{Key: "scheduler", HolderID: "node-b", TTL: 60})
	require.NoError(t, err, "the lock was not freed by revoking its recovered lease")
	require.Greater(t, next.FencingToken, held.FencingToken)
}
