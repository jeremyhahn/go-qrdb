// Copyright (c) 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package database

import (
	"strconv"

	"github.com/jeremyhahn/go-qrdb/pkg/placement"
)

// SetLocalNode records the identity this node is known by in the cluster:
// its Raft address, the form a placement replica's NodeID takes. A database
// whose backend is not raft is held on one node (OD-2): its catalog record
// names the node that created it (DatabaseMeta.OwnerNode), only that node
// opens its store, and every other node answers its lookups with a
// *placement.NotAReplicaError naming the owner, which each transport's
// replica forwarding acts on. An empty identity -- a standalone node --
// records no owner, and a record without one opens on any node.
//
// It is set once, before the catalog is attached and the recorded databases
// are opened (OpenAll).
func (m *Manager) SetLocalNode(id string) {
	m.localNode.Store(&id)
}

// LocalNode returns the identity SetLocalNode recorded, or "" when none was.
func (m *Manager) LocalNode() string {
	if id := m.localNode.Load(); id != nil {
		return *id
	}
	return ""
}

// ownerFor returns the node a database created here from cfg is recorded as
// held by: this one, for a database that is not raft-placed. A raft-placed
// database -- backend raft, or any backend with a Raft config, whose shard
// stores in it -- has none, since its placement names its replicas; neither
// has a provisioned one, which every node creates and holds for itself.
func (m *Manager) ownerFor(cfg DatabaseConfig, provisioned bool) string {
	if provisioned || cfg.Backend == BackendRaft || cfg.Raft != nil {
		return ""
	}
	return m.LocalNode()
}

// ownerRefusal reports whether meta, key's catalog record, is held by
// another node, and if so returns the refusal that sends its requests there:
// a local binding's (placement.NotAReplicaError.LocalBinding), whose one
// replica is the owner. Nothing is opened for such a record on this node.
func (m *Manager) ownerRefusal(key dbKey, meta DatabaseMeta) (*placement.NotAReplicaError, bool) {
	local := m.LocalNode()
	if meta.OwnerNode == "" || meta.OwnerNode == local {
		return nil, false
	}
	return &placement.NotAReplicaError{
		TenantID: key.tenant,
		Database: key.name,
		NodeID:   local,
		Replicas: []placement.Replica{{NodeID: meta.OwnerNode, RaftAddr: meta.OwnerNode}},
	}, true
}

// remoteInfo describes a database another node holds, from its catalog
// record alone: a listing reports it without opening it here.
func remoteInfo(meta DatabaseMeta) DatabaseInfo {
	status := StatusRemote
	if meta.Dropping {
		status = StatusDropping
	}
	info := DatabaseInfo{
		TenantID:     meta.TenantID,
		Name:         meta.Name,
		ID:           meta.ID,
		Backend:      meta.Backend,
		Status:       status,
		Protocol:     meta.Protocol,
		DataDir:      meta.DataDir,
		Mode:         meta.Mode,
		StorageCodec: meta.StorageCodec,
		Replicas:     meta.Replicas,
		DedicatedWAL: meta.DedicatedWAL,
		Raft:         meta.Raft,
		Barrier:      meta.Barrier,
		Connection:   meta.Connection,
		SQL:          meta.SQL,
		OwnerNode:    meta.OwnerNode,
	}
	if meta.Raft != nil {
		info.ShardID = meta.Raft.ShardID
	}
	return info
}

// BindingOwnerUnavailableError reports that a request for a database held on
// one node (DatabaseMeta.OwnerNode) was forwarded to that node and it did not
// serve it: the owner is stopped, unreachable, or not a member this node
// knows. The database is unavailable until its owner is back, which is what
// a backend that is not raft means (OD-2). Err is the forwarding failure.
type BindingOwnerUnavailableError struct {
	TenantID string
	Name     string
	Owner    string
	Err      error
}

// Error returns a human-readable description naming the owner.
func (e *BindingOwnerUnavailableError) Error() string {
	msg := "database: database " + strconv.Quote(e.Name) + " is held by node " + strconv.Quote(e.Owner) + ", which did not serve the request"
	if e.Err != nil {
		msg += ": " + e.Err.Error()
	}
	return msg
}

// Unwrap returns the forwarding failure.
func (e *BindingOwnerUnavailableError) Unwrap() error {
	return e.Err
}
