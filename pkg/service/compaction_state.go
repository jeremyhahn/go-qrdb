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
	"encoding/json"
	"errors"
	"time"

	quicraft "github.com/jeremyhahn/go-quicraft/pkg"

	"github.com/jeremyhahn/go-qrdb/pkg/cluster/featuregate"
	"github.com/jeremyhahn/go-qrdb/pkg/compaction"
	"github.com/jeremyhahn/go-qrdb/pkg/protocol"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// ---------------------------------------------------------------------------
// Scheduled-compaction state
//
// A scope's policy and samples are a reserved record in the shard that holds
// its data (pkg/compaction), written only through protocol.OpCompactionState
// and read with CompactionStateQuery. There is no node-local copy: the record
// replicates with the shard's group, snapshots and backups carry it, and a
// new leader -- or a restarted standalone node -- continues from it.
// ---------------------------------------------------------------------------

// CompactionStateStore is a shard's replicated compaction table. The raw
// shard service, MVCCKVService, implements it; the compaction scheduler
// drives a shard through it.
type CompactionStateStore interface {
	// SetCompactionPolicy binds scope's policy (compaction.Binding). An off
	// or nil policy removes the record.
	SetCompactionPolicy(ctx context.Context, scope string, binding compaction.Binding) error
	// ClearCompactionState removes scope's record.
	ClearCompactionState(ctx context.Context, scope string) error
	// RecordCompactionSample records scope's current revision at at, for a
	// period policy. The state machine keeps it only under the sample cap.
	RecordCompactionSample(ctx context.Context, scope string, at time.Time, revision int64) error
	// CompactionState reads scope's record, nil when it has none.
	CompactionState(ctx context.Context, scope string) (*compaction.State, error)
	// CompactionStates reads every scope's record, keyed by scope.
	CompactionStates(ctx context.Context) (map[string]*compaction.State, error)
}

// CompactionPolicyHolder is a database's own view of its scheduled-compaction
// policy: the namespaced MVCC view, which knows the database's scope, owner
// and incarnation and so writes the binding itself.
type CompactionPolicyHolder interface {
	// CompactionPolicy returns the database's policy, nil when it has none.
	CompactionPolicy(ctx context.Context) (*compaction.Policy, error)
	// SetCompactionPolicy sets the database's policy; nil turns it off.
	SetCompactionPolicy(ctx context.Context, policy *compaction.Policy) error
}

// ShardLeader reports whether this node currently leads the shard a service
// proposes to. A standalone node leads its only shard.
type ShardLeader interface {
	LeadsShard() bool
}

var (
	_ CompactionStateStore = (*MVCCKVService)(nil)
	_ ShardLeader          = (*MVCCKVService)(nil)
)

// SetCompactionPolicy implements CompactionStateStore.
func (s *MVCCKVService) SetCompactionPolicy(ctx context.Context, scope string, binding compaction.Binding) error {
	if binding.Policy != nil {
		if err := binding.Policy.Validate(); err != nil {
			return err
		}
	}
	body, err := json.Marshal(binding)
	if err != nil {
		return &EncodeRequestError{Err: err}
	}
	return s.proposeCompactionState(ctx, protocol.CompactionStateCommand{
		Kind: protocol.CompactionStateSetPolicy, Scope: scope, Policy: body,
	})
}

// ClearCompactionState implements CompactionStateStore.
func (s *MVCCKVService) ClearCompactionState(ctx context.Context, scope string) error {
	return s.proposeCompactionState(ctx, protocol.CompactionStateCommand{
		Kind: protocol.CompactionStateClear, Scope: scope,
	})
}

// RecordCompactionSample implements CompactionStateStore.
func (s *MVCCKVService) RecordCompactionSample(ctx context.Context, scope string, at time.Time, revision int64) error {
	return s.proposeCompactionState(ctx, protocol.CompactionStateCommand{
		Kind: protocol.CompactionStateSample, Scope: scope, At: at.UnixNano(), Revision: revision,
	})
}

// proposeCompactionState refuses a scope that is not one database's keyspace
// before proposing, and proposes nothing until the group's feature gate
// reports every replica can apply the command.
func (s *MVCCKVService) proposeCompactionState(ctx context.Context, cmd protocol.CompactionStateCommand) error {
	if !compaction.ValidScope(cmd.Scope) {
		return &CompactPrefixInvalidError{Prefix: cmd.Scope}
	}
	if gated, ok := s.host.(FeatureGatedHost); ok && !gated.FeatureEnabled(s.shardID, featuregate.PrefixCompaction) {
		return &featuregate.NotActiveError{Feature: featuregate.PrefixCompaction, ShardID: s.shardID}
	}
	encoded, err := protocol.EncodeCompactionState(cmd)
	if err != nil {
		return &EncodeRequestError{Err: err}
	}
	result, err := propose(ctx, s.host, s.shardID, encoded)
	if err != nil {
		return translateProposeError(err)
	}
	return checkResultError(result)
}

// CompactionState implements CompactionStateStore.
func (s *MVCCKVService) CompactionState(ctx context.Context, scope string) (*compaction.State, error) {
	got, err := s.host.SyncRead(ctx, s.shardID, CompactionStateQuery{Scope: scope})
	if err != nil {
		return nil, noTableIsEmpty(s.translateReadError(err))
	}
	state, ok := got.(*compaction.State)
	if !ok {
		return nil, &ReadError{Err: &unexpectedResultTypeError{}}
	}
	return state, nil
}

// CompactionStates implements CompactionStateStore.
func (s *MVCCKVService) CompactionStates(ctx context.Context) (map[string]*compaction.State, error) {
	got, err := s.host.SyncRead(ctx, s.shardID, CompactionStatesQuery{})
	if err != nil {
		return nil, noTableIsEmpty(s.translateReadError(err))
	}
	states, ok := got.(map[string]*compaction.State)
	if !ok {
		return nil, &ReadError{Err: &unexpectedResultTypeError{}}
	}
	return states, nil
}

// noTableIsEmpty answers a read of the compaction table on a store whose
// state machine keeps no MVCC history -- it refuses the table's query as an
// unknown query type (*MVCCBackendRequiredError) -- as an empty table: such
// a store refuses OpCompactionState, so it can hold no record. A drop of a
// database on it then has nothing to clear and a scheduler pass nothing to
// run, rather than failing. Every other error is returned as it is.
func noTableIsEmpty(err error) error {
	var noHistory *MVCCBackendRequiredError
	if errors.As(err, &noHistory) {
		return nil
	}
	return err
}

// shardInfoHost is a Host that reports a shard's state -- the quicraft host,
// and the standalone backend, which leads its only shard.
type shardInfoHost interface {
	GetShardInfo(shardID uint64) (*quicraft.ShardInfo, error)
}

// LeadsShard implements ShardLeader. A host that cannot report leadership
// leads nothing: the compaction scheduler then never acts on the shard from
// this node, rather than acting from every node.
func (s *MVCCKVService) LeadsShard() bool {
	h, ok := s.host.(shardInfoHost)
	if !ok {
		return false
	}
	info, err := h.GetShardInfo(s.shardID)
	return err == nil && info != nil && info.IsLeader
}

// CompactionScheduleGuard returns the write guard every compaction and
// sample the schedule proposes for st must carry: st's database's drop fence
// for st's incarnation (DropFenceGuard). Against a database that was dropped,
// or dropped and created again under the name, the state machine refuses the
// proposal, so a record that outlived its database compacts nothing. It
// reports false for a record no droppable database owns -- the node keyspace,
// a default database -- whose proposals carry no guard.
func CompactionScheduleGuard(st *compaction.State) (WriteGuard, bool, error) {
	if st.Database == "" || st.Database == tenant.DefaultDatabase {
		return WriteGuard{}, false, nil
	}
	guard, err := DropFenceGuard(st.Tenant, st.Database, st.Incarnation)
	if err != nil {
		return WriteGuard{}, false, err
	}
	return guard, true, nil
}

// ---------------------------------------------------------------------------
// The database service's port
// ---------------------------------------------------------------------------

// CompactionPolicies sets and reads a database's scheduled-compaction policy
// in the database's own store, for DatabaseService. NewCompactionPolicies
// implements it over the service registry, so a policy is written through the
// same (tenant, database) view -- namespace, drop fence and incarnation
// included -- as every other write to the database.
type CompactionPolicies interface {
	// SetCompactionPolicy sets (tenantID, db)'s policy; nil turns it off.
	SetCompactionPolicy(ctx context.Context, tenantID, db string, policy *compaction.Policy) error
	// CompactionPolicy returns (tenantID, db)'s policy: nil when it has none,
	// or when its store keeps no MVCC history and so cannot have one.
	CompactionPolicy(ctx context.Context, tenantID, db string) (*compaction.Policy, error)
}

// CompactionPolicyWiring is implemented by a database service that takes a
// CompactionPolicies port once the server's registry exists.
type CompactionPolicyWiring interface {
	SetCompactionPolicies(CompactionPolicies)
}

// registryCompactionPolicies implements CompactionPolicies over a Registry.
type registryCompactionPolicies struct {
	reg Registry
}

// NewCompactionPolicies returns the CompactionPolicies port over reg.
func NewCompactionPolicies(reg Registry) CompactionPolicies {
	return &registryCompactionPolicies{reg: reg}
}

// holder resolves (tenantID, db)'s MVCC view as its policy holder. ok is false
// for a database whose store keeps no MVCC history.
func (p *registryCompactionPolicies) holder(tenantID, db string) (CompactionPolicyHolder, bool, error) {
	mvcc, err := p.reg.MVCCService(tenantID, db)
	if err != nil {
		return nil, false, err
	}
	if r, ok := mvcc.(RevisionHistoryReporter); ok && !r.KeepsRevisionHistory() {
		return nil, false, nil
	}
	h, ok := mvcc.(CompactionPolicyHolder)
	return h, ok, nil
}

// SetCompactionPolicy implements CompactionPolicies.
func (p *registryCompactionPolicies) SetCompactionPolicy(ctx context.Context, tenantID, db string, policy *compaction.Policy) error {
	h, ok, err := p.holder(tenantID, db)
	if err != nil {
		return err
	}
	if !ok {
		return &CompactionPolicyUnavailableError{Database: db}
	}
	return h.SetCompactionPolicy(ctx, policy)
}

// CompactionPolicy implements CompactionPolicies.
func (p *registryCompactionPolicies) CompactionPolicy(ctx context.Context, tenantID, db string) (*compaction.Policy, error) {
	h, ok, err := p.holder(tenantID, db)
	if err != nil || !ok {
		return nil, err
	}
	return h.CompactionPolicy(ctx)
}

// CompactionPolicyUnavailableError is returned when a database's policy
// cannot be set: its store keeps no MVCC history, or the service was built
// without the port that reaches the database's store.
type CompactionPolicyUnavailableError struct {
	Database string
}

// Error returns a human-readable description.
func (e *CompactionPolicyUnavailableError) Error() string {
	return "service: database " + e.Database + " cannot hold a compaction policy: its store keeps no MVCC history"
}
