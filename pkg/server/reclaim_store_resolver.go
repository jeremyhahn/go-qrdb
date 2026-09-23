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
	"context"
	"errors"
	"strconv"

	"github.com/jeremyhahn/go-qrdb/pkg/api/rest"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// reclaimStoreResolver tells service.ReclaimSweeper which node sweeps a
// pending reclaim job and which store the job lives in (go-qrdb#547).
//
// The job was published by a TableService that restServiceRegistry.resolve
// built, and resolve recorded where that store lives on the entry
// (service.StorePlacement). The answer is read back through the same
// registry, so a job is always swept in exactly the (tenant, database) view
// it was written through -- namespace, dedicated store and quota accounting
// included -- rather than through a second routing table that can disagree:
//
//   - StorePlacementShared: the replicated shared store, reachable from every
//     node, swept by the system shard's leader.
//   - StorePlacementNode: a store on one node only (a volume-isolated tenant's
//     Host, a database with its own encrypting store), swept by the node that
//     published it -- no other node can reach it.
//   - StorePlacementShard: a database's own Raft group, swept by that group's
//     current leader, which need not be the system leader.
type reclaimStoreResolver struct {
	reg    reclaimRegistry
	shards reclaimShardLeader
	origin string
}

// reclaimRegistry is what the resolver needs from restServiceRegistry.
type reclaimRegistry interface {
	resolve(tenantID, db string) (*DatabaseServices, error)
	resolveSharedStore(tenantID, db string) (*DatabaseServices, error)
}

// reclaimShardLeader reports whether this node currently leads a database's
// dedicated Raft group. RaftBackendFactory satisfies it.
type reclaimShardLeader interface {
	LeadsDatabase(tenantID, db string) bool
}

var (
	_ service.ReclaimStoreResolver = (*reclaimStoreResolver)(nil)
	_ reclaimRegistry              = (*restServiceRegistry)(nil)
	_ reclaimShardLeader           = (*RaftBackendFactory)(nil)
)

// newReclaimStoreResolver returns a resolver over reg. shards may be nil on
// a node that serves no Raft-isolated tenants; such a node owns no
// StorePlacementShard job.
func newReclaimStoreResolver(reg reclaimRegistry, shards reclaimShardLeader, origin string) *reclaimStoreResolver {
	if isNilShardLeader(shards) {
		shards = nil
	}
	return &reclaimStoreResolver{reg: reg, shards: shards, origin: origin}
}

// reclaimOwnership decides, per placement, whether this node sweeps an
// entry. A placement not listed here has no owner: an entry this build does
// not understand is left alone rather than guessed at.
var reclaimOwnership = map[service.StorePlacement]func(r *reclaimStoreResolver, e service.ReclaimIndexEntry, systemLeader bool) bool{
	service.StorePlacementShared: func(_ *reclaimStoreResolver, _ service.ReclaimIndexEntry, systemLeader bool) bool {
		return systemLeader
	},
	service.StorePlacementNode: func(r *reclaimStoreResolver, e service.ReclaimIndexEntry, _ bool) bool {
		return r.origin != "" && e.Origin == r.origin
	},
	service.StorePlacementShard: func(r *reclaimStoreResolver, e service.ReclaimIndexEntry, _ bool) bool {
		return r.shards != nil && r.shards.LeadsDatabase(e.TenantID, e.Database)
	},
}

// Owns reports whether this node sweeps entry. See the type doc comment.
func (r *reclaimStoreResolver) Owns(entry service.ReclaimIndexEntry, systemLeader bool) bool {
	owns, ok := reclaimOwnership[entry.Placement]
	return ok && owns(r, entry, systemLeader)
}

// ReclaimStore resolves entry's store, called only on the node that owns it.
//
// A shared-store job is resolved without resolve's existence check: that
// check reads this node's local catalog, which the system leader need not
// hold for a database another node created, while the replicated store it
// guards is reachable here regardless. A job whose database has since been
// dropped needs no answer from the catalog either -- the drop reclaimed the
// namespace, job record included, and the sweeper's fence check settles the
// entry.
//
// A node-local or dedicated-group job is resolved through resolve itself,
// and the live placement must match the recorded one: a mismatch means this
// node's routing no longer leads to the store the job was written in, and
// guessing would look for the job in the wrong store. On the publishing node
// a StorePlacementNode database that no longer exists is gone for good --
// that node's catalog is the authority for its own local stores.
func (r *reclaimStoreResolver) ReclaimStore(_ context.Context, entry service.ReclaimIndexEntry) (service.ReclaimStore, error) {
	var (
		svcs *DatabaseServices
		err  error
	)
	if entry.Placement == service.StorePlacementShared {
		svcs, err = r.reg.resolveSharedStore(entry.TenantID, entry.Database)
	} else {
		svcs, err = r.reg.resolve(entry.TenantID, entry.Database)
	}
	if err != nil {
		var notFound *rest.DatabaseNotFoundError
		if entry.Placement == service.StorePlacementNode && errors.As(err, &notFound) {
			return nil, &service.ReclaimStoreGoneError{TenantID: entry.TenantID, Database: entry.Database}
		}
		return nil, err
	}
	if svcs.Placement != entry.Placement {
		return nil, &ReclaimPlacementMismatchError{
			TenantID: entry.TenantID, Database: entry.Database,
			Recorded: string(entry.Placement), Resolved: string(svcs.Placement),
		}
	}
	if svcs.MVCC == nil {
		return nil, notImplemented("mvcc", "")
	}
	if svcs.Transaction == nil {
		return nil, notImplemented("transaction", "")
	}
	return service.NewReclaimStore(svcs.MVCC, svcs.Transaction), nil
}

// resolveSharedStore resolves the tenant (for its quota wrapping) and returns
// the shared, composite-namespaced view of db. See resolveShared.
func (r *restServiceRegistry) resolveSharedStore(tenantID, db string) (*DatabaseServices, error) {
	_, info, err := r.resolveTenant(tenantID)
	if err != nil {
		return nil, err
	}
	return r.resolveShared(tenantID, db, info)
}

// isNilShardLeader reports whether s is nil or a typed-nil pointer.
func isNilShardLeader(s reclaimShardLeader) bool {
	if s == nil {
		return true
	}
	f, ok := s.(*RaftBackendFactory)
	return ok && f == nil
}

// ReclaimPlacementMismatchError is returned when a reclaim job's (tenant,
// database) now resolves to a different kind of store than the one the job
// was published in. The sweeper retries rather than look for the job in a
// store it was never written to.
type ReclaimPlacementMismatchError struct {
	TenantID string
	Database string
	Recorded string
	Resolved string
}

// Error returns a human-readable description.
func (e *ReclaimPlacementMismatchError) Error() string {
	return "server: reclaim job for tenant " + strconv.Quote(e.TenantID) + ", database " + strconv.Quote(e.Database) +
		" was published in a " + strconv.Quote(e.Recorded) + " store but now resolves to a " + strconv.Quote(e.Resolved) + " store"
}
