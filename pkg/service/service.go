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

	quicraft "github.com/jeremyhahn/go-quicraft/pkg"
	"github.com/jeremyhahn/go-quicraft/pkg/sm"
	"github.com/jeremyhahn/go-quicraft/pkg/sm/kv"
)

// KVServicer provides KV data-plane operations with linearizable reads
// and writes through the Raft consensus layer.
type KVServicer interface {
	// Put stores a key-value pair through Raft consensus.
	Put(ctx context.Context, key string, value []byte) error

	// Get retrieves a value by key with a linearizable read.
	Get(ctx context.Context, key string) ([]byte, error)

	// Delete removes a key through Raft consensus.
	Delete(ctx context.Context, key string) error

	// List returns sorted keys matching the given prefix with a
	// linearizable read. An empty prefix returns all keys.
	List(ctx context.Context, prefix string) ([]string, error)
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
}

// HealthStatus describes the aggregate health of the host.
type HealthStatus struct {
	// Status is the health verdict: "ok", "degraded", "sealed", or "unhealthy".
	Status string

	// Leader is true when the local replica is the leader for any shard.
	Leader bool
}

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
	RequestAddNode(ctx context.Context, shardID, replicaID uint64, addr string) (*quicraft.RequestState, error)

	// RequestRemoveNode requests removing a replica from a shard.
	RequestRemoveNode(ctx context.Context, shardID, replicaID uint64) (*quicraft.RequestState, error)

	// RequestSnapshot requests an asynchronous snapshot for a shard.
	RequestSnapshot(ctx context.Context, shardID uint64) (*quicraft.RequestState, error)
}

// KVService implements KVServicer backed by a QuicRaft host.
type KVService struct {
	host    Host
	shardID uint64
}

// NewKVService creates a new KVServicer that operates on the specified shard.
func NewKVService(host Host, shardID uint64) KVServicer {
	return &KVService{
		host:    host,
		shardID: shardID,
	}
}

// Put stores a key-value pair through Raft consensus. The command is encoded
// using the KV wire protocol and proposed to the shard's Raft group.
func (s *KVService) Put(ctx context.Context, key string, value []byte) error {
	cmd := kv.EncodePut([]byte(key), value)
	_, err := s.host.SyncPropose(ctx, s.shardID, cmd)
	if err != nil {
		return s.translateProposeError(err)
	}
	return nil
}

// Get retrieves a value by key with a linearizable read through the Raft
// consensus layer. Returns KeyNotFoundError when the key does not exist.
func (s *KVService) Get(ctx context.Context, key string) ([]byte, error) {
	result, err := s.host.SyncRead(ctx, s.shardID, []byte(key))
	if err != nil {
		return nil, s.translateReadError(err)
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
	cmd := kv.EncodeDelete([]byte(key))
	_, err := s.host.SyncPropose(ctx, s.shardID, cmd)
	if err != nil {
		return s.translateProposeError(err)
	}
	return nil
}

// List returns sorted keys matching the given prefix with a linearizable
// read. An empty prefix returns all user keys.
func (s *KVService) List(ctx context.Context, prefix string) ([]string, error) {
	result, err := s.host.SyncRead(ctx, s.shardID, kv.ListQuery{Prefix: prefix})
	if err != nil {
		return nil, s.translateReadError(err)
	}

	keys, ok := result.([]string)
	if !ok {
		return nil, &ReadError{Err: &unexpectedResultTypeError{}}
	}

	return keys, nil
}

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

// AdminService implements AdminServicer backed by a QuicRaft host.
type AdminService struct {
	host Host
}

// NewAdminService creates a new AdminServicer backed by the given host.
func NewAdminService(host Host) AdminServicer {
	return &AdminService{host: host}
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
	rs, err := s.host.RequestAddNode(ctx, shardID, replicaID, raftAddr)
	if err != nil {
		return &MembershipError{Op: "add", Err: err}
	}

	if _, err := rs.Result(); err != nil {
		return &MembershipError{Op: "add", Err: err}
	}

	return nil
}

// RemoveMember removes a replica from the specified shard. The request is
// proposed through Raft and completes when the membership change is
// committed.
func (s *AdminService) RemoveMember(ctx context.Context, shardID, replicaID uint64) error {
	rs, err := s.host.RequestRemoveNode(ctx, shardID, replicaID)
	if err != nil {
		return &MembershipError{Op: "remove", Err: err}
	}

	if _, err := rs.Result(); err != nil {
		return &MembershipError{Op: "remove", Err: err}
	}

	return nil
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
