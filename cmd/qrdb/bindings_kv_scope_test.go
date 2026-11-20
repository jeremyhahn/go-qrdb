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
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// KV and lease calls are scoped to the selected database exactly as the MVCC
// calls are: "" and "system" stay on the connection's default database, and a
// named database reaches the Go SDK's DatabaseScoped, so a connection that
// cannot scope to it refuses the call instead of serving the default database
// under that name.
func TestKVAndLeaseBindings_ScopeToTheSelectedDatabase(t *testing.T) {
	conn, _ := newTestConnWithMVCC(t)
	kv := NewKVBinding(testLogger(t), conn)
	lease := NewLeaseBinding(testLogger(t), conn)

	for _, db := range []string{"", "system"} {
		if _, err := kv.List(db, ""); err != nil {
			t.Fatalf("KV List(%q): %v", db, err)
		}
	}

	var unsupported *transport.DatabaseScopeUnsupportedError
	calls := map[string]func() error{
		"KV.Put":    func() error { return kv.Put("orders", "k", "v") },
		"KV.Get":    func() error { _, err := kv.Get("orders", "k"); return err },
		"KV.Delete": func() error { return kv.Delete("orders", "k") },
		"KV.List":   func() error { _, err := kv.List("orders", ""); return err },
		"KV.Exists": func() error { _, err := kv.Exists("orders", "k"); return err },
		"KV.Scan":   func() error { _, err := kv.Scan("orders", ""); return err },
		"Lease.Grant": func() error {
			_, err := lease.Grant("orders", 10)
			return err
		},
		"Lease.Revoke":     func() error { return lease.Revoke("orders", 1) },
		"Lease.KeepAlive":  func() error { _, err := lease.KeepAlive("orders", 1); return err },
		"Lease.TimeToLive": func() error { _, err := lease.TimeToLive("orders", 1); return err },
		"Lease.List":       func() error { _, err := lease.List("orders"); return err },
	}
	for name, call := range calls {
		if err := call(); !errors.As(err, &unsupported) {
			t.Errorf("%s(orders): want *transport.DatabaseScopeUnsupportedError, got %T: %v", name, err, err)
		}
	}
}

// Exists answers false only for a typed key-not-found; every other Get
// failure (a refusal, a sealed store, a lost connection) reaches the caller.
func TestKVBinding_Exists_PropagatesLookupFailures(t *testing.T) {
	missing := NewKVBinding(testLogger(t), newTestConnectionBinding(t, newMockKVService(), newMockAdminService()))
	ok, err := missing.Exists("", "absent")
	if err != nil || ok {
		t.Fatalf("Exists(absent) = %v, %v; want false, nil", ok, err)
	}

	failing := NewKVBinding(testLogger(t), newTestConnectionBinding(t, &errorKVService{}, newMockAdminService()))
	ok, err = failing.Exists("", "k")
	if err == nil {
		t.Fatalf("Exists over a failing Get = %v, nil; want the Get error", ok)
	}
	var knf *transport.KeyNotFoundError
	if errors.As(err, &knf) {
		t.Fatalf("Exists over a failing Get returned a key-not-found: %v", err)
	}
}
