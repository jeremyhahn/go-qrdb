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

// Package service provides the QRDB service layer that encapsulates business
// logic and provides a clean separation between public API interfaces and the
// underlying QuicRaft host operations.
package service

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"time"

	quicraft "github.com/jeremyhahn/go-quicraft/pkg"
	"github.com/jeremyhahn/go-quicraft/pkg/config"
	"github.com/jeremyhahn/go-quicraft/pkg/sm"
	"github.com/jeremyhahn/go-quicraft/pkg/sm/kv"

	"github.com/jeremyhahn/go-qrdb/pkg/capabilities"
	"github.com/jeremyhahn/go-qrdb/pkg/consistency"
	"github.com/jeremyhahn/go-qrdb/pkg/protocol"
	"github.com/jeremyhahn/go-qrdb/pkg/statemachine"
	"github.com/jeremyhahn/go-qrdb/pkg/writemode"
)

// KVServicer provides KV data-plane operations with linearizable reads
// and writes.
//
// Implementations exist for several engines — Raft consensus, MVCC, and a
// direct storage backend — so the doc comments below describe the guarantee,
// not the mechanism. Each implementation states how it achieves it.
type KVServicer interface {
	// Put stores a key-value pair.
	Put(ctx context.Context, key string, value []byte) error

	// PutIfAbsent atomically stores value under key only if key does not
	// already exist. It reports stored=true when this call created the key and
	// stored=false, with a nil error, when the key already existed — in which
	// case the store is unchanged. A non-nil error means the outcome is
	// unknown and the caller must not assume either way.
	//
	// This is the only write on this interface with a defined outcome under
	// concurrency. Put always overwrites, and a caller performing Get-then-Put
	// has a window between the two in which another caller can interleave, so
	// both observe "absent" and both write. Anything that must happen exactly
	// once — consuming a single-use token, claiming a name, electing one
	// winner — requires this.
	//
	// Every engine implements it natively: Raft evaluates the condition inside
	// the state machine so replicas cannot diverge, MVCC uses a transaction,
	// and the backend-storage engine uses its own atomic primitive.
	PutIfAbsent(ctx context.Context, key string, value []byte) (stored bool, err error)

	// CompareAndDelete atomically removes key only when its stored value
	// still equals expected, reporting whether this call removed it. It is
	// the release half of PutIfAbsent: an unguarded delete can un-decide what
	// a guarded create decided (go-qrdb#41).
	CompareAndDelete(ctx context.Context, key string, expected []byte) (deleted bool, err error)

	// Get retrieves a value by key with a linearizable read.
	Get(ctx context.Context, key string) ([]byte, error)

	// Delete removes a key through Raft consensus.
	Delete(ctx context.Context, key string) error

	// List returns sorted keys matching the given prefix with a
	// linearizable read. An empty prefix returns all keys.
	List(ctx context.Context, prefix string) ([]string, error)

	// Scan returns all key-value pairs matching the given prefix
	// using a PrefixQuery for compatibility with the FastStateMachine.
	Scan(ctx context.Context, prefix string) (map[string][]byte, error)

	// GetStale retrieves a value by key from the local state machine
	// without Raft coordination. Returns potentially stale data but
	// with sub-millisecond latency. Returns KeyNotFoundError when the
	// key does not exist.
	GetStale(ctx context.Context, key string) ([]byte, error)

	// ListStale returns sorted keys matching the given prefix from the
	// local state machine without Raft coordination. Returns potentially
	// stale data but with sub-millisecond latency.
	ListStale(ctx context.Context, prefix string) ([]string, error)

	// ScanStale returns all key-value pairs matching the given prefix
	// from the local state machine without Raft coordination. Returns
	// potentially stale data but with sub-millisecond latency.
	ScanStale(ctx context.Context, prefix string) (map[string][]byte, error)

	// PutWithMode stores a key-value pair with the specified write
	// durability. Allows trading consistency for performance: Sync
	// (quorum), WALDurable (local WAL), or Batched (aggregated writes).
	PutWithMode(ctx context.Context, key string, value []byte, mode writemode.Mode) error

	// DeleteWithMode removes a key with the specified write durability.
	// Allows trading consistency for performance: Sync (quorum),
	// WALDurable (local WAL), or Batched (aggregated writes).
	DeleteWithMode(ctx context.Context, key string, mode writemode.Mode) error

	// GetWithConsistency retrieves a value by key with the specified
	// consistency level. Strong uses full Raft consensus, Linearizable
	// uses ReadIndex optimization, and Eventual reads from the local
	// state machine.
	GetWithConsistency(ctx context.Context, key string, opts consistency.ReadOptions) ([]byte, error)

	// ListWithConsistency returns sorted keys matching the prefix with
	// the specified consistency level. An empty prefix returns all keys.
	ListWithConsistency(ctx context.Context, prefix string, opts consistency.ReadOptions) ([]string, error)

	// ScanWithConsistency returns all key-value pairs matching the
	// prefix with the specified consistency level.
	ScanWithConsistency(ctx context.Context, prefix string, opts consistency.ReadOptions) (map[string][]byte, error)

	// ExistsWithConsistency checks whether a key exists with the
	// specified consistency level. Returns true when the key is present
	// and false when it is not found.
	ExistsWithConsistency(ctx context.Context, key string, opts consistency.ReadOptions) (bool, error)

	// PutEntity stores a typed entity with consistency and write mode options.
	PutEntity(ctx context.Context, req *PutEntityRequest) (*PutEntityResponse, error)

	// GetEntity retrieves a typed entity with consistency options.
	GetEntity(ctx context.Context, req *GetEntityRequest) (*GetEntityResponse, error)

	// DeleteEntity removes a typed entity with consistency options.
	DeleteEntity(ctx context.Context, req *DeleteEntityRequest) (*DeleteEntityResponse, error)

	// ListEntities returns a paginated list of entity keys for a given type.
	ListEntities(ctx context.Context, req *ListEntitiesRequest) (*ListEntitiesResponse, error)
}

// AdminServicer provides cluster control-plane operations for health
// monitoring, membership management, and operational tasks.
type AdminServicer interface {
	// Health returns the current health status of the host.
	Health(ctx context.Context) (*HealthStatus, error)

	// Status returns detailed status information about the host
	// and all loaded shards.
	Status(ctx context.Context) (*StatusInfo, error)

	// Peers returns the peer membership for all loaded shards.
	Peers(ctx context.Context) (*PeersInfo, error)

	// AddMember adds a new replica to the specified shard.
	AddMember(ctx context.Context, shardID, replicaID uint64, raftAddr string) error

	// RemoveMember removes a replica from the specified shard.
	RemoveMember(ctx context.Context, shardID, replicaID uint64) error

	// RequestSnapshot triggers an asynchronous snapshot for the
	// specified shard.
	RequestSnapshot(ctx context.Context, shardID uint64) error

	// Drain transitions the host into drain mode where new proposals
	// and reads are rejected while in-flight operations complete.
	Drain(ctx context.Context) error

	// Capabilities returns the compile-time feature set of the QRDB
	// binary, indicating which optional services are available.
	Capabilities(ctx context.Context) (*capabilities.Capabilities, error)

	// StartShard initializes and starts a Raft group for the given shard.
	// Members maps replicaID to address for initial cluster members.
	// Set Join to true when this node is joining an existing cluster.
	StartShard(ctx context.Context, req *StartShardRequest) error

	// StopShard stops the Raft group for the given shard.
	StopShard(ctx context.Context, shardID uint64) error

	// GetShardState returns the Raft state for a shard including
	// the configuration change index, term, and current leader.
	GetShardState(ctx context.Context, shardID uint64) (*ShardStateResponse, error)

	// GetTopology returns the full cluster topology as seen from
	// this node, including all loaded shards and their membership.
	GetTopology(ctx context.Context) (*TopologyResponse, error)
}

// HealthStatus describes the aggregate health of the host.
type HealthStatus struct {
	// Status is the health verdict: "ok", "degraded", "sealed", or "unhealthy".
	Status string

	// Leader is true when the local replica is the leader for any shard.
	Leader bool
}

// HealthStatusSealed is the canonical HealthStatus.Status verdict reported
// by the gRPC, native QUIC, and Unix (grpc-over-UDS) Health handlers while
// the daemon is in the pre-host sealed window -- i.e. before AdminServicer
// exists (see cmd/qrdbd/seal_subsystem.go's awaitUnseal and
// pkg/server.ServeSealedWindow). Health is on the sealed-window allowlist
// (pkg/api/sealedwindow.Table VerbHealth) on every transport, so those
// handlers report this verdict directly instead of erroring, letting SDK
// Connect() (which requires a successful Health RPC) treat a reachable,
// correctly-sealed daemon as connected rather than unreachable.
const HealthStatusSealed = "sealed"

// StatusInfo provides detailed host and shard status information.
type StatusInfo struct {
	// NodeID is the unique host identifier.
	NodeID string

	// RaftAddress is the host's Raft network address.
	RaftAddress string

	// IsLeader is true when the local replica is the leader for any shard.
	IsLeader bool

	// LeaderID is the leader's replica ID for the primary shard.
	LeaderID uint64

	// ShardCount is the number of shards loaded on this host.
	ShardCount int

	// IsDraining is true when the host is in drain mode.
	IsDraining bool

	// Shards contains per-shard status information.
	Shards []quicraft.ShardInfo
}

// PeerInfo describes a single peer in a shard.
type PeerInfo struct {
	// ReplicaID is the peer's replica identifier.
	ReplicaID uint64

	// Address is the peer's Raft network address.
	Address string

	// IsLeader is true when this peer is the current leader.
	IsLeader bool
}

// ShardPeers describes the membership of a single shard.
type ShardPeers struct {
	// ShardID is the shard's unique identifier.
	ShardID uint64

	// LeaderID is the current leader's replica ID. 0 if unknown.
	LeaderID uint64

	// Peers contains all known members of the shard.
	Peers []PeerInfo
}

// PeersInfo contains the peer membership for all loaded shards.
type PeersInfo struct {
	// Shards contains per-shard membership information.
	Shards []ShardPeers
}

// Host is the interface the service layer requires from QuicRaft. This
// narrow interface decouples the service from the concrete *quicraft.Host
// type, enabling clean testing and abstraction boundaries.
type Host interface {
	// ID returns the unique host identifier string.
	ID() string

	// RaftAddress returns the host's Raft network address.
	RaftAddress() string

	// ShardCount returns the number of shards loaded on this host.
	ShardCount() int

	// GetNodeHostInfo returns a snapshot of the host's state for
	// monitoring and debugging.
	GetNodeHostInfo(opt quicraft.NodeHostInfoOption) *quicraft.NodeHostInfo

	// HealthCheck returns the aggregate health status of the host.
	HealthCheck() *quicraft.HealthStatus

	// Drain transitions the host into drain mode.
	Drain() error

	// IsDraining returns true when the host is in drain mode.
	IsDraining() bool

	// SyncPropose proposes a command through Raft consensus and waits
	// for it to be applied.
	SyncPropose(ctx context.Context, shardID uint64, cmd []byte) (sm.Result, error)

	// SyncRead performs a linearizable read against the state machine.
	SyncRead(ctx context.Context, shardID uint64, query interface{}) (interface{}, error)

	// RequestAddNode requests adding a new replica to a shard.
	RequestAddNode(ctx context.Context, shardID, replicaID uint64, addr string, configChangeIndex uint64) (*quicraft.RequestState, error)

	// RequestRemoveNode requests removing a replica from a shard.
	RequestRemoveNode(ctx context.Context, shardID, replicaID uint64, configChangeIndex uint64) (*quicraft.RequestState, error)

	// RequestSnapshot requests an asynchronous snapshot for a shard.
	RequestSnapshot(ctx context.Context, shardID uint64) (*quicraft.RequestState, error)

	// ProposeWithMode proposes a write with specified durability guarantee.
	// Allows trading consistency for performance: Sync (quorum), WALDurable
	// (local WAL), or Batched (aggregated writes).
	ProposeWithMode(ctx context.Context, shardID uint64, cmd []byte, mode writemode.Mode) (sm.Result, error)

	// StaleRead reads from the local state machine without Raft coordination.
	// Returns potentially stale data but with sub-millisecond latency.
	StaleRead(ctx context.Context, shardID uint64, query interface{}) (interface{}, error)

	// ReadWithConsistency performs a read with the specified consistency level.
	// Strong: full Raft consensus. Linearizable: ReadIndex optimization.
	// Eventual: local stale read. FollowerRead: ReadIndex on any node.
	ReadWithConsistency(ctx context.Context, shardID uint64, query interface{}, opts consistency.ReadOptions) (interface{}, error)

	// FollowerRead reads from the local state machine after confirming the
	// node has applied up to the committed index via ReadIndex. Allows any
	// node (leader or follower) to serve reads without forwarding to the
	// leader, providing stronger guarantees than StaleRead with lower
	// latency than requiring the leader to execute the query itself.
	FollowerRead(ctx context.Context, shardID uint64, query interface{}) (interface{}, error)

	// GetLeaderID returns the current leader's replica ID for the shard
	// and whether a leader is known. Returns 0, false, error if the shard
	// does not exist.
	GetLeaderID(shardID uint64) (uint64, bool, error)

	// GetShardRaftState returns the Raft state for a shard: the config
	// change index and current term. Used for monitoring and consistency
	// checks.
	GetShardRaftState(shardID uint64) (configChangeIndex uint64, term uint64, err error)

	// StartShard initializes and starts a Raft group for the given shard.
	// members maps replicaID to address for initial cluster members. join
	// indicates whether this node is joining an existing cluster. create
	// is the state machine factory. cfg provides per-shard Raft
	// configuration including ShardID and ReplicaID.
	StartShard(members map[uint64]string, join bool, create sm.CreateFunc, cfg config.Config) error

	// StopShard stops the Raft group for the given shard.
	StopShard(shardID uint64) error

	// Close gracefully shuts down the host and all its Raft groups.
	Close() error
}

// KVServiceOption configures KVService at construction time.
type KVServiceOption func(*KVService)

// KVService implements KVServicer backed by a QuicRaft host. Values are
// stored and returned verbatim as opaque byte slices. Callers that require
// typed serialization (e.g., msgpack or JSON marshaling of structs) must
// encode/decode themselves before calling Put and after calling Get;
// KVService makes no assumptions about value encoding.
type KVService struct {
	host    Host
	shardID uint64
}

// NewKVService creates a new KVServicer that operates on the specified shard.
// Optional KVServiceOption values configure additional behavior at construction time.
func NewKVService(host Host, shardID uint64, opts ...KVServiceOption) KVServicer {
	svc := &KVService{
		host:    host,
		shardID: shardID,
	}
	for _, opt := range opts {
		opt(svc)
	}
	return svc
}

// Put stores a key-value pair through Raft consensus. The command is encoded
// using the KV wire protocol and proposed to the shard's Raft group. The
// value is stored verbatim; no codec transformation is applied.
func (s *KVService) Put(ctx context.Context, key string, value []byte) error {
	cmd, err := kv.EncodePut([]byte(key), value)
	if err != nil {
		return &EncodeRequestError{Err: err}
	}
	result, err := s.host.SyncPropose(ctx, s.shardID, cmd)
	if err != nil {
		return s.translateProposeError(err)
	}
	// Surface a state-machine-encoded application error (e.g. a unique-index
	// violation that rejected the write without stalling consensus).
	if resultErr := checkResultError(result); resultErr != nil {
		return resultErr
	}
	return nil
}

// PutIfAbsent stores value under key only when the key is absent, through Raft
// consensus.
//
// The condition is evaluated inside the state machine, at this entry's position
// in the committed log, so every replica decides identically from identical
// state. It deliberately does NOT read first and then propose a plain Put: that
// is two operations with a window between them, and two proposers racing would
// both observe "absent" and both write.
//
// A declined write is a successful application — the state machine reports
// kv.ResultNotStored rather than an error — so consensus is never stalled by
// losing the race.
func (s *KVService) PutIfAbsent(ctx context.Context, key string, value []byte) (bool, error) {
	cmd, err := kv.EncodePutIfAbsent([]byte(key), value)
	if err != nil {
		return false, &EncodeRequestError{Err: err}
	}
	result, err := s.host.SyncPropose(ctx, s.shardID, cmd)
	if err != nil {
		return false, s.translateProposeError(err)
	}
	if resultErr := checkResultError(result); resultErr != nil {
		return false, resultErr
	}
	return result.Value == kv.ResultStored, nil
}

// CompareAndDelete removes key only when its stored value still equals
// expected, with the comparison evaluated inside the state machine at this
// entry's position in the committed log — so every replica decides
// identically from identical state.
//
// It deliberately does NOT read first and then propose a plain Delete: that
// is two operations with a window between them, and in that window another
// caller can legitimately claim the key, so the delete would remove a value
// this caller never observed.
//
// A refused delete is a successful application — the state machine reports
// protocol.ResultNotDeleted rather than an error — so consensus is never
// stalled by losing the race.
func (s *KVService) CompareAndDelete(ctx context.Context, key string, expected []byte) (bool, error) {
	cmd, err := protocol.EncodeCompareAndDelete([]byte(key), expected)
	if err != nil {
		return false, &EncodeRequestError{Err: err}
	}
	result, err := s.host.SyncPropose(ctx, s.shardID, cmd)
	if err != nil {
		return false, s.translateProposeError(err)
	}
	if resultErr := checkResultError(result); resultErr != nil {
		return false, resultErr
	}
	return result.Value == protocol.ResultDeleted, nil
}

// Get retrieves a value by key with a linearizable read through the Raft
// consensus layer. Returns KeyNotFoundError when the key does not exist.
// The value is returned verbatim; no codec transformation is applied.
func (s *KVService) Get(ctx context.Context, key string) ([]byte, error) {
	result, err := s.host.SyncRead(ctx, s.shardID, []byte(key))
	if err != nil {
		return nil, s.translateReadError(err)
	}
	if result == nil {
		return nil, &ReadError{Err: &KeyNotFoundError{Key: key}}
	}

	data, ok := result.([]byte)
	if !ok {
		return nil, &ReadError{Err: &unexpectedResultTypeError{}}
	}

	return data, nil
}

// Delete removes a key through Raft consensus. The command is encoded
// using the KV wire protocol and proposed to the shard's Raft group.
func (s *KVService) Delete(ctx context.Context, key string) error {
	cmd, err := kv.EncodeDelete([]byte(key))
	if err != nil {
		return &EncodeRequestError{Err: err}
	}
	result, err := s.host.SyncPropose(ctx, s.shardID, cmd)
	if err != nil {
		return s.translateProposeError(err)
	}
	// The state machine encodes a not-configured subsystem into the RESULT
	// rather than returning it to Raft, so discarding the result reports a
	// missing subsystem as success (go-qrdb#62).
	if resultErr := checkResultError(result); resultErr != nil {
		return resultErr
	}
	return nil
}

// List returns sorted keys matching the given prefix with a linearizable
// read. An empty prefix returns all user keys: keys in the internal namespace
// (see IsInternalKey) are excluded from the results.
func (s *KVService) List(ctx context.Context, prefix string) ([]string, error) {
	result, err := s.host.SyncRead(ctx, s.shardID, kv.ListQuery{Prefix: prefix})
	if err != nil {
		return nil, s.translateReadError(err)
	}
	if result == nil {
		return nil, nil
	}

	keys, ok := result.([]string)
	if !ok {
		return nil, &ReadError{Err: &unexpectedResultTypeError{}}
	}

	return FilterInternalKeys(keys, prefix), nil
}

// Scan returns all key-value pairs matching the given prefix. Uses a
// PrefixQuery for compatibility with the basic FastStateMachine. Internal
// keys in the internal namespace are excluded from the results unless the
// prefix itself lies inside a namespace whose owning subsystem enumerates it.
func (s *KVService) Scan(ctx context.Context, prefix string) (map[string][]byte, error) {
	query := &statemachine.PrefixQuery{
		Prefix: []byte(prefix),
		Limit:  0,
	}
	result, err := s.host.SyncRead(ctx, s.shardID, query)
	if err != nil {
		return nil, s.translateReadError(err)
	}
	if result == nil {
		return nil, nil
	}
	data, ok := result.(map[string][]byte)
	if !ok {
		return nil, &ReadError{Err: &unexpectedResultTypeError{}}
	}
	return FilterInternalMap(data, prefix), nil
}

// GetStale retrieves a value by key from the local state machine without
// Raft coordination. Returns potentially stale data but with sub-millisecond
// latency. Returns KeyNotFoundError when the key does not exist. The value
// is returned verbatim; no codec transformation is applied.
func (s *KVService) GetStale(ctx context.Context, key string) ([]byte, error) {
	result, err := s.host.StaleRead(ctx, s.shardID, []byte(key))
	if err != nil {
		return nil, s.translateReadError(err)
	}
	if result == nil {
		return nil, &ReadError{Err: &KeyNotFoundError{Key: key}}
	}

	data, ok := result.([]byte)
	if !ok {
		return nil, &ReadError{Err: &unexpectedResultTypeError{}}
	}

	return data, nil
}

// ListStale returns sorted keys matching the given prefix from the local
// state machine without Raft coordination. Returns potentially stale data
// but with sub-millisecond latency.
func (s *KVService) ListStale(ctx context.Context, prefix string) ([]string, error) {
	result, err := s.host.StaleRead(ctx, s.shardID, kv.ListQuery{Prefix: prefix})
	if err != nil {
		return nil, s.translateReadError(err)
	}
	if result == nil {
		return nil, nil
	}

	keys, ok := result.([]string)
	if !ok {
		return nil, &ReadError{Err: &unexpectedResultTypeError{}}
	}

	return FilterInternalKeys(keys, prefix), nil
}

// ScanStale returns all key-value pairs matching the given prefix from the
// local state machine without Raft coordination. Returns potentially stale
// data but with sub-millisecond latency.
func (s *KVService) ScanStale(ctx context.Context, prefix string) (map[string][]byte, error) {
	query := &statemachine.PrefixQuery{
		Prefix: []byte(prefix),
		Limit:  0,
	}
	result, err := s.host.StaleRead(ctx, s.shardID, query)
	if err != nil {
		return nil, s.translateReadError(err)
	}
	if result == nil {
		return nil, nil
	}
	data, ok := result.(map[string][]byte)
	if !ok {
		return nil, &ReadError{Err: &unexpectedResultTypeError{}}
	}
	return FilterInternalMap(data, prefix), nil
}

// ---------------------------------------------------------------------------
// Write mode operations
// ---------------------------------------------------------------------------

// PutWithMode stores a key-value pair with the specified write durability.
// The command is encoded using the KV wire protocol and proposed to the
// shard's Raft group via ProposeWithMode. The value is stored verbatim;
// no codec transformation is applied.
func (s *KVService) PutWithMode(ctx context.Context, key string, value []byte, mode writemode.Mode) error {
	cmd, err := kv.EncodePut([]byte(key), value)
	if err != nil {
		return &EncodeRequestError{Err: err}
	}
	result, err := s.host.ProposeWithMode(ctx, s.shardID, cmd, mode)
	if err != nil {
		return s.translateProposeError(err)
	}
	// The state machine encodes a not-configured subsystem into the RESULT
	// rather than returning it to Raft, so discarding the result reports a
	// missing subsystem as success (go-qrdb#62).
	if resultErr := checkResultError(result); resultErr != nil {
		return resultErr
	}
	return nil
}

// DeleteWithMode removes a key with the specified write durability. The
// command is encoded using the KV wire protocol and proposed to the shard's
// Raft group via ProposeWithMode.
func (s *KVService) DeleteWithMode(ctx context.Context, key string, mode writemode.Mode) error {
	cmd, err := kv.EncodeDelete([]byte(key))
	if err != nil {
		return &EncodeRequestError{Err: err}
	}
	result, err := s.host.ProposeWithMode(ctx, s.shardID, cmd, mode)
	if err != nil {
		return s.translateProposeError(err)
	}
	// The state machine encodes a not-configured subsystem into the RESULT
	// rather than returning it to Raft, so discarding the result reports a
	// missing subsystem as success (go-qrdb#62).
	if resultErr := checkResultError(result); resultErr != nil {
		return resultErr
	}
	return nil
}

// ---------------------------------------------------------------------------
// Consistency-level read operations
// ---------------------------------------------------------------------------

// GetWithConsistency retrieves a value by key with the specified consistency
// level. Returns KeyNotFoundError when the key does not exist. The value is
// returned verbatim; no codec transformation is applied.
func (s *KVService) GetWithConsistency(ctx context.Context, key string, opts consistency.ReadOptions) ([]byte, error) {
	result, err := s.host.ReadWithConsistency(ctx, s.shardID, []byte(key), opts)
	if err != nil {
		return nil, s.translateReadError(err)
	}
	if result == nil {
		return nil, &ReadError{Err: &KeyNotFoundError{Key: key}}
	}

	data, ok := result.([]byte)
	if !ok {
		return nil, &ReadError{Err: &unexpectedResultTypeError{}}
	}

	return data, nil
}

// ListWithConsistency returns sorted keys matching the given prefix with
// the specified consistency level. An empty prefix returns all user keys.
// Keys in the internal namespace (see IsInternalKey) are excluded from the
// results.
func (s *KVService) ListWithConsistency(ctx context.Context, prefix string, opts consistency.ReadOptions) ([]string, error) {
	result, err := s.host.ReadWithConsistency(ctx, s.shardID, kv.ListQuery{Prefix: prefix}, opts)
	if err != nil {
		return nil, s.translateReadError(err)
	}
	if result == nil {
		return nil, nil
	}

	keys, ok := result.([]string)
	if !ok {
		return nil, &ReadError{Err: &unexpectedResultTypeError{}}
	}

	return FilterInternalKeys(keys, prefix), nil
}

// ScanWithConsistency returns all key-value pairs matching the given prefix
// with the specified consistency level. Keys in the internal namespace are
// excluded from the results unless the prefix itself lies inside a namespace
// whose owning subsystem enumerates it.
func (s *KVService) ScanWithConsistency(ctx context.Context, prefix string, opts consistency.ReadOptions) (map[string][]byte, error) {
	query := &statemachine.PrefixQuery{
		Prefix: []byte(prefix),
		Limit:  0,
	}
	result, err := s.host.ReadWithConsistency(ctx, s.shardID, query, opts)
	if err != nil {
		return nil, s.translateReadError(err)
	}
	if result == nil {
		return nil, nil
	}
	data, ok := result.(map[string][]byte)
	if !ok {
		return nil, &ReadError{Err: &unexpectedResultTypeError{}}
	}
	return FilterInternalMap(data, prefix), nil
}

// ExistsWithConsistency checks whether a key exists with the specified
// consistency level. Returns true when the key is present and false when
// it is not found. Other errors are propagated to the caller.
func (s *KVService) ExistsWithConsistency(ctx context.Context, key string, opts consistency.ReadOptions) (bool, error) {
	result, err := s.host.ReadWithConsistency(ctx, s.shardID, []byte(key), opts)
	if err != nil {
		var knf *KeyNotFoundError
		if errors.As(err, &knf) {
			return false, nil
		}
		// Check for the upstream kv.KeyNotFoundError as well.
		var kvKnf *kv.KeyNotFoundError
		if errors.As(err, &kvKnf) {
			return false, nil
		}
		return false, s.translateReadError(err)
	}
	return result != nil, nil
}

// ---------------------------------------------------------------------------
// Entity operations
// ---------------------------------------------------------------------------

// PutEntity stores a typed entity through Raft consensus. The entity key is
// constructed as "{entityType}/{id}" and the data is stored via the basic
// Put operation. WriteMode and Consistency are captured for future phases.
func (s *KVService) PutEntity(ctx context.Context, req *PutEntityRequest) (*PutEntityResponse, error) {
	if err := validateEntityWrite(req.EntityType, req.ID, req.Data); err != nil {
		return nil, err
	}

	key := entityKey(req.EntityType, req.ID)
	if err := s.Put(ctx, key, req.Data); err != nil {
		return nil, err
	}

	now := time.Now().UnixNano()
	return &PutEntityResponse{
		Version:   1,
		Revision:  1,
		Timestamp: now,
	}, nil
}

// GetEntity retrieves a typed entity with a linearizable read. The entity
// key is constructed as "{entityType}/{id}" and looked up via the basic
// Get operation. Returns KeyNotFoundError when the entity does not exist.
func (s *KVService) GetEntity(ctx context.Context, req *GetEntityRequest) (*GetEntityResponse, error) {
	if req.EntityType == "" {
		return nil, &EntityEmptyTypeError{}
	}
	if req.ID == "" {
		return nil, &EntityEmptyIDError{EntityType: req.EntityType}
	}

	key := entityKey(req.EntityType, req.ID)
	data, err := s.Get(ctx, key)
	if err != nil {
		return nil, err
	}

	now := time.Now().UnixNano()
	return &GetEntityResponse{
		Data:      data,
		Version:   1,
		Revision:  1,
		Timestamp: now,
	}, nil
}

// DeleteEntity removes a typed entity through Raft consensus. The entity
// key is constructed as "{entityType}/{id}" and removed via the basic
// Delete operation.
func (s *KVService) DeleteEntity(ctx context.Context, req *DeleteEntityRequest) (*DeleteEntityResponse, error) {
	if req.EntityType == "" {
		return nil, &EntityEmptyTypeError{}
	}
	if req.ID == "" {
		return nil, &EntityEmptyIDError{EntityType: req.EntityType}
	}

	key := entityKey(req.EntityType, req.ID)
	if err := s.Delete(ctx, key); err != nil {
		return nil, err
	}

	return &DeleteEntityResponse{
		Deleted:  true,
		Revision: 1,
	}, nil
}

// ListEntities returns a paginated list of entity keys for a given type.
// The entity type prefix is constructed as "{entityType}/" and listed via
// the basic List operation. Results are paginated using page token and
// page size. The page token is the last key from the previous page encoded
// as an offset index.
func (s *KVService) ListEntities(ctx context.Context, req *ListEntitiesRequest) (*ListEntitiesResponse, error) {
	if req.EntityType == "" {
		return nil, &EntityEmptyTypeError{}
	}

	prefix := entityPrefix(req.EntityType)
	if req.Prefix != "" {
		prefix += req.Prefix
	}

	allKeys, err := s.List(ctx, prefix)
	if err != nil {
		return nil, err
	}

	// Strip the entity type prefix from returned keys so callers
	// receive entity IDs rather than full storage keys.
	typePrefix := entityPrefix(req.EntityType)
	stripped := make([]string, 0, len(allKeys))
	for _, k := range allKeys {
		stripped = append(stripped, strings.TrimPrefix(k, typePrefix))
	}

	totalCount := int64(len(stripped))

	// Apply page token (offset-based).
	startIdx := 0
	if req.PageToken != "" {
		offset, parseErr := strconv.Atoi(req.PageToken)
		if parseErr != nil {
			return nil, &InvalidPageTokenError{Token: req.PageToken}
		}
		startIdx = offset
	}

	if startIdx > len(stripped) {
		startIdx = len(stripped)
	}

	remaining := stripped[startIdx:]

	// Apply page size.
	pageSize := int(req.PageSize)
	if pageSize <= 0 {
		pageSize = len(remaining)
	}

	var page []string
	var nextPageToken string
	if pageSize >= len(remaining) {
		page = remaining
	} else {
		page = remaining[:pageSize]
		nextPageToken = strconv.Itoa(startIdx + pageSize)
	}

	return &ListEntitiesResponse{
		Keys:          page,
		NextPageToken: nextPageToken,
		TotalCount:    totalCount,
	}, nil
}

// validateEntityWrite checks that the entity type, ID, and data are all
// non-empty. Returns a typed error if any validation fails.
func validateEntityWrite(entityType EntityType, id string, data []byte) error {
	if entityType == "" {
		return &EntityEmptyTypeError{}
	}
	if id == "" {
		return &EntityEmptyIDError{EntityType: entityType}
	}
	if len(data) == 0 {
		return &EntityEmptyDataError{EntityType: entityType, ID: id}
	}
	return nil
}

// ---------------------------------------------------------------------------
// Error translation
// ---------------------------------------------------------------------------

// translateProposeError converts QuicRaft proposal errors to service-level
// typed errors. NotLeaderError is mapped to service.NotLeaderError so
// callers can inspect leader routing information.
func (s *KVService) translateProposeError(err error) error {
	var nle *quicraft.NotLeaderError
	if errors.As(err, &nle) {
		return &NotLeaderError{
			ShardID:       nle.ShardID,
			LeaderID:      nle.LeaderID,
			LeaderAddress: nle.LeaderAddress,
		}
	}
	return &ProposeError{Err: err}
}

// translateReadError converts QuicRaft read errors to service-level typed
// errors. KeyNotFoundError and NotLeaderError are mapped to their service
// equivalents.
func (s *KVService) translateReadError(err error) error {
	var knf *kv.KeyNotFoundError
	if errors.As(err, &knf) {
		return &KeyNotFoundError{Key: knf.Key}
	}

	var nle *quicraft.NotLeaderError
	if errors.As(err, &nle) {
		return &NotLeaderError{
			ShardID:       nle.ShardID,
			LeaderID:      nle.LeaderID,
			LeaderAddress: nle.LeaderAddress,
		}
	}

	return &ReadError{Err: err}
}

// unexpectedResultTypeError is returned when SyncRead returns a result
// that does not match the expected type.
type unexpectedResultTypeError struct{}

// Error returns a human-readable description.
func (e *unexpectedResultTypeError) Error() string {
	return "unexpected result type from state machine"
}

// AdminServiceOption configures optional AdminService behavior.
type AdminServiceOption func(*AdminService)

// WithStateMachineFactory sets the state machine factory used by StartShard
// to create state machines for new shards.
func WithStateMachineFactory(factory sm.CreateFunc) AdminServiceOption {
	return func(s *AdminService) {
		s.smFactory = factory
	}
}

// WithDefaultShardConfig sets the base Raft configuration used when starting
// new shards. ShardID and ReplicaID are overridden per request from the
// StartShardRequest fields.
// WithCapabilityProfile pins this AdminService instance to an explicit
// effective capability profile (build tags x run mode x config) instead of
// the process-global sealed profile. Composition roots that can host more
// than one node per process (the SDK) use this so each node reports its
// own truth.
func WithCapabilityProfile(profile capabilities.Capabilities) AdminServiceOption {
	return func(s *AdminService) { s.profile = &profile }
}

func WithDefaultShardConfig(cfg config.Config) AdminServiceOption {
	return func(s *AdminService) {
		s.defaultCfg = cfg
	}
}

// AdminService implements AdminServicer backed by a QuicRaft host.
type AdminService struct {
	host       Host
	smFactory  sm.CreateFunc
	defaultCfg config.Config
	profile    *capabilities.Capabilities
}

// NewAdminService creates a new AdminServicer backed by the given host.
// Optional AdminServiceOption values configure state machine factories
// and other behavior.
func NewAdminService(host Host, opts ...AdminServiceOption) AdminServicer {
	svc := &AdminService{host: host}
	for _, opt := range opts {
		opt(svc)
	}
	return svc
}

// Health returns the current health status of the host. The Status field
// maps directly from the QuicRaft HealthCheck. The Leader field is derived
// from shard information.
func (s *AdminService) Health(_ context.Context) (*HealthStatus, error) {
	hs := s.host.HealthCheck()

	isLeader := false
	for _, si := range hs.Shards {
		if si.IsLeader {
			isLeader = true
			break
		}
	}

	return &HealthStatus{
		Status: hs.Status,
		Leader: isLeader,
	}, nil
}

// Status returns detailed status information about the host and all loaded
// shards. This includes the host identity, Raft address, leadership state,
// drain state, and per-shard information.
func (s *AdminService) Status(_ context.Context) (*StatusInfo, error) {
	info := s.host.GetNodeHostInfo(quicraft.NodeHostInfoOption{SkipLogInfo: true})

	isLeader := false
	var leaderID uint64
	for _, si := range info.ShardInfoList {
		if si.IsLeader {
			isLeader = true
			leaderID = si.ReplicaID
			break
		}
	}

	return &StatusInfo{
		NodeID:      s.host.ID(),
		RaftAddress: info.RaftAddress,
		IsLeader:    isLeader,
		LeaderID:    leaderID,
		ShardCount:  s.host.ShardCount(),
		IsDraining:  s.host.IsDraining(),
		Shards:      info.ShardInfoList,
	}, nil
}

// Peers returns the peer membership for all loaded shards. Each shard's
// membership includes all known replicas, their addresses, and which
// replica is the current leader.
func (s *AdminService) Peers(_ context.Context) (*PeersInfo, error) {
	info := s.host.GetNodeHostInfo(quicraft.NodeHostInfoOption{SkipLogInfo: true})

	shards := make([]ShardPeers, 0, len(info.ShardInfoList))
	for _, si := range info.ShardInfoList {
		peers := make([]PeerInfo, 0, len(si.Nodes))
		for replicaID, addr := range si.Nodes {
			peers = append(peers, PeerInfo{
				ReplicaID: replicaID,
				Address:   addr,
				IsLeader:  replicaID == si.LeaderID,
			})
		}
		shards = append(shards, ShardPeers{
			ShardID:  si.ShardID,
			LeaderID: si.LeaderID,
			Peers:    peers,
		})
	}

	return &PeersInfo{Shards: shards}, nil
}

// AddMember adds a new replica to the specified shard. The request is
// proposed through Raft and completes when the membership change is
// committed.
func (s *AdminService) AddMember(ctx context.Context, shardID, replicaID uint64, raftAddr string) error {
	rs, err := s.host.RequestAddNode(ctx, shardID, replicaID, raftAddr, 0)
	if err != nil {
		return s.translateMembershipError("add", err)
	}

	if _, err := rs.Result(); err != nil {
		return s.translateMembershipError("add", err)
	}

	return nil
}

// RemoveMember removes a replica from the specified shard. The request is
// proposed through Raft and completes when the membership change is
// committed.
func (s *AdminService) RemoveMember(ctx context.Context, shardID, replicaID uint64) error {
	rs, err := s.host.RequestRemoveNode(ctx, shardID, replicaID, 0)
	if err != nil {
		return s.translateMembershipError("remove", err)
	}

	if _, err := rs.Result(); err != nil {
		return s.translateMembershipError("remove", err)
	}

	return nil
}

// translateMembershipError converts QuicRaft membership-change errors to
// service-level typed errors. NotLeaderError is mapped to service.NotLeaderError
// so callers (in particular the REST/gRPC leader-forwarding paths) can
// redirect the request to the current leader instead of surfacing a generic
// failure. All other errors are wrapped as MembershipError.
func (s *AdminService) translateMembershipError(op string, err error) error {
	var nle *quicraft.NotLeaderError
	if errors.As(err, &nle) {
		return &NotLeaderError{
			ShardID:       nle.ShardID,
			LeaderID:      nle.LeaderID,
			LeaderAddress: nle.LeaderAddress,
		}
	}
	return &MembershipError{Op: op, Err: err}
}

// RequestSnapshot triggers an asynchronous snapshot for the specified shard.
// The request completes when the snapshot is initiated, not when it finishes.
func (s *AdminService) RequestSnapshot(ctx context.Context, shardID uint64) error {
	rs, err := s.host.RequestSnapshot(ctx, shardID)
	if err != nil {
		return &SnapshotRequestError{Err: err}
	}

	if _, err := rs.Result(); err != nil {
		return &SnapshotRequestError{Err: err}
	}

	return nil
}

// Drain transitions the host into drain mode where new proposals and reads
// are rejected while in-flight operations complete. This is used for
// graceful shutdown.
func (s *AdminService) Drain(_ context.Context) error {
	if err := s.host.Drain(); err != nil {
		return &DrainError{Err: err}
	}
	return nil
}

// Capabilities returns the effective feature set of this node: the
// per-instance profile when one was pinned (WithCapabilityProfile), else
// the process-global view from the capabilities package (the sealed
// profile after a composition root seals, or the compile-time fallback).
func (s *AdminService) Capabilities(_ context.Context) (*capabilities.Capabilities, error) {
	if s.profile != nil {
		caps := *s.profile
		return &caps, nil
	}
	caps := capabilities.Get()
	return &caps, nil
}

// StartShard initializes and starts a Raft group for the given shard.
// A state machine factory must be registered via WithStateMachineFactory
// before calling this method.
func (s *AdminService) StartShard(_ context.Context, req *StartShardRequest) error {
	if s.smFactory == nil {
		return &NoStateMachineFactoryError{}
	}
	cfg := s.defaultCfg
	cfg.ShardID = req.ShardID
	cfg.ReplicaID = req.ReplicaID

	if err := s.host.StartShard(req.Members, req.Join, s.smFactory, cfg); err != nil {
		return &ShardStartError{ShardID: req.ShardID, Err: err}
	}
	return nil
}

// StopShard stops the Raft group for the given shard.
func (s *AdminService) StopShard(_ context.Context, shardID uint64) error {
	if err := s.host.StopShard(shardID); err != nil {
		return &ShardStopError{ShardID: shardID, Err: err}
	}
	return nil
}

// GetShardState returns the Raft state for a shard including the
// configuration change index, term, and current leader.
func (s *AdminService) GetShardState(_ context.Context, shardID uint64) (*ShardStateResponse, error) {
	configChangeIndex, term, err := s.host.GetShardRaftState(shardID)
	if err != nil {
		return nil, &ShardStateError{ShardID: shardID, Err: err}
	}
	leaderID, hasLeader, err := s.host.GetLeaderID(shardID)
	if err != nil {
		return nil, &ShardStateError{ShardID: shardID, Err: err}
	}
	return &ShardStateResponse{
		ShardID:           shardID,
		ConfigChangeIndex: configChangeIndex,
		Term:              term,
		LeaderID:          leaderID,
		HasLeader:         hasLeader,
	}, nil
}

// GetTopology returns the full cluster topology as seen from this node,
// including all loaded shards and their membership.
func (s *AdminService) GetTopology(_ context.Context) (*TopologyResponse, error) {
	info := s.host.GetNodeHostInfo(quicraft.NodeHostInfoOption{SkipLogInfo: true})
	shards := make([]TopologyShardInfo, 0, len(info.ShardInfoList))
	for _, si := range info.ShardInfoList {
		shards = append(shards, TopologyShardInfo{
			ShardID:   si.ShardID,
			ReplicaID: si.ReplicaID,
			IsLeader:  si.IsLeader,
			LeaderID:  si.LeaderID,
			Nodes:     si.Nodes,
		})
	}
	return &TopologyResponse{
		NodeID:      s.host.ID(),
		RaftAddress: s.host.RaftAddress(),
		ShardCount:  s.host.ShardCount(),
		Shards:      shards,
	}, nil
}

// The internal-key predicate and the two filters built on it live in
// internal_keys.go, which is the canonical declaration of the reserved
// namespace for every layer.
