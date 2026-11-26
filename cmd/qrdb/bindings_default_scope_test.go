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
	"strings"
	"testing"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// embeddedModes runs fn on a standalone and on a raft embedded server.
func embeddedModes(t *testing.T, fn func(t *testing.T, conn *ConnectionBinding)) {
	t.Helper()
	for _, mode := range []struct {
		name string
		cfg  func(dir string) *EmbeddedConfig
	}{
		{"standalone", func(dir string) *EmbeddedConfig {
			return &EmbeddedConfig{StorageBackend: "memory", DataDir: dir}
		}},
		{"raft", func(dir string) *EmbeddedConfig {
			return &EmbeddedConfig{StorageBackend: "pebble", DataDir: dir, RaftEnabled: true}
		}},
	} {
		t.Run(mode.name, func(t *testing.T) {
			t.Setenv("XDG_CONFIG_HOME", t.TempDir())
			log := testLogger(t)
			conn := NewConnectionBinding(log)
			esb := NewEmbeddedServerBinding(log, conn, NewSQLBinding(log, conn))
			if err := esb.StartEmbedded(mode.cfg(t.TempDir())); err != nil {
				t.Fatalf("StartEmbedded: %v", err)
			}
			t.Cleanup(func() { _ = esb.StopEmbedded() })
			if _, err := NewDatabaseBinding(log, conn).Create(&transport.CreateDatabaseRequest{Name: "orders"}); err != nil {
				t.Fatalf("create database orders: %v", err)
			}
			fn(t, conn)
		})
	}
}

// The default database is served through the registry like every named
// one, so it cannot list, read or delete another database's physical keys.
func TestEmbeddedServerBinding_DefaultDatabaseScoped(t *testing.T) {
	embeddedModes(t, func(t *testing.T, conn *ConnectionBinding) {
		kv := NewKVBinding(testLogger(t), conn)
		if err := kv.Put("orders", "k", "v"); err != nil {
			t.Fatalf("Put on orders: %v", err)
		}
		for _, db := range []string{"", "system"} {
			all, err := kv.Scan(db, "")
			if err != nil {
				t.Fatalf("Scan(%q, \"\"): %v", db, err)
			}
			for k := range all {
				if strings.Contains(k, "orders") {
					t.Fatalf("default database Scan listed orders' key %q", k)
				}
			}
			// The physical key of orders/k, and the bare db-relative form.
			for _, phys := range []string{"system/orders/k", "orders/k"} {
				if v, err := kv.Get(db, phys); err == nil {
					t.Fatalf("default database Get(%q) = %q, want not found", phys, v)
				}
				_ = kv.Delete(db, phys)
			}
		}
		got, err := kv.Get("orders", "k")
		if err != nil || got != "v" {
			t.Fatalf("Get on orders after default deletes = %q, %v; want v", got, err)
		}
	})
}
