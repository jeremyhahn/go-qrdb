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

package service

import (
	"context"

	"github.com/jeremyhahn/go-qrdb/pkg/compaction"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// ---------------------------------------------------------------------------
// Platform node-keyspace compaction policy
//
// The node keyspace -- every key of the shared store outside all database
// namespaces -- belongs to no tenant database, so its scheduled-compaction
// policy is the platform's. It is the compaction.NodeScope record of the
// shared store's compaction table, written through the same replicated
// command as a database's policy, so every replica holds it and whichever
// node leads the shared store runs it. Only the system tenant may read or
// set it: the admin:node_compaction permission is ScopeCross, and the
// service refuses a caller bound to any other tenant as well, so an
// in-process caller scoped to a tenant is refused exactly as a remote one is.
// ---------------------------------------------------------------------------

// WithNodeCompaction gives the admin service the shared store's compaction
// table, where the platform's node-keyspace policy is kept. Without it the
// policy verbs report *NodeCompactionUnavailableError.
func WithNodeCompaction(store CompactionStateStore) AdminServiceOption {
	return func(s *AdminService) { s.nodeCompaction = store }
}

// NodeCompaction returns the platform's node-keyspace compaction policy,
// nil when none is set.
func (s *AdminService) NodeCompaction(ctx context.Context) (*compaction.Policy, error) {
	if err := s.authorizeNodeCompaction(ctx, "read node compaction policy"); err != nil {
		return nil, err
	}
	st, err := s.nodeCompaction.CompactionState(ctx, compaction.NodeScope)
	if err != nil || st == nil {
		return nil, err
	}
	return st.Policy.Normalized(), nil
}

// SetNodeCompaction sets the platform's node-keyspace compaction policy; nil
// or an off policy removes it. An invalid policy is refused with
// *compaction.InvalidPolicyError before anything is proposed.
func (s *AdminService) SetNodeCompaction(ctx context.Context, policy *compaction.Policy) error {
	if err := s.authorizeNodeCompaction(ctx, "set node compaction policy"); err != nil {
		return err
	}
	if policy != nil {
		if err := policy.Validate(); err != nil {
			return err
		}
	}
	return s.nodeCompaction.SetCompactionPolicy(ctx, compaction.NodeScope,
		compaction.Binding{Policy: policy.Normalized()})
}

// authorizeNodeCompaction refuses a caller bound to a tenant other than the
// system tenant (*tenant.CrossTenantDeniedError), and a node with no
// compaction table for the node keyspace.
func (s *AdminService) authorizeNodeCompaction(ctx context.Context, op string) error {
	if _, err := tenant.EffectiveTenant(ctx, tenant.SystemTenantID, op); err != nil {
		return err
	}
	if s.nodeCompaction == nil {
		return &NodeCompactionUnavailableError{}
	}
	return nil
}

// NodeCompactionUnavailableError is returned by the node-keyspace policy
// verbs on a node composed without the shared store's compaction table: one
// whose shared store keeps no MVCC revision history, or a composition that
// runs no compaction scheduler.
type NodeCompactionUnavailableError struct{}

// Error returns a human-readable description.
func (e *NodeCompactionUnavailableError) Error() string {
	return "service: node-keyspace compaction is not available on this node"
}
