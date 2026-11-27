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

// A database workspace hands one scoped client to every data tab, so the
// transaction, batch, watch and lock bindings are scoped exactly as the KV,
// MVCC and lease bindings are: a named database reaches the Go SDK's
// DatabaseScoped, so a connection that cannot scope to it refuses the call
// instead of reading or writing a same-named key in the default database.
func TestDataPlaneBindings_ScopeToTheSelectedDatabase(t *testing.T) {
	conn, _ := newTestConnWithMVCC(t)
	tx := NewTransactionBinding(testLogger(t), conn)
	batch := NewBatchBinding(testLogger(t), conn)
	watch := NewWatchBinding(testLogger(t), conn)
	lock := NewLockBinding(testLogger(t), conn)

	put := []WailsOp{{Type: 0, Key: "k", Value: "v"}}
	acquire := &transport.LockAcquireRequest{Key: "k", HolderID: "h", TTL: 1}

	var unsupported *transport.DatabaseScopeUnsupportedError
	calls := map[string]func() error{
		"Transaction.Execute": func() error {
			_, err := tx.Execute("orders", &transport.TransactionRequest{})
			return err
		},
		"Batch.Execute": func() error { _, err := batch.Execute("orders", put); return err },
		"Batch.ImportFile": func() error {
			_, err := batch.ImportFile("orders", "/nonexistent/data.csv", WailsImportOptions{})
			return err
		},
		"Watch.Watch": func() error {
			_, err := watch.Watch("orders", "k", transport.WatchOptions{})
			return err
		},
		"Watch.WatchPrefix": func() error {
			_, err := watch.WatchPrefix("orders", "", transport.WatchOptions{})
			return err
		},
		"Lock.Acquire": func() error { _, err := lock.Acquire("orders", acquire); return err },
		"Lock.TryLock": func() error { _, err := lock.TryLock("orders", acquire); return err },
		"Lock.Release": func() error {
			return lock.Release("orders", &transport.LockReleaseRequest{Key: "k", FencingToken: 1})
		},
		"Lock.Extend": func() error {
			_, err := lock.Extend("orders", &transport.LockExtendRequest{Key: "k", FencingToken: 1})
			return err
		},
		"Lock.Status": func() error { _, err := lock.Status("orders", "k"); return err },
		"Lock.List":   func() error { _, err := lock.List("orders", ""); return err },
	}
	for name, call := range calls {
		if err := call(); !errors.As(err, &unsupported) {
			t.Errorf("%s(orders): want *transport.DatabaseScopeUnsupportedError, got %T: %v", name, err, err)
		}
	}
}
