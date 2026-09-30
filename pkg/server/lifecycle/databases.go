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

package lifecycle

import "github.com/jeremyhahn/go-qrdb/pkg/server"

// SystemDatabase is the name of the node's own database: the default entry
// of the server's database service registry, served by the node's service
// graph.
const SystemDatabase = "system"

// DatabaseServices projects the node's service graph onto the per-database
// service bundle server.DatabaseServiceRegistry holds under name.
//
// A node serves one shared engine set, so every database is a namespace over
// it: the registry layers the composite "<tenant>/<database>/" prefix over
// each engine. The Table and Sequence servicers the relational catalog needs
// are not set here; the registry derives both from the resolved, already
// namespaced KV/MVCC/Transaction servicers, so deriving them here would
// produce a second, unscoped pair. shardID is the Raft shard serving the
// graph, zero on an engine-direct standalone node.
func (n *Node) DatabaseServices(name string, shardID uint64) *server.DatabaseServices {
	return &server.DatabaseServices{
		Name:        name,
		ShardID:     shardID,
		KV:          n.KV,
		MVCC:        n.MVCC,
		Batch:       n.Batch,
		Transaction: n.Transaction,
		Lease:       n.Lease,
		TimeSeries:  n.TimeSeries,
		Index:       n.Index,
		Watch:       n.Watch,
		Collection:  n.Collection,
		Vector:      n.Vector,
	}
}
