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
	"slices"
	"testing"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// TestEmbeddedServerBinding_NamedDatabaseScopes verifies that an embedded
// connection reaches a named database: the GUI's per-database binding calls
// scope the connection's client to it, which needs the node's service
// registry on the embedded transport.
func TestEmbeddedServerBinding_NamedDatabaseScopes(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	log := testLogger(t)
	conn := NewConnectionBinding(log)
	esb := NewEmbeddedServerBinding(log, conn, NewSQLBinding(log, conn))
	if err := esb.StartEmbedded(&EmbeddedConfig{StorageBackend: "memory", DataDir: t.TempDir()}); err != nil {
		t.Fatalf("StartEmbedded: %v", err)
	}
	t.Cleanup(func() { _ = esb.StopEmbedded() })

	if _, err := NewDatabaseBinding(log, conn).Create(&transport.CreateDatabaseRequest{Name: "orders"}); err != nil {
		t.Fatalf("create database orders: %v", err)
	}

	kv := NewKVBinding(log, conn)
	if err := kv.Put("orders", "order/1", "widget"); err != nil {
		t.Fatalf("Put on orders: %v", err)
	}
	got, err := kv.Get("orders", "order/1")
	if err != nil {
		t.Fatalf("Get on orders: %v", err)
	}
	if got != "widget" {
		t.Fatalf("Get on orders = %q, want %q", got, "widget")
	}
	keys, err := kv.List("orders", "order/")
	if err != nil {
		t.Fatalf("List on orders: %v", err)
	}
	if len(keys) != 1 || keys[0] != "order/1" {
		t.Fatalf("List on orders = %v, want [order/1]", keys)
	}

	// The key lives in orders only: the system database does not see it.
	if v, err := kv.Get("system", "order/1"); err == nil && v == "widget" {
		t.Fatalf("Get on system returned orders' value %q", v)
	}

	// A database that was never created is not served as a namespaced view.
	if err := kv.Put("missing", "order/1", "widget"); err == nil {
		t.Fatal("Put on a database that was never created succeeded")
	}

	lease, err := NewLeaseBinding(log, conn).Grant("orders", 60)
	if err != nil {
		t.Fatalf("lease Grant on orders: %v", err)
	}
	if lease == nil || lease.ID == 0 {
		t.Fatalf("lease Grant on orders = %+v, want a lease", lease)
	}
}

// startEmbeddedWithDatabase starts an in-memory embedded desktop server,
// creates database name on it and returns the connection the GUI's
// bindings share.
func startEmbeddedWithDatabase(t *testing.T, name string) *ConnectionBinding {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	log := testLogger(t)
	conn := NewConnectionBinding(log)
	esb := NewEmbeddedServerBinding(log, conn, NewSQLBinding(log, conn))
	if err := esb.StartEmbedded(&EmbeddedConfig{StorageBackend: "memory", DataDir: t.TempDir()}); err != nil {
		t.Fatalf("StartEmbedded: %v", err)
	}
	t.Cleanup(func() { _ = esb.StopEmbedded() })
	if _, err := NewDatabaseBinding(log, conn).Create(&transport.CreateDatabaseRequest{Name: name}); err != nil {
		t.Fatalf("create database %s: %v", name, err)
	}
	return conn
}

// TestEmbeddedServerBinding_NamedDatabaseScopesEverySurface drives the MVCC,
// batch, index and lock bindings the GUI calls against a created database on
// the embedded connection, and proves each refuses a database never created.
func TestEmbeddedServerBinding_NamedDatabaseScopesEverySurface(t *testing.T) {
	const db, missing = "orders", "missing"
	conn := startEmbeddedWithDatabase(t, db)
	log := testLogger(t)

	t.Run("mvcc", func(t *testing.T) {
		mvcc := NewMVCCBinding(log, conn)
		if _, err := mvcc.PutWithLease(db, "mvcc/1", "rev-value", 0); err != nil {
			t.Fatalf("PutWithLease on %s: %v", db, err)
		}
		got, err := mvcc.GetRevision(db, "mvcc/1")
		if err != nil {
			t.Fatalf("GetRevision on %s: %v", db, err)
		}
		if got == nil || string(got.Value) != "rev-value" || got.ModRevision <= 0 {
			t.Fatalf("GetRevision on %s = %+v, want value rev-value at a revision > 0", db, got)
		}
		rev, err := mvcc.CurrentRevision(db)
		if err != nil {
			t.Fatalf("CurrentRevision on %s: %v", db, err)
		}
		if rev < got.ModRevision {
			t.Fatalf("CurrentRevision on %s = %d, want >= %d", db, rev, got.ModRevision)
		}
		at, err := mvcc.GetAtRevision(db, "mvcc/1", got.ModRevision)
		if err != nil {
			t.Fatalf("GetAtRevision on %s: %v", db, err)
		}
		if at == nil || string(at.Value) != "rev-value" {
			t.Fatalf("GetAtRevision on %s = %+v, want value rev-value", db, at)
		}
		if _, err := mvcc.PutWithLease(missing, "mvcc/1", "rev-value", 0); err == nil {
			t.Fatalf("PutWithLease on %s succeeded", missing)
		}
		if _, err := mvcc.CurrentRevision(missing); err == nil {
			t.Fatalf("CurrentRevision on %s succeeded", missing)
		}
	})

	t.Run("batch", func(t *testing.T) {
		ops := []WailsOp{
			{Type: transport.OpPut, Key: "batch/1", Value: "one"},
			{Type: transport.OpPut, Key: "batch/2", Value: "two"},
		}
		if _, err := NewBatchBinding(log, conn).Execute(db, ops); err != nil {
			t.Fatalf("batch Execute on %s: %v", db, err)
		}
		kv := NewKVBinding(log, conn)
		for key, want := range map[string]string{"batch/1": "one", "batch/2": "two"} {
			got, err := kv.Get(db, key)
			if err != nil {
				t.Fatalf("Get %s on %s: %v", key, db, err)
			}
			if got != want {
				t.Fatalf("Get %s on %s = %q, want %q", key, db, got, want)
			}
		}
		if _, err := NewBatchBinding(log, conn).Execute(missing, ops); err == nil {
			t.Fatalf("batch Execute on %s succeeded", missing)
		}
	})

	t.Run("index", func(t *testing.T) {
		idx := NewIndexBinding(log, conn)
		params := IndexCreateParams{Name: "by_sku", FieldName: "sku"}
		if err := idx.Create(db, params); err != nil {
			t.Fatalf("index Create on %s: %v", db, err)
		}
		list, err := idx.List(db)
		if err != nil {
			t.Fatalf("index List on %s: %v", db, err)
		}
		if !slices.Contains(list.Names, "by_sku") {
			t.Fatalf("index List on %s = %v, want by_sku", db, list.Names)
		}
		if err := idx.Drop(db, "by_sku"); err != nil {
			t.Fatalf("index Drop on %s: %v", db, err)
		}
		list, err = idx.List(db)
		if err != nil {
			t.Fatalf("index List after Drop on %s: %v", db, err)
		}
		if slices.Contains(list.Names, "by_sku") {
			t.Fatalf("index List after Drop on %s = %v, still has by_sku", db, list.Names)
		}
		if err := idx.Create(missing, params); err == nil {
			t.Fatalf("index Create on %s succeeded", missing)
		}
		if _, err := idx.List(missing); err == nil {
			t.Fatalf("index List on %s succeeded", missing)
		}
	})

	t.Run("lock", func(t *testing.T) {
		locks := NewLockBinding(log, conn)
		req := &transport.LockAcquireRequest{Key: "lock/1", HolderID: "desktop", TTL: 60}
		info, err := locks.Acquire(db, req)
		if err != nil {
			t.Fatalf("lock Acquire on %s: %v", db, err)
		}
		if info == nil || info.FencingToken <= 0 {
			t.Fatalf("lock Acquire on %s = %+v, want a fencing token", db, info)
		}
		status, err := locks.Status(db, "lock/1")
		if err != nil {
			t.Fatalf("lock Status on %s: %v", db, err)
		}
		if status == nil || status.FencingToken != info.FencingToken {
			t.Fatalf("lock Status on %s = %+v, want token %d", db, status, info.FencingToken)
		}
		if err := locks.Release(db, &transport.LockReleaseRequest{Key: "lock/1", FencingToken: info.FencingToken}); err != nil {
			t.Fatalf("lock Release on %s: %v", db, err)
		}
		if _, err := locks.Acquire(missing, req); err == nil {
			t.Fatalf("lock Acquire on %s succeeded", missing)
		}
		if _, err := locks.Status(missing, "lock/1"); err == nil {
			t.Fatalf("lock Status on %s succeeded", missing)
		}
	})

	t.Run("lease", func(t *testing.T) {
		if _, err := NewLeaseBinding(log, conn).Grant(missing, 60); err == nil {
			t.Fatalf("lease Grant on %s succeeded", missing)
		}
	})
}
