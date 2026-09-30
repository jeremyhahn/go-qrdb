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

package server

import (
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/database"
)

// ---------------------------------------------------------------------------
// go-qrdb#482 finding (c): resolve's dbReg.GetOrDefault short-circuit
// returns a pre-registered *DatabaseServices bundle WITHOUT ever calling
// databaseExists/database.Manager.Get, so a name that happens to be
// pre-registered (e.g. the default database, or one an embedding registered
// directly) bypassed the drop fence entirely. refuseIfDropping closes that
// by checking database.Manager.Get -- unconditionally, before dbReg is even
// consulted -- whenever a database.Manager is wired in.
// ---------------------------------------------------------------------------

// TestRESTServiceRegistry_RefusesPreRegisteredDatabaseWhileDropping is the
// failing-first test for finding (c): "mydb" is pre-registered in dbReg
// (see newFilledRegistry), which is exactly the path that used to bypass
// the fence, and it is ALSO tracked by a real database.Manager whose
// Dropping flag is set directly (standing in for a Manager.Drop already in
// flight).
func TestRESTServiceRegistry_RefusesPreRegisteredDatabaseWhileDropping(t *testing.T) {
	dbMgr := database.NewManager(nil, t.TempDir())
	db, err := dbMgr.Create(database.DatabaseConfig{
		Name: "mydb", Engine: database.EngineKV, Backend: database.BackendMemory,
	})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	db.SetDropping(true)

	r := NewRESTServiceRegistry(newFilledRegistry(), nil, "system", withDatabaseManager(dbMgr))

	_, err = r.KVService("system", "mydb")
	var dropping *database.DroppingError
	if !errors.As(err, &dropping) {
		t.Fatalf("KVService(%q) error = %v (%T), want *database.DroppingError", "mydb", err, err)
	}
}

// TestRESTServiceRegistry_PreRegisteredDatabaseResolvesWhenNotDropping is
// the control: a database.Manager wired in must not itself break ordinary
// resolution of a pre-registered database that is not being dropped.
func TestRESTServiceRegistry_PreRegisteredDatabaseResolvesWhenNotDropping(t *testing.T) {
	dbMgr := database.NewManager(nil, t.TempDir())
	if _, err := dbMgr.Create(database.DatabaseConfig{
		Name: "mydb", Engine: database.EngineKV, Backend: database.BackendMemory,
	}); err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	r := NewRESTServiceRegistry(newFilledRegistry(), nil, "system", withDatabaseManager(dbMgr))

	svc, err := r.KVService("system", "mydb")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if svc == nil {
		t.Fatal("expected non-nil KVServicer")
	}
}

// TestRESTServiceRegistry_UnmanagedDatabaseUnaffectedByDropFence pins the
// no-op cases refuseIfDropping documents: a database.Manager with no entry
// for the requested name (a pre-registered database an embedding manages
// itself, entirely outside database.Manager's catalog) must resolve exactly
// as it did before the fence existed -- a NotFoundError from Manager.Get is
// not a refusal.
func TestRESTServiceRegistry_UnmanagedDatabaseUnaffectedByDropFence(t *testing.T) {
	dbMgr := database.NewManager(nil, t.TempDir())

	r := NewRESTServiceRegistry(newFilledRegistry(), nil, "system", withDatabaseManager(dbMgr))

	svc, err := r.KVService("system", "mydb")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if svc == nil {
		t.Fatal("expected non-nil KVServicer")
	}
}
