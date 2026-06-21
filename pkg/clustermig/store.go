// Copyright 2026 Jeremy Hahn
// SPDX-License-Identifier: Apache-2.0
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

// Package clustermig is the replicated coordination plane for a cluster-wide
// enable-encryption migration (per-node barrier architecture, Phase D). The
// leader writes a ClusterMigration intent record; each owning node runs its own
// in-place per-node recycle (the proven single-node mechanism) and writes back a
// NodeMigrationStatus; the migration is complete when every owner acks the
// current Generation. Records live in the same Raft-replicated system KV the
// placement and tenant subsystems use, so every node reads a consistent view and
// the coordination survives leader change and node restart.
//
// This package owns ONLY the replicated record + completion logic. It performs
// no crypto and no data movement: the per-node recycle (TenantHostRegistry /
// system recycle) and the at-rest barrier are elsewhere. That separation is what
// makes the coordination plane independently testable without consensus or
// crypto risk.
package clustermig

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/jeremyhahn/go-qrdb/pkg/seal"
)

// Prefix is the key prefix under which cluster-migration coordination records
// live in the replicated system KV. Like the placement prefix it is hidden from
// user-facing KV list/scan.
const Prefix = "__sys:encmig/"

// nodesSegment separates the migration key from its per-node status records:
//
//	__sys:encmig/{tenant}/{db}                         -> ClusterMigration
//	__sys:encmig/{tenant}/{db}/nodes/{nodeID}          -> NodeMigrationStatus
const nodesSegment = "/nodes/"

// KV is the minimal replicated system-KV substrate the store needs; the shared
// system KVStore satisfies it directly (same substrate as placement/tenant).
type KV interface {
	Put(ctx context.Context, key string, value []byte) error
	Get(ctx context.Context, key string) ([]byte, error)
	Delete(ctx context.Context, key string) error
	Scan(ctx context.Context, prefix string, fn func(key string, value []byte) error) error
}

// ClusterMigration is the replicated intent record written once by the leader
// when a cluster-wide enable-encryption is requested for a barrier.
type ClusterMigration struct {
	// TenantID + Database identify the migrated barrier's scope. An empty
	// Database is the tenant's default/whole-tenant scope; an empty TenantID with
	// Database "" denotes the system barrier.
	TenantID string `json:"tenant_id,omitempty"`
	Database string `json:"database,omitempty"`

	// ShardID is the Raft shard whose replicas must each recycle.
	ShardID uint64 `json:"shard_id,omitempty"`

	// Target is the keyed strategy every owner converges to.
	Target seal.StrategyID `json:"target"`

	// Mechanism selects swap vs in-place recode for each node's recycle.
	Mechanism seal.MigrationMechanism `json:"mechanism,omitempty"`

	// Owners is the NodeID set (from the placement Replicas) that must each
	// confirm. Completion is measured against this set + Generation.
	Owners []string `json:"owners"`

	// Generation is a monotonically increasing request id. A NodeMigrationStatus
	// only counts toward completion when its Generation matches; this guards
	// against a stale/aborted request racing a newer one.
	Generation uint64 `json:"generation"`

	// RequestedAtUnix is the request timestamp (caller-stamped; the state machine
	// has no wall clock).
	RequestedAtUnix int64 `json:"requested_at_unix,omitempty"`

	// Credentials carries, per owner NodeID, that owner's migration unlock
	// credential (passphrase / Shamir unlock secret) SEALED to that node — an
	// opaque per-recipient ciphertext blob (HPKE envelope) produced by the
	// CredentialSealer seam. The plaintext credential NEVER appears here: only the
	// holder of a node's private key can open its own entry, so the replicated
	// record (and the Raft log) carries no secret in the clear. Empty when no
	// sealer is wired (e.g. a deployment that supplies credentials per node).
	Credentials map[string][]byte `json:"credentials,omitempty"`
}

// NodeMigrationStatus is one owning node's replicated progress for a generation.
type NodeMigrationStatus struct {
	NodeID        string              `json:"node_id"`
	Generation    uint64              `json:"generation"`
	Phase         seal.MigrationPhase `json:"phase"`
	Detail        string              `json:"detail,omitempty"`
	UpdatedAtUnix int64               `json:"updated_at_unix,omitempty"`
}

// Store persists ClusterMigration intent + per-node status in the replicated
// system KV. It is stateless beyond its KV reference.
type Store struct {
	kv KV
}

// NewStore constructs a Store over the given replicated KV.
func NewStore(kv KV) *Store {
	return &Store{kv: kv}
}

// migrationKey returns the intent record key for a scope.
func migrationKey(tenantID, database string) string {
	return Prefix + tenantID + "/" + database
}

// nodeStatusKey returns the per-node status key under a scope.
func nodeStatusKey(tenantID, database, nodeID string) string {
	return Prefix + tenantID + "/" + database + nodesSegment + nodeID
}

// PutMigration writes (or overwrites) the intent record for its scope.
func (s *Store) PutMigration(ctx context.Context, m *ClusterMigration) error {
	if m == nil {
		return ErrNilMigration
	}
	if m.Target == "" {
		return ErrEmptyTarget
	}
	b, err := json.Marshal(m)
	if err != nil {
		return &StoreError{Op: "marshal-migration", Err: err}
	}
	if err := s.kv.Put(ctx, migrationKey(m.TenantID, m.Database), b); err != nil {
		return &StoreError{Op: "put-migration", Err: err}
	}
	return nil
}

// GetMigration returns the intent record for a scope, or (nil, nil) when absent.
func (s *Store) GetMigration(ctx context.Context, tenantID, database string) (*ClusterMigration, error) {
	raw, err := s.kv.Get(ctx, migrationKey(tenantID, database))
	if err != nil {
		return nil, &StoreError{Op: "get-migration", Err: err}
	}
	if len(raw) == 0 {
		return nil, nil
	}
	var m ClusterMigration
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, &StoreError{Op: "unmarshal-migration", Err: err}
	}
	return &m, nil
}

// PutNodeStatus writes one node's status for the scope.
func (s *Store) PutNodeStatus(ctx context.Context, tenantID, database string, st *NodeMigrationStatus) error {
	if st == nil || st.NodeID == "" {
		return ErrNilStatus
	}
	b, err := json.Marshal(st)
	if err != nil {
		return &StoreError{Op: "marshal-status", Err: err}
	}
	if err := s.kv.Put(ctx, nodeStatusKey(tenantID, database, st.NodeID), b); err != nil {
		return &StoreError{Op: "put-status", Err: err}
	}
	return nil
}

// GetNodeStatus returns one node's status for the scope, or (nil, nil) when the
// node has not yet recorded a status. The reconciler uses it to decide whether
// this node still owes the current generation.
func (s *Store) GetNodeStatus(ctx context.Context, tenantID, database, nodeID string) (*NodeMigrationStatus, error) {
	raw, err := s.kv.Get(ctx, nodeStatusKey(tenantID, database, nodeID))
	if err != nil {
		return nil, &StoreError{Op: "get-status", Err: err}
	}
	if len(raw) == 0 {
		return nil, nil
	}
	var st NodeMigrationStatus
	if err := json.Unmarshal(raw, &st); err != nil {
		return nil, &StoreError{Op: "unmarshal-status", Err: err}
	}
	return &st, nil
}

// ListNodeStatuses returns all per-node statuses recorded for the scope.
func (s *Store) ListNodeStatuses(ctx context.Context, tenantID, database string) ([]*NodeMigrationStatus, error) {
	prefix := Prefix + tenantID + "/" + database + nodesSegment
	var out []*NodeMigrationStatus
	err := s.kv.Scan(ctx, prefix, func(key string, value []byte) error {
		// Guard against the intent record or nested keys: only direct children.
		if strings.Contains(strings.TrimPrefix(key, prefix), "/") {
			return nil
		}
		var st NodeMigrationStatus
		if err := json.Unmarshal(value, &st); err != nil {
			return &StoreError{Op: "unmarshal-status", Err: err}
		}
		out = append(out, &st)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// ListMigrations returns every active ClusterMigration intent record across all
// scopes. It is used by the per-node reconciler to discover pending work without
// knowing scopes ahead of time. Per-node status children (which share the prefix
// path under nodesSegment) are excluded.
func (s *Store) ListMigrations(ctx context.Context) ([]*ClusterMigration, error) {
	var out []*ClusterMigration
	err := s.kv.Scan(ctx, Prefix, func(key string, value []byte) error {
		// Intent records live at __sys:encmig/{tenant}/{db}; status children carry
		// nodesSegment ("/nodes/"). Skip anything below a nodes segment.
		if strings.Contains(key, nodesSegment) {
			return nil
		}
		var m ClusterMigration
		if err := json.Unmarshal(value, &m); err != nil {
			return &StoreError{Op: "unmarshal-migration", Err: err}
		}
		out = append(out, &m)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// Delete removes the intent record and all per-node statuses for the scope. It
// is called once the migration is complete (or to clear an aborted one).
func (s *Store) Delete(ctx context.Context, tenantID, database string) error {
	statuses, err := s.ListNodeStatuses(ctx, tenantID, database)
	if err != nil {
		return err
	}
	for _, st := range statuses {
		if derr := s.kv.Delete(ctx, nodeStatusKey(tenantID, database, st.NodeID)); derr != nil {
			return &StoreError{Op: "delete-status", Err: derr}
		}
	}
	if derr := s.kv.Delete(ctx, migrationKey(tenantID, database)); derr != nil {
		return &StoreError{Op: "delete-migration", Err: derr}
	}
	return nil
}

// IsComplete reports whether every owner has acked the migration's current
// Generation with a terminal-success phase (PhaseComplete). A node whose status
// is for a stale generation, missing, or in any non-complete phase keeps the
// migration incomplete — the cluster never declares done until all owners
// confirm, but it never blocks on a laggard either (callers poll/observe).
func (s *Store) IsComplete(ctx context.Context, tenantID, database string) (bool, error) {
	m, err := s.GetMigration(ctx, tenantID, database)
	if err != nil {
		return false, err
	}
	if m == nil {
		return false, nil
	}
	statuses, err := s.ListNodeStatuses(ctx, tenantID, database)
	if err != nil {
		return false, err
	}
	done := make(map[string]bool, len(statuses))
	for _, st := range statuses {
		if st.Generation == m.Generation && st.Phase == seal.PhaseComplete {
			done[st.NodeID] = true
		}
	}
	for _, owner := range m.Owners {
		if !done[owner] {
			return false, nil
		}
	}
	return len(m.Owners) > 0, nil
}

// Progress returns (acked, total) for the current generation: how many owners
// have reached PhaseComplete out of the owner set. Useful for status surfacing.
func (s *Store) Progress(ctx context.Context, tenantID, database string) (acked, total int, err error) {
	m, err := s.GetMigration(ctx, tenantID, database)
	if err != nil {
		return 0, 0, err
	}
	if m == nil {
		return 0, 0, nil
	}
	statuses, err := s.ListNodeStatuses(ctx, tenantID, database)
	if err != nil {
		return 0, 0, err
	}
	done := make(map[string]bool, len(statuses))
	for _, st := range statuses {
		if st.Generation == m.Generation && st.Phase == seal.PhaseComplete {
			done[st.NodeID] = true
		}
	}
	for _, owner := range m.Owners {
		if done[owner] {
			acked++
		}
	}
	return acked, len(m.Owners), nil
}

// scopeLabel is a human-readable scope id for logs/errors.
func scopeLabel(tenantID, database string) string {
	if tenantID == "" {
		return "system"
	}
	return fmt.Sprintf("tenant=%s db=%s", tenantID, database)
}
