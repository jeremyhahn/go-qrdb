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

// GenerationPrefix holds, per scope, the highest Generation a reap has
// removed: __sys:encmig-gen/{tenant}/{db} -> generationFloor. It lies outside
// Prefix so no scan of intent records meets it. A request started after the
// reap numbers itself above it, so an ack a node writes late for the reaped
// generation can never count toward the next one.
const GenerationPrefix = "__sys:encmig-gen/"

// KV is the minimal replicated system-KV substrate the store needs; the shared
// system KVStore satisfies it directly (same substrate as placement/tenant).
// The intent record is only ever written conditionally; Put serves the
// per-node status records, each written by its own node alone.
type KV interface {
	Put(ctx context.Context, key string, value []byte) error
	Get(ctx context.Context, key string) ([]byte, error)
	Scan(ctx context.Context, prefix string, fn func(key string, value []byte) error) error

	// PutIfAbsent, CompareAndSwap and CompareAndDelete each write only while
	// the key still holds what its caller read, and the replicated store
	// decides that inside its state machine. They are the fence between two
	// coordinators: the Raft host forwards a deposed leader's proposal, so no
	// leadership check on the writing node can stop it overwriting the
	// record its successor accepted.
	PutIfAbsent(ctx context.Context, key string, value []byte) (stored bool, err error)
	CompareAndSwap(ctx context.Context, key string, expected, next []byte) (swapped bool, err error)
	CompareAndDelete(ctx context.Context, key string, expected []byte) (deleted bool, err error)
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

	// ShamirThreshold / ShamirTotal carry the M-of-N scheme for a Shamir-target
	// migration (seal.MigrationOptions.ShamirThreshold / ShamirTotal). They are
	// ignored for a passphrase target. Every owner's reconciler rebuilds
	// seal.MigrationOptions from this record alone (see LocalRecycler), so these
	// fields MUST round-trip through the replicated record or a Shamir-target
	// migration fails seal.MigrationOptions.Validate() on every retry, forever,
	// regardless of what the operator originally requested.
	ShamirThreshold int `json:"shamir_threshold,omitempty"`
	ShamirTotal     int `json:"shamir_total,omitempty"`

	// Owners is the NodeID set (from the placement Replicas) that must each
	// confirm. Completion is measured against this set + Generation.
	Owners []string `json:"owners"`

	// Generation is a monotonically increasing request id. A NodeMigrationStatus
	// only counts toward completion when its Generation matches; this guards
	// against a stale/aborted request racing a newer one.
	Generation uint64 `json:"generation"`

	// Steps is the rolling order: one entry per owner, recycled one at a
	// time, each only once every step before it is StepServing. The
	// Coordinator alone advances it (Coordinator.Advance) and a Reconciler
	// recycles its node only while the node's step is StepRecycling, so a
	// restarted coordinator resumes from this record. Empty is a request
	// made before featuregate.RollingMigration was activated: every owner
	// recycles at once.
	Steps []NodeStep `json:"steps,omitempty"`

	// RequestedAtUnix is the request timestamp (caller-stamped; the state machine
	// has no wall clock).
	RequestedAtUnix int64 `json:"requested_at_unix,omitempty"`

	// The record carries no credential in any form, encrypted or not: each
	// owner supplies its own locally (CredentialSource). Nothing in the Raft
	// log can unlock a node.
}

// PhaseAwaitingCredential is an owner's phase while its CredentialSource has
// no credential for the migration yet. It is retried on every pass and never
// counted against MaxRecycleAttempts.
const PhaseAwaitingCredential seal.MigrationPhase = "awaiting-credential"

// NodeMigrationStatus is one owning node's replicated progress for a generation.
type NodeMigrationStatus struct {
	NodeID        string              `json:"node_id"`
	Generation    uint64              `json:"generation"`
	Phase         seal.MigrationPhase `json:"phase"`
	Detail        string              `json:"detail,omitempty"`
	UpdatedAtUnix int64               `json:"updated_at_unix,omitempty"`

	// Attempts counts consecutive PhaseFailed recycles this node has recorded
	// for Generation (reset to 0 on a new generation or a PhaseComplete ack).
	// The Reconciler stops retrying — and this record becomes the
	// operator-visible terminal failure — once Attempts reaches
	// MaxRecycleAttempts; Detail then explains the exhaustion. It is the bound
	// on the otherwise-infinite per-tick retry loop.
	Attempts int `json:"attempts,omitempty"`
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

// generationFloorKey returns the reaped-generation high-water key for a scope.
func generationFloorKey(tenantID, database string) string {
	return GenerationPrefix + tenantID + "/" + database
}

// generationFloor is the record at generationFloorKey.
type generationFloor struct {
	Generation uint64 `json:"generation"`
}

// ReapedGeneration returns the highest Generation reaped for the scope, or 0
// when none has been. A new request numbers itself above both it and any
// record still present.
func (s *Store) ReapedGeneration(ctx context.Context, tenantID, database string) (uint64, error) {
	f, _, err := s.getGenerationFloor(ctx, tenantID, database)
	return f.Generation, err
}

func (s *Store) getGenerationFloor(ctx context.Context, tenantID, database string) (generationFloor, []byte, error) {
	raw, err := s.kv.Get(ctx, generationFloorKey(tenantID, database))
	if err != nil {
		return generationFloor{}, nil, &StoreError{Op: "get-generation-floor", Err: err}
	}
	var f generationFloor
	if len(raw) == 0 {
		return f, nil, nil
	}
	if err := json.Unmarshal(raw, &f); err != nil {
		return generationFloor{}, nil, &StoreError{Op: "unmarshal-generation-floor", Err: err}
	}
	return f, raw, nil
}

// raiseGenerationFloor raises the scope's reaped-generation high-water mark
// to gen, conditionally on the value read, and never lowers it.
func (s *Store) raiseGenerationFloor(ctx context.Context, tenantID, database string, gen uint64) error {
	key := generationFloorKey(tenantID, database)
	next, err := json.Marshal(generationFloor{Generation: gen})
	if err != nil {
		return &StoreError{Op: "marshal-generation-floor", Err: err}
	}
	for {
		f, raw, err := s.getGenerationFloor(ctx, tenantID, database)
		if err != nil {
			return err
		}
		if f.Generation >= gen {
			return nil
		}
		var raised bool
		if raw == nil {
			raised, err = s.kv.PutIfAbsent(ctx, key, next)
		} else {
			raised, err = s.kv.CompareAndSwap(ctx, key, raw, next)
		}
		if err != nil {
			return &StoreError{Op: "raise-generation-floor", Err: err}
		}
		if raised {
			return nil
		}
		if err := ctx.Err(); err != nil {
			return &StoreError{Op: "raise-generation-floor", Err: err}
		}
	}
}

// MigrationVersion is one stored state of a scope's intent record as a reader
// saw it: the exact encoded bytes, which a conditional write compares against
// inside the replicated store. The zero value is "no record".
type MigrationVersion struct {
	raw []byte
}

// CompareAndSwapMigration writes next as its scope's intent record only while
// the stored record is still expected: it creates the record when expected is
// the zero MigrationVersion and the scope has none, and otherwise replaces the
// exact record expected names. It reports false with a nil error when the
// record changed since it was read, leaving the store as it is; the caller
// re-reads and decides again from what is there now.
func (s *Store) CompareAndSwapMigration(ctx context.Context, expected MigrationVersion, next *ClusterMigration) (bool, error) {
	if next == nil {
		return false, ErrNilMigration
	}
	if next.Target == "" {
		return false, ErrEmptyTarget
	}
	b, err := json.Marshal(next)
	if err != nil {
		return false, &StoreError{Op: "marshal-migration", Err: err}
	}
	key := migrationKey(next.TenantID, next.Database)
	if expected.raw == nil {
		stored, perr := s.kv.PutIfAbsent(ctx, key, b)
		if perr != nil {
			return false, &StoreError{Op: "create-migration", Err: perr}
		}
		return stored, nil
	}
	swapped, err := s.kv.CompareAndSwap(ctx, key, expected.raw, b)
	if err != nil {
		return false, &StoreError{Op: "swap-migration", Err: err}
	}
	return swapped, nil
}

// GetMigration returns the intent record for a scope, or (nil, nil) when absent.
func (s *Store) GetMigration(ctx context.Context, tenantID, database string) (*ClusterMigration, error) {
	m, _, err := s.GetMigrationVersion(ctx, tenantID, database)
	return m, err
}

// GetMigrationVersion returns the intent record for a scope with the version
// a conditional write of it compares against; (nil, zero, nil) when absent.
func (s *Store) GetMigrationVersion(ctx context.Context, tenantID, database string) (*ClusterMigration, MigrationVersion, error) {
	raw, err := s.kv.Get(ctx, migrationKey(tenantID, database))
	if err != nil {
		return nil, MigrationVersion{}, &StoreError{Op: "get-migration", Err: err}
	}
	if len(raw) == 0 {
		return nil, MigrationVersion{}, nil
	}
	var m ClusterMigration
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, MigrationVersion{}, &StoreError{Op: "unmarshal-migration", Err: err}
	}
	return &m, MigrationVersion{raw: raw}, nil
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

// CompareAndDeleteMigration removes a finished migration: the intent record
// expected names, and the per-node statuses for its generation or an earlier
// one. It first raises the scope's reaped-generation floor to the record's
// generation (ReapedGeneration), so the scope's next request is numbered
// above it. Every delete is conditional on the bytes it read, so nothing a newer
// request wrote is removed -- neither its record nor an ack for its
// generation. It reports false with a nil error, leaving the record, when the
// record is no longer the one expected.
func (s *Store) CompareAndDeleteMigration(ctx context.Context, expected MigrationVersion) (bool, error) {
	if expected.raw == nil {
		return false, ErrNilMigration
	}
	var m ClusterMigration
	if err := json.Unmarshal(expected.raw, &m); err != nil {
		return false, &StoreError{Op: "unmarshal-migration", Err: err}
	}
	// The floor goes first: a request that finds the intent gone has also
	// found the floor raised, and numbers itself past the reaped generation.
	if err := s.raiseGenerationFloor(ctx, m.TenantID, m.Database, m.Generation); err != nil {
		return false, err
	}
	prefix := Prefix + m.TenantID + "/" + m.Database + nodesSegment
	stale := make(map[string][]byte)
	err := s.kv.Scan(ctx, prefix, func(key string, value []byte) error {
		if !strings.Contains(strings.TrimPrefix(key, prefix), "/") {
			stale[key] = value
		}
		return nil
	})
	if err != nil {
		return false, &StoreError{Op: "scan-status", Err: err}
	}
	for key, raw := range stale {
		if err := s.deleteStatusThrough(ctx, key, raw, m.Generation); err != nil {
			return false, err
		}
	}
	deleted, err := s.kv.CompareAndDelete(ctx, migrationKey(m.TenantID, m.Database), expected.raw)
	if err != nil {
		return false, &StoreError{Op: "delete-migration", Err: err}
	}
	return deleted, nil
}

// deleteStatusThrough deletes the status record at key while it acks gen or
// an earlier generation, starting from raw, the value last read there. A
// record rewritten in between is read again and judged afresh; an ack for a
// later generation is kept.
func (s *Store) deleteStatusThrough(ctx context.Context, key string, raw []byte, gen uint64) error {
	for len(raw) > 0 {
		var st NodeMigrationStatus
		if err := json.Unmarshal(raw, &st); err != nil {
			return &StoreError{Op: "unmarshal-status", Err: err}
		}
		if st.Generation > gen {
			return nil
		}
		deleted, err := s.kv.CompareAndDelete(ctx, key, raw)
		if err != nil {
			return &StoreError{Op: "delete-status", Err: err}
		}
		if deleted {
			return nil
		}
		if err := ctx.Err(); err != nil {
			return &StoreError{Op: "delete-status", Err: err}
		}
		if raw, err = s.kv.Get(ctx, key); err != nil {
			return &StoreError{Op: "get-status", Err: err}
		}
	}
	return nil
}

// IsComplete reports whether every owner has acked the migration's current
// Generation with a terminal-success phase (PhaseComplete). A node whose status
// is for a stale generation, missing, or in any non-complete phase keeps the
// migration incomplete — the cluster never declares done until all owners
// confirm, but it never blocks on a laggard either (callers poll/observe).
// A rolling migration (one with Steps) is complete once every step is
// StepServing: the last owner's ack alone is not enough until the
// Coordinator has recorded it serving.
func (s *Store) IsComplete(ctx context.Context, tenantID, database string) (bool, error) {
	m, err := s.GetMigration(ctx, tenantID, database)
	if err != nil {
		return false, err
	}
	if m == nil {
		return false, nil
	}
	return s.complete(ctx, m)
}

// complete reports whether m, a record already read, is complete in the
// sense IsComplete documents.
func (s *Store) complete(ctx context.Context, m *ClusterMigration) (bool, error) {
	if len(m.Steps) > 0 {
		return m.rolledOut(), nil
	}
	statuses, err := s.ListNodeStatuses(ctx, m.TenantID, m.Database)
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
	r, err := s.Report(ctx, tenantID, database)
	if err != nil || r == nil {
		return 0, 0, err
	}
	return r.Acked, r.Total, nil
}

// scopeLabel is a human-readable scope id for logs/errors.
func scopeLabel(tenantID, database string) string {
	if tenantID == "" {
		return "system"
	}
	return fmt.Sprintf("tenant=%s db=%s", tenantID, database)
}
