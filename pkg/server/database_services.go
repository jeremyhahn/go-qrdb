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
	"sync"

	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// DatabaseServices holds all service instances scoped to a single database.
// Each database gets its own service set backed by its dedicated shard.
type DatabaseServices struct {
	// Name is the database name.
	Name string

	// ShardID is the Raft shard ID allocated to this database.
	ShardID uint64

	// KV provides key-value operations for this database.
	KV service.KVServicer

	// MVCC provides MVCC operations for this database.
	MVCC service.MVCCKVServicer

	// Batch provides batch operations for this database.
	Batch service.BatchServicer

	// Transaction provides transaction operations for this database.
	Transaction service.TransactionServicer

	// Lease provides lease operations for this database.
	Lease service.LeaseServicer

	// TimeSeries provides time-series operations for this database.
	TimeSeries service.TimeSeriesServicer

	// Index provides secondary index operations for this database.
	Index service.IndexServicer

	// Watch provides watch/notification operations for this database.
	Watch service.WatchServicer

	// Collection provides collection operations for this database.
	Collection service.CollectionServicer

	// Vector provides vector database operations for this database.
	Vector service.VectorServicer

	// Placement reports where this bundle's store lives -- the replicated
	// shared store (composite-namespaced), a store local to this node, or a
	// database's own Raft group. restServiceRegistry.resolve sets it at each
	// of its return points. TableService records it on every reclaim job it
	// publishes (service.WithReclaimIndex) so the sweeper routes the job to
	// the one node that can reach it (go-qrdb#547).
	Placement service.StorePlacement
}

// DatabaseServiceRegistry manages per-database service instances.
// It is safe for concurrent access.
type DatabaseServiceRegistry struct {
	mu       sync.RWMutex
	services map[string]*DatabaseServices // database name -> services

	// defaultDB is the name of the default database used when no
	// database is specified in a request.
	defaultDB string
}

// NewDatabaseServiceRegistry creates an empty registry with the given
// default database name.
func NewDatabaseServiceRegistry(defaultDB string) *DatabaseServiceRegistry {
	return &DatabaseServiceRegistry{
		services:  make(map[string]*DatabaseServices),
		defaultDB: defaultDB,
	}
}

// Register adds a database's services to the registry.
func (r *DatabaseServiceRegistry) Register(name string, svc *DatabaseServices) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.services[name] = svc
}

// Unregister removes a database's services from the registry.
func (r *DatabaseServiceRegistry) Unregister(name string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.services, name)
}

// Get returns the services for the named database.
// Returns nil if not found.
func (r *DatabaseServiceRegistry) Get(name string) *DatabaseServices {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.services[name]
}

// GetOrDefault returns the services for the named database, falling back
// to the default database if name is empty.
func (r *DatabaseServiceRegistry) GetOrDefault(name string) *DatabaseServices {
	if name == "" {
		name = r.defaultDB
	}
	return r.Get(name)
}

// Default returns the services for the default database.
func (r *DatabaseServiceRegistry) Default() *DatabaseServices {
	return r.Get(r.defaultDB)
}

// DefaultName returns the default database name.
func (r *DatabaseServiceRegistry) DefaultName() string {
	return r.defaultDB
}

// List returns the names of all registered databases.
func (r *DatabaseServiceRegistry) List() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	names := make([]string, 0, len(r.services))
	for name := range r.services {
		names = append(names, name)
	}
	return names
}

// Count returns the number of registered databases.
func (r *DatabaseServiceRegistry) Count() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.services)
}
