//go:build qrdb_embedded

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
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// TestEmbeddedServerBinding_LocalLeasesExpire: the GUI's embedded server
// sweeps the leases of its local MVCC engine. It used to give the engine an
// anonymous lessor nothing swept, so a lease's key -- and a lock held by a
// holder that went away -- was never deleted.
func TestEmbeddedServerBinding_LocalLeasesExpire(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	log := testLogger(t)
	conn := NewConnectionBinding(log)
	esb := NewEmbeddedServerBinding(log, conn, NewSQLBinding(log, conn))
	if err := esb.StartEmbedded(&EmbeddedConfig{StorageBackend: "memory", DataDir: t.TempDir()}); err != nil {
		t.Fatalf("StartEmbedded: %v", err)
	}
	t.Cleanup(func() { _ = esb.StopEmbedded() })

	client, err := conn.loadClient()
	if err != nil {
		t.Fatalf("client: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	l, err := client.Lease().LeaseGrant(ctx, 1)
	if err != nil {
		t.Fatalf("grant: %v", err)
	}
	if _, err := client.MVCC().PutWithLease(ctx, "gui/leased", []byte("v"), l.ID); err != nil {
		t.Fatalf("put with lease: %v", err)
	}
	if _, err := client.Lock().LockTryLock(ctx, &transport.LockAcquireRequest{Key: "gui/lock", HolderID: "a", TTL: 30}); err != nil {
		t.Fatalf("the embedded binding serves no lock: %v", err)
	}
	for {
		if _, err := client.KV().Get(ctx, "gui/leased"); err != nil {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatal("the leased key outlived its 1s lease: nothing swept it")
		case <-time.After(100 * time.Millisecond):
		}
	}
}

// TestEmbeddedServerBinding_StopWaitsForTheSweeper: StopEmbedded stops the
// lease-expiry sweeper before it closes the backend, and leaves none behind.
func TestEmbeddedServerBinding_StopWaitsForTheSweeper(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	log := testLogger(t)
	conn := NewConnectionBinding(log)
	esb := NewEmbeddedServerBinding(log, conn, NewSQLBinding(log, conn))
	if err := esb.StartEmbedded(&EmbeddedConfig{StorageBackend: "memory", DataDir: t.TempDir()}); err != nil {
		t.Fatalf("StartEmbedded: %v", err)
	}
	if esb.stopLeaseExpiry == nil {
		t.Fatal("a local MVCC binding started no lease-expiry sweeper")
	}
	if err := esb.StopEmbedded(); err != nil {
		t.Fatalf("StopEmbedded: %v", err)
	}
	if esb.stopLeaseExpiry != nil || esb.lessor != nil {
		t.Fatal("StopEmbedded left the sweeper or its lessor behind")
	}
}
