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

package node

import (
	"sync/atomic"

	"github.com/jeremyhahn/go-qrdb/pkg/server"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// nodeServiceRegistry is the service.Registry the node's embedded transport
// scopes databases through (embedded.WithServiceRegistry). It is the running
// server's own per-(tenant, database) registry -- the one every network
// transport resolves requests through -- read at call time, because the
// embedded transport is built before the server and outlives a data-plane
// recycle that replaces it.
//
// One registry is kept per server instance: server.ServiceRegistry builds a
// fresh one per call, and the registry's collection caches carry
// process-local state that must not be rebuilt per request.
type nodeServiceRegistry struct {
	node   *Node
	cached atomic.Pointer[registryForServer]
}

// registryForServer pairs a registry with the server it was built from.
type registryForServer struct {
	srv *server.Server
	reg service.Registry
}

var _ service.Registry = (*nodeServiceRegistry)(nil)

// current returns the live server's registry, or a DataPlaneRecyclingError
// while no server is running (before it starts, or mid-recycle).
func (r *nodeServiceRegistry) current() (service.Registry, error) {
	srv := r.node.srv.Load()
	if srv == nil {
		return nil, &service.DataPlaneRecyclingError{Service: "database registry"}
	}
	if c := r.cached.Load(); c != nil && c.srv == srv {
		return c.reg, nil
	}
	reg := srv.ServiceRegistry()
	if reg == nil {
		return nil, &service.CapabilityUnsupportedError{Message: "database registry: the node has no database registry"}
	}
	r.cached.Store(&registryForServer{srv: srv, reg: reg})
	return reg, nil
}

// KVService implements service.Registry.
func (r *nodeServiceRegistry) KVService(tenant, db string) (service.KVServicer, error) {
	reg, err := r.current()
	if err != nil {
		return nil, err
	}
	return reg.KVService(tenant, db)
}

// MVCCService implements service.Registry.
func (r *nodeServiceRegistry) MVCCService(tenant, db string) (service.MVCCKVServicer, error) {
	reg, err := r.current()
	if err != nil {
		return nil, err
	}
	return reg.MVCCService(tenant, db)
}

// WatchService implements service.Registry.
func (r *nodeServiceRegistry) WatchService(tenant, db string) (service.WatchServicer, error) {
	reg, err := r.current()
	if err != nil {
		return nil, err
	}
	return reg.WatchService(tenant, db)
}

// LeaseService implements service.Registry.
func (r *nodeServiceRegistry) LeaseService(tenant, db string) (service.LeaseServicer, error) {
	reg, err := r.current()
	if err != nil {
		return nil, err
	}
	return reg.LeaseService(tenant, db)
}

// IndexService implements service.Registry.
func (r *nodeServiceRegistry) IndexService(tenant, db string) (service.IndexServicer, error) {
	reg, err := r.current()
	if err != nil {
		return nil, err
	}
	return reg.IndexService(tenant, db)
}

// TimeSeriesService implements service.Registry.
func (r *nodeServiceRegistry) TimeSeriesService(tenant, db string) (service.TimeSeriesServicer, error) {
	reg, err := r.current()
	if err != nil {
		return nil, err
	}
	return reg.TimeSeriesService(tenant, db)
}

// VectorService implements service.Registry.
func (r *nodeServiceRegistry) VectorService(tenant, db string) (service.VectorServicer, error) {
	reg, err := r.current()
	if err != nil {
		return nil, err
	}
	return reg.VectorService(tenant, db)
}

// TransactionService implements service.Registry.
func (r *nodeServiceRegistry) TransactionService(tenant, db string) (service.TransactionServicer, error) {
	reg, err := r.current()
	if err != nil {
		return nil, err
	}
	return reg.TransactionService(tenant, db)
}

// BatchService implements service.Registry.
func (r *nodeServiceRegistry) BatchService(tenant, db string) (service.BatchServicer, error) {
	reg, err := r.current()
	if err != nil {
		return nil, err
	}
	return reg.BatchService(tenant, db)
}

// TableService implements service.Registry.
func (r *nodeServiceRegistry) TableService(tenant, db string) (service.TableServicer, error) {
	reg, err := r.current()
	if err != nil {
		return nil, err
	}
	return reg.TableService(tenant, db)
}

// SequenceService implements service.Registry.
func (r *nodeServiceRegistry) SequenceService(tenant, db string) (service.SequenceServicer, error) {
	reg, err := r.current()
	if err != nil {
		return nil, err
	}
	return reg.SequenceService(tenant, db)
}

// PrivilegeService implements service.Registry.
func (r *nodeServiceRegistry) PrivilegeService(tenant, db string) (service.PrivilegeServicer, error) {
	reg, err := r.current()
	if err != nil {
		return nil, err
	}
	return reg.PrivilegeService(tenant, db)
}

// CollectionService implements service.Registry.
func (r *nodeServiceRegistry) CollectionService(tenant, db string) (service.CollectionServicer, error) {
	reg, err := r.current()
	if err != nil {
		return nil, err
	}
	return reg.CollectionService(tenant, db)
}

// LockService implements service.Registry.
func (r *nodeServiceRegistry) LockService(tenant, db string) (service.LockServicer, error) {
	reg, err := r.current()
	if err != nil {
		return nil, err
	}
	return reg.LockService(tenant, db)
}
