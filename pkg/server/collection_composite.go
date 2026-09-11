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

// buildCompositeCollectionService builds a CollectionServicer over the
// (tenant, database)-NAMESPACED engines produced by applyComposite, rather
// than copying the one the bundle was constructed with.
//
// The distinction is the whole of go-qrdb#441. A collection document key is
// "__col:{database}:{collection}/{id}" and the database name comes from the
// request, so two tenants naming the same database and collection address the
// same key. Only the namespacing wrapper separates them, and a collection
// service built over the raw engines never meets it.
//
// The sequence servicer is wired whenever the namespaced MVCC and transaction
// servicers are both present, for the same reason lifecycle.BuildServices and
// buildDataPlaneServices wire theirs: without it, IDStrategyIncrement falls
// back to a process-local counter. Here that matters twice over, because the
// counter key "__seq:{database}:__collection:{name}" is namespaced by the same
// wrapper -- so each tenant advances its OWN counter instead of sharing one.
//
// Per-service configuration set on the bundle's own Collection
// (WithDatabaseCodecLookup, WithCollectionQuotaChecker) is NOT inherited,
// because the composite service is built from the bundle's engines rather than
// cloned from its collection service. No composer sets either today; a
// composer that starts to must set it on the engines' side or teach this
// builder about it, and the compile-time nil return below is what keeps the
// omission loud rather than silent.
//
// It returns nil when the namespaced bundle has no KV servicer, which the
// registry accessor reports as ServiceNotImplementedError.
func buildCompositeCollectionService(namespaced *DatabaseServices) service.CollectionServicer {
	if namespaced.KV == nil {
		return nil
	}
	var svc *service.CollectionService
	if namespaced.Index != nil {
		svc = service.NewCollectionServiceWithIndex(namespaced.KV, namespaced.Index)
	} else {
		svc = service.NewCollectionService(namespaced.KV)
	}
	if namespaced.MVCC != nil && namespaced.Transaction != nil {
		svc = svc.WithSequenceService(service.NewSequenceService(namespaced.MVCC, namespaced.Transaction))
	}
	return svc
}

// compositeCollectionCache caches the namespaced collection service built for
// each (source bundle, composite prefix) pair.
//
// It exists for the reason Server.buildServiceRegistry declines to memoize the
// registry: a CollectionService owns the per-collection, process-local
// increment ID generators (service.CollectionService.getOrCreateIDGen), so the
// lifetime of the service is observable in ID allocation. applyComposite runs
// on EVERY resolve, so building one per call would reset those generators per
// REQUEST -- every insert on a collection with no sequence servicer would be
// handed ID 1 and overwrite the previous document. Caching gives the composite
// service exactly the lifetime the bundle's own collection service has today.
//
// The source bundle pointer is kept alongside the entry so that a bundle
// re-registered under the same name -- a database dropped and recreated -- does
// not keep serving a collection service over the previous, closed engines. It
// is the same guard, for the same reason, as dedicatedStoreServiceCache.
//
// It is safe for concurrent use.
type compositeCollectionCache struct {
	mu      sync.RWMutex
	entries map[string]*compositeCollectionEntry
}

// compositeCollectionEntry pairs a cached collection service with the bundle
// it was built from.
type compositeCollectionEntry struct {
	src *DatabaseServices
	svc service.CollectionServicer
}

// newCompositeCollectionCache creates an empty cache.
func newCompositeCollectionCache() *compositeCollectionCache {
	return &compositeCollectionCache{entries: make(map[string]*compositeCollectionEntry)}
}

// getOrBuild returns the collection service for the given composite prefix,
// building one over the namespaced bundle when absent or when src is a
// different *DatabaseServices than the one currently cached under that prefix.
//
// A nil result (the namespaced bundle has no KV servicer) is cached like any
// other, so a bundle with no collection surface does not re-enter the builder
// on every request.
//
// A nil receiver builds WITHOUT caching rather than panicking: restServiceRegistry
// is a package-local struct that this package's tests construct directly, so
// applyComposite must not depend on NewRESTServiceRegistry having run. The
// uncached path is correct, only slower, and it keeps the composite collection
// service namespaced in every construction shape.
func (c *compositeCollectionCache) getOrBuild(src, namespaced *DatabaseServices, prefix string) service.CollectionServicer {
	if c == nil {
		return buildCompositeCollectionService(namespaced)
	}
	c.mu.RLock()
	if e, ok := c.entries[prefix]; ok && e.src == src {
		c.mu.RUnlock()
		return e.svc
	}
	c.mu.RUnlock()

	c.mu.Lock()
	defer c.mu.Unlock()
	if e, ok := c.entries[prefix]; ok && e.src == src {
		return e.svc
	}
	svc := buildCompositeCollectionService(namespaced)
	c.entries[prefix] = &compositeCollectionEntry{src: src, svc: svc}
	return svc
}
