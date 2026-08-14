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
	"github.com/jeremyhahn/go-qrdb/pkg/server"
	"github.com/jeremyhahn/go-qrdb/pkg/server/lifecycle"
)

// databaseServices projects a node's service graph onto the per-database
// service bundle server.DatabaseServiceRegistry holds.
//
// A standalone node serves one shared engine set, so the bundle names the
// DSN's database and points at that set; the registry then layers the
// composite "<tenant>/<database>/" namespace over every engine, which is what
// keeps each (tenant, database) keyspace disjoint. ShardID is left zero: a
// standalone node runs engine-direct over a LocalBackend and has no Raft
// shard to name.
//
// The Table and Sequence servicers the relational catalog needs are NOT set
// here; server.restServiceRegistry derives both from the resolved, already
// namespaced KV/MVCC/Transaction servicers, so deriving them here instead
// would produce a second, unscoped pair.
func databaseServices(name string, svcs *lifecycle.Node) *server.DatabaseServices {
	return &server.DatabaseServices{
		Name:        name,
		KV:          svcs.KV,
		MVCC:        svcs.MVCC,
		Batch:       svcs.Batch,
		Transaction: svcs.Transaction,
		Lease:       svcs.Lease,
		TimeSeries:  svcs.TimeSeries,
		Index:       svcs.Index,
		Watch:       svcs.Watch,
		Collection:  svcs.Collection,
		Vector:      svcs.Vector,
	}
}
