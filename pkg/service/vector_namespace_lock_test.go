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

package service

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/storage/vector"
)

// TestVectorNamespaceLocker_ReclaimWaitsForAnInFlightWrite proves the
// primitive reclaimVectorNamespaces relies on: a write holding a
// namespace's lock excludes a concurrent exclusive (reclaim) acquisition of
// the SAME namespace's lock until the write releases it, and an exclusive
// acquisition in turn excludes a new write from starting until it releases.
func TestVectorNamespaceLocker_ReclaimWaitsForAnInFlightWrite(t *testing.T) {
	svc, err := NewVectorService(newVecMockStorage(), newVecTestLogger())
	require.NoError(t, err)
	locker := svc.(VectorNamespaceLocker)

	const ns = "acme/orders/"
	unlockWrite := locker.LockVectorNamespaceWrite(ns)

	reclaimGranted := make(chan struct{})
	go func() {
		unlockReclaim := locker.LockVectorNamespaceReclaim(ns)
		close(reclaimGranted)
		unlockReclaim()
	}()

	select {
	case <-reclaimGranted:
		t.Fatal("reclaim acquired the namespace lock while a write still held it")
	case <-time.After(50 * time.Millisecond):
	}

	unlockWrite()

	select {
	case <-reclaimGranted:
	case <-time.After(2 * time.Second):
		t.Fatal("reclaim never acquired the namespace lock after the write released it")
	}

	// And the direction reverses: once reclaim (simulated here directly, since
	// the goroutine above already released it) holds the lock, a new write
	// must wait for it.
	unlockReclaim := locker.LockVectorNamespaceReclaim(ns)
	writeGranted := make(chan struct{})
	go func() {
		unlock := locker.LockVectorNamespaceWrite(ns)
		close(writeGranted)
		unlock()
	}()

	select {
	case <-writeGranted:
		t.Fatal("a write acquired the namespace lock while reclaim still held it")
	case <-time.After(50 * time.Millisecond):
	}

	unlockReclaim()

	select {
	case <-writeGranted:
	case <-time.After(2 * time.Second):
		t.Fatal("the write never acquired the namespace lock after reclaim released it")
	}
}

// TestKeyspaceReclaim_VectorReclaimWaitsForAnInFlightWrite proves
// reclaimVectorNamespaces -- the step Manager.Drop takes to tear down a
// database's vector namespaces -- actually takes the lock a write holds: a
// write that began its check-then-write window before the drop started
// always finishes before the namespace it wrote to is reclaimed.
func TestKeyspaceReclaim_VectorReclaimWaitsForAnInFlightWrite(t *testing.T) {
	storage := newVecMockStorage()
	storage.listNamespacesFn = func(ctx context.Context) ([]vector.NamespaceInfo, error) {
		return []vector.NamespaceInfo{{Name: "acme/orders/default"}}, nil
	}
	vecSvc, err := NewVectorService(storage, newVecTestLogger())
	require.NoError(t, err)

	reclaimer := NewKeyspaceReclaimService(ReclaimTarget{Vector: vecSvc})

	const ns = "acme/orders/"
	unlockWrite := vecSvc.(VectorNamespaceLocker).LockVectorNamespaceWrite(ns)

	done := make(chan error, 1)
	go func() {
		done <- reclaimer.ReclaimDatabaseKeyspace(context.Background(), "acme", "orders", "")
	}()

	select {
	case <-done:
		t.Fatal("ReclaimDatabaseKeyspace returned while a write held the namespace lock")
	case <-time.After(50 * time.Millisecond):
	}

	unlockWrite()

	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(2 * time.Second):
		t.Fatal("ReclaimDatabaseKeyspace did not finish after the write lock was released")
	}
}
