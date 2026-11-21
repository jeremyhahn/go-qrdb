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
