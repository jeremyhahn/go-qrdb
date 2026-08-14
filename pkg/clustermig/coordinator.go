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

package clustermig

import (
	"context"

	"github.com/jeremyhahn/go-qrdb/pkg/seal"
)

// OwnerResolver enumerates the node owners (and the Raft shard) that must each
// recycle for a migration scope. For a tenant database scope this is the
// placement Replicas' NodeIDs and the database's dedicated shard; for the system
// scope it is every cluster member and the system shard. The resolver is the
// single seam between the coordination plane and the placement/membership
// subsystems, keeping clustermig free of those dependencies.
type OwnerResolver interface {
	Owners(ctx context.Context, tenantID, database string) (shardID uint64, owners []string, err error)
}

// CredentialSealer seals a migration's unlock credential individually to each
// owning node, returning a map of NodeID -> opaque per-recipient ciphertext
// (e.g. an HPKE envelope). It is the seam through which the daemon injects the
// node public keys + crypto, keeping clustermig free of key material: the
// coordinator only stores the sealed blobs it returns. credential is the
// plaintext unlock secret (passphrase / Shamir U) the operator supplied; it
// MUST NOT be persisted anywhere except sealed. A nil sealer leaves the record's
// Credentials empty (deployments that supply credentials out of band).
type CredentialSealer interface {
	SealFor(ctx context.Context, m *ClusterMigration, owners []string, credential []byte) (map[string][]byte, error)
}

// Coordinator runs on the cluster leader. When an enable-encryption is requested
// for a scope it resolves the owner set, allocates the next generation, seals the
// unlock credential to each owner, and writes the replicated ClusterMigration
// intent record. It performs no crypto itself (the CredentialSealer seam does)
// and moves no data; per-node execution is the Reconciler's job. The coordinator
// is safe to call on any node — Request is a no-op-with-error when this node is
// not the leader, so the transport layer can route blindly and let the seam gate.
type Coordinator struct {
	store    *Store
	owners   OwnerResolver
	sealer   CredentialSealer
	isLeader func() bool
	nowUnix  func() int64
}

// NewCoordinator constructs a Coordinator. isLeader gates intent writes to the
// leader (the replicated record must be written exactly once per request).
// sealer seals the unlock credential per owner; pass nil for deployments that
// supply credentials out of band. nowUnix stamps the request time; pass nil to
// omit the timestamp (the state machine has no wall clock, so callers supply one).
func NewCoordinator(store *Store, owners OwnerResolver, sealer CredentialSealer, isLeader func() bool, nowUnix func() int64) *Coordinator {
	if isLeader == nil {
		isLeader = func() bool { return true }
	}
	return &Coordinator{store: store, owners: owners, sealer: sealer, isLeader: isLeader, nowUnix: nowUnix}
}

// Request begins a cluster-wide enable-encryption for a scope. It resolves the
// owners, bumps the generation past any prior request for the same scope, seals
// the unlock credential to each owner, and writes the intent record. Only the
// leader writes; a follower returns ErrNotLeader so the caller can forward to
// the leader. An empty tenantID with empty database denotes the system scope.
// credential is the plaintext unlock secret (passphrase / Shamir U) the operator
// supplied; it is sealed per owner via the CredentialSealer and never persisted
// in the clear. credential may be empty when no sealer is wired.
func (c *Coordinator) Request(ctx context.Context, tenantID, database string, opts seal.MigrationOptions, credential []byte) (*ClusterMigration, error) {
	if err := opts.Validate(); err != nil {
		return nil, err
	}
	if !c.isLeader() {
		return nil, ErrNotLeader
	}

	shardID, owners, err := c.owners.Owners(ctx, tenantID, database)
	if err != nil {
		return nil, &StoreError{Op: "resolve-owners", Err: err}
	}
	if len(owners) == 0 {
		return nil, ErrNoOwners
	}

	// Bump the generation past any prior request for this scope so a stale or
	// aborted request's acks never satisfy the new one (the Store completion
	// logic matches on Generation).
	var gen uint64 = 1
	if prev, gerr := c.store.GetMigration(ctx, tenantID, database); gerr != nil {
		return nil, gerr
	} else if prev != nil {
		gen = prev.Generation + 1
	}

	mech := opts.Mechanism
	if mech == "" {
		mech = seal.MechanismSwap
	}
	m := &ClusterMigration{
		TenantID:        tenantID,
		Database:        database,
		ShardID:         shardID,
		Target:          opts.TargetStrategy,
		Mechanism:       mech,
		ShamirThreshold: opts.ShamirThreshold,
		ShamirTotal:     opts.ShamirTotal,
		Owners:          owners,
		Generation:      gen,
	}
	if c.nowUnix != nil {
		m.RequestedAtUnix = c.nowUnix()
	}

	// Seal the unlock credential individually to each owner. The plaintext is
	// dropped here; only the per-recipient ciphertext is stored in the record.
	if c.sealer != nil && len(credential) > 0 {
		sealed, serr := c.sealer.SealFor(ctx, m, owners, credential)
		if serr != nil {
			return nil, &StoreError{Op: "seal-credentials", Err: serr}
		}
		m.Credentials = sealed
	}

	if perr := c.store.PutMigration(ctx, m); perr != nil {
		return nil, perr
	}
	return m, nil
}

// IsLeader reports whether this node is currently the cluster leader (per the
// configured probe). Callers use it to skip leader-only work — e.g. a follower
// avoids scanning the coordination store every reconcile tick just to discover
// it has nothing to reap.
func (c *Coordinator) IsLeader() bool { return c.isLeader() }

// Reap clears completed migration records. The leader calls it after observing
// IsComplete so finished coordination state does not accumulate. It is a no-op
// for an incomplete or absent migration, making it safe to call repeatedly.
func (c *Coordinator) Reap(ctx context.Context, tenantID, database string) (bool, error) {
	if !c.isLeader() {
		return false, nil
	}
	done, err := c.store.IsComplete(ctx, tenantID, database)
	if err != nil || !done {
		return false, err
	}
	if derr := c.store.Delete(ctx, tenantID, database); derr != nil {
		return false, derr
	}
	return true, nil
}
