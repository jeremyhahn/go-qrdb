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
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	qrdbtenant "github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// guardCatalog is a DatabaseServicer that answers GetForTenant from a fixed
// table keyed by (tenant, database); every other method is unused here.
type guardCatalog struct {
	service.DatabaseServicer
	infos map[string]*database.DatabaseInfo
}

func (c *guardCatalog) GetForTenant(ctx context.Context, name string) (*database.DatabaseInfo, error) {
	tenantID, _ := qrdbtenant.IDFromContext(ctx)
	if info, ok := c.infos[tenantID+"/"+name]; ok {
		return info, nil
	}
	return nil, &service.DatabaseNotFoundError{TenantID: tenantID, Database: name}
}

// guardNext is the wrapped registry: it records which scopes reached it.
type guardNext struct {
	service.Registry
	reached []string
}

func (n *guardNext) KVService(tenantID, db string) (service.KVServicer, error) {
	n.reached = append(n.reached, tenantID+"/"+db)
	return nil, nil
}

func (n *guardNext) TimeSeriesService(tenantID, db string) (service.TimeSeriesServicer, error) {
	n.reached = append(n.reached, tenantID+"/"+db)
	return nil, nil
}

func dedicatedBarrierInfo(status database.StatusType) *database.DatabaseInfo {
	return &database.DatabaseInfo{
		Name:    "vault",
		Backend: database.BackendPebble,
		Status:  status,
		DataDir: "/srv/vault",
		Barrier: &database.DatabaseBarrierConfig{Enabled: true},
	}
}

// TestEmbeddedBarrierGuard_RefusesSealedDedicatedBarrier checks that the
// embedded service registry refuses a sealed database carrying its own
// barrier, as the REST server does, and never hands its scope to the shared
// service graph.
func TestEmbeddedBarrierGuard_RefusesSealedDedicatedBarrier(t *testing.T) {
	next := &guardNext{}
	g := &embeddedBarrierGuard{next: next, databases: &guardCatalog{infos: map[string]*database.DatabaseInfo{
		"t1/vault": dedicatedBarrierInfo(database.StatusSealed),
	}}}

	_, err := g.KVService("t1", "vault")
	var sealed *service.BarrierSealedError
	if !errors.As(err, &sealed) || sealed.TenantID != "t1" || sealed.Database != "vault" {
		t.Fatalf("KVService err = %v, want BarrierSealedError for t1/vault", err)
	}
	if _, err := g.TimeSeriesService("t1", "vault"); !errors.As(err, &sealed) {
		t.Fatalf("TimeSeriesService err = %v, want BarrierSealedError", err)
	}
	if err := g.StreamRefusal("t1", "vault"); !errors.As(err, &sealed) {
		t.Fatalf("StreamRefusal err = %v, want BarrierSealedError", err)
	}
	if len(next.reached) != 0 {
		t.Fatalf("sealed database reached the shared graph: %v", next.reached)
	}
}

// TestEmbeddedBarrierGuard_RefusesUnsealedDedicatedBarrier checks that an
// unsealed database whose barrier sits on its own data path is refused as
// unserved: the embedded registry cannot route it to its dedicated store and
// must not serve it from the shared keyspace.
func TestEmbeddedBarrierGuard_RefusesUnsealedDedicatedBarrier(t *testing.T) {
	next := &guardNext{}
	g := &embeddedBarrierGuard{next: next, databases: &guardCatalog{infos: map[string]*database.DatabaseInfo{
		"t1/vault": dedicatedBarrierInfo(database.StatusOpen),
	}}}

	_, err := g.KVService("t1", "vault")
	var notFound *service.DatabaseNotFoundError
	if !errors.As(err, &notFound) {
		t.Fatalf("KVService err = %v, want DatabaseNotFoundError", err)
	}
	if len(next.reached) != 0 {
		t.Fatalf("dedicated-barrier database reached the shared graph: %v", next.reached)
	}
}

// TestEmbeddedBarrierGuard_ServesPlainAndDefault checks that a database with
// no barrier of its own, the default database and an unknown name pass to
// the wrapped registry unchanged.
func TestEmbeddedBarrierGuard_ServesPlainAndDefault(t *testing.T) {
	next := &guardNext{}
	g := &embeddedBarrierGuard{next: next, databases: &guardCatalog{infos: map[string]*database.DatabaseInfo{
		"t1/plain": {Name: "plain", Backend: database.BackendPebble, Status: database.StatusOpen},
	}}}

	for _, db := range []string{"plain", "", "unknown"} {
		if _, err := g.KVService("t1", db); err != nil {
			t.Fatalf("KVService(t1, %q) err = %v, want nil", db, err)
		}
	}
	if len(next.reached) != 3 {
		t.Fatalf("reached = %v, want three scopes", next.reached)
	}
}
