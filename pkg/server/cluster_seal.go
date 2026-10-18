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

package server

import (
	"context"
	"errors"
	"log/slog"
	"slices"
	"strconv"

	quicraft "github.com/jeremyhahn/go-quicraft/pkg"

	"github.com/jeremyhahn/go-qrdb/pkg/api/sealedwindow"
	"github.com/jeremyhahn/go-qrdb/pkg/cluster/featuregate"
	"github.com/jeremyhahn/go-qrdb/pkg/clusterseal"
	"github.com/jeremyhahn/go-qrdb/pkg/config"
	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
	"github.com/jeremyhahn/go-qrdb/pkg/watch"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// This file is the server's half of cluster sealing
// (docs/barrier-seal/architecture.md, "Sealing in a cluster"): the node gate,
// the tenant and database gates, the streams a seal ends, and the Enforcer a
// clusterseal.Monitor seals this node through.

// ClusterSeal is what the server needs from cluster sealing.
// *clusterseal.Deferred satisfies it.
type ClusterSeal interface {
	clusterseal.Sealer
	// Refusing reports whether this node must refuse every request outside
	// the sealed-window allowlist: held, cut off, or not yet confirmed.
	Refusing() bool
	// Streams is the registry every long-lived stream subscribes to.
	Streams() *clusterseal.Streams
}

// WithClusterSeal makes the server enforce cluster seals: the regular
// listener's sealed-window gate also closes while cs is refusing, a tenant or
// database whose record is sealed is refused at request resolution, and every
// watch and etcd stream ends when its barrier is sealed or this node is cut
// off.
func WithClusterSeal(cs ClusterSeal) ServerOption {
	return func(o *serverOptions) { o.clusterSeal = cs }
}

// clusterSealStreams returns the stream registry seals end streams through,
// or nil when the server enforces no cluster seals.
func (s *Server) clusterSealStreams() *clusterseal.Streams {
	if s.opts.clusterSeal == nil {
		return nil
	}
	return s.opts.clusterSeal.Streams()
}

// nodeSealProbe is the regular listener's sealed-window probe under cluster
// sealing: closed while the node barrier is sealed, and while the node is
// held, cut off or not yet confirmed.
type nodeSealProbe struct {
	barrier SealedWindowProbe
	seal    ClusterSeal
}

// IsSealed implements SealedWindowProbe.
func (p nodeSealProbe) IsSealed() bool {
	return p.barrier.IsSealed() || p.seal.Refusing()
}

// SealState implements sealedwindow.StateProbe, so every transport refuses a
// node that cannot confirm the cluster seal state as SEAL_STATE_UNCONFIRMED
// rather than as sealed: a sealed node barrier, or a cluster seal holding
// the node, is sealed; a node cut off from the seal records, or not yet
// confirmed, is unconfirmed.
func (p nodeSealProbe) SealState() (sealed, unconfirmed bool) {
	if p.barrier.IsSealed() {
		return true, false
	}
	err := clusterSealNodeRefusal(p.seal)
	_, unconfirmed = errors.AsType[*clusterseal.SealStateUnconfirmedError](err)
	return err != nil, unconfirmed
}

var _ sealedwindow.StateProbe = nodeSealProbe{}

// NodeSealProbe returns the probe every request-serving surface of a node
// gates on: the regular listener here, and the in-process embedded transport
// (embedded.WithSealProbe). It is closed while barrier is sealed and, when cs
// is not nil, while the node is held, cut off or not yet confirmed.
func NodeSealProbe(barrier SealedWindowProbe, cs ClusterSeal) SealedWindowProbe {
	if cs == nil {
		return barrier
	}
	return nodeSealProbe{barrier: barrier, seal: cs}
}

// nodeRefusal returns why this node refuses every request outside the
// sealed-window allowlist, or nil while it serves. A cluster seal that holds
// the node, or a sealed node barrier, is *clusterseal.SealedError for the node
// barrier; a node cut off from the seal records, or not yet confirmed, is
// *clusterseal.SealStateUnconfirmedError. Listeners with no sealed-window
// middleware of their own -- the per-database etcd listener -- refuse with it.
func (s *Server) nodeRefusal() error {
	if sealed, unconfirmed := sealedwindow.Sample(s.opts.sealedWindowProbe); sealed && !unconfirmed {
		return &clusterseal.SealedError{Barrier: clusterseal.NodeBarrier()}
	}
	return clusterSealNodeRefusal(s.opts.clusterSeal)
}

// clusterSealNodeRefusal returns why cs refuses this node, or nil while it
// serves or cs is nil: a node a cluster seal holds, or whose node barrier is
// sealed here, is *clusterseal.SealedError for the node barrier; one cut off
// from the seal records, or not yet confirmed, is
// *clusterseal.SealStateUnconfirmedError.
func clusterSealNodeRefusal(cs ClusterSeal) error {
	if cs == nil || !cs.Refusing() {
		return nil
	}
	if sealedLocal[cs.Status(clusterseal.NodeBarrier()).Local] {
		return &clusterseal.SealedError{Barrier: clusterseal.NodeBarrier()}
	}
	return &clusterseal.SealStateUnconfirmedError{}
}

// sealedLocal names the node barrier's local states in which a refusing
// cluster seal has confirmed the node sealed. In every other -- cut off,
// unconfirmed, or not yet known -- the node cannot confirm the seal records,
// and its refusal must not say sealed.
var sealedLocal = map[clusterseal.Local]bool{
	clusterseal.LocalHeld:   true,
	clusterseal.LocalSealed: true,
}

// applyClusterSeal wraps the regular listener's probe once options are
// applied. The boot-time sealed-window listener keeps the bare barrier: it
// runs before the host, with no seal records to read.
func (o *serverOptions) applyClusterSeal() {
	if o.clusterSeal == nil || o.sealedWindowProbe == nil || o.sealedWindowListener {
		return
	}
	o.sealedWindowProbe = NodeSealProbe(o.sealedWindowProbe, o.clusterSeal)
}

// clusterSealKV adapts the system shard's KVServicer to clusterseal.KV.
// clusterSealTransient are the Raft errors of a system shard electing a
// leader or moving it: a request that timed out or was dropped, aborted by
// the leadership change, refused by a replica that is not, or not yet, the
// leader, or rate-limited while a backlog drains.
var clusterSealTransient = []error{
	quicraft.ErrTimeout,
	quicraft.ErrDropped,
	quicraft.ErrAborted,
	quicraft.ErrBusy,
	quicraft.ErrNotLeader,
	quicraft.ErrShardNotReady,
	quicraft.ErrReadIndexNotReady,
}

// ClusterSealRetryable is the clusterseal.Config Retryable of a raft node: it
// reports whether err, from the system store under a seal record, is the
// system shard electing a leader or its leader moving, which a seal or clear
// waits out. Anything else -- a lost race, a sealed or unconfigured store, a
// refused permission, a cancelled request -- is final.
//
// Unlike SystemShardSettling, a not-leader answer naming a leader is
// transient too: leadership moved under the request, and the next attempt
// reaches the new leader.
func ClusterSealRetryable(err error) bool {
	for _, transient := range clusterSealTransient {
		if errors.Is(err, transient) {
			return true
		}
	}
	var nl *service.NotLeaderError
	var tnl *transport.NotLeaderError
	return errors.As(err, &nl) || errors.As(err, &tnl)
}

type clusterSealKV struct {
	kv service.KVServicer
}

var _ clusterseal.KV = clusterSealKV{}

// NewClusterSealKV adapts kv, the replicated system store, for a
// clusterseal.Monitor.
func NewClusterSealKV(kv service.KVServicer) clusterseal.KV {
	return clusterSealKV{kv: kv}
}

// Get implements clusterseal.KV, reporting a missing key as not found.
func (a clusterSealKV) Get(ctx context.Context, key string) ([]byte, bool, error) {
	v, err := a.kv.Get(ctx, key)
	var nf *service.KeyNotFoundError
	if errors.As(err, &nf) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return v, true, nil
}

// PutIfAbsent implements clusterseal.KV.
func (a clusterSealKV) PutIfAbsent(ctx context.Context, key string, value []byte) (bool, error) {
	return a.kv.PutIfAbsent(ctx, key, value)
}

// CompareAndSwap implements clusterseal.KV.
func (a clusterSealKV) CompareAndSwap(ctx context.Context, key string, expected, next []byte) (bool, error) {
	return a.kv.CompareAndSwap(ctx, key, expected, next)
}

// Put implements clusterseal.KV.
func (a clusterSealKV) Put(ctx context.Context, key string, value []byte) error {
	return a.kv.Put(ctx, key, value)
}

// Scan implements clusterseal.KV. "__sys:" is an owner-enumerated namespace,
// so the scan is not filtered.
func (a clusterSealKV) Scan(ctx context.Context, prefix string) (map[string][]byte, error) {
	return a.kv.Scan(ctx, prefix)
}

// ScanStale implements clusterseal.KV from this node's local state machine,
// unfiltered for the same reason as Scan.
func (a clusterSealKV) ScanStale(ctx context.Context, prefix string) (map[string][]byte, error) {
	return a.kv.ScanStale(ctx, prefix)
}

// SystemShardMembers lists the system shard's members by replica ID -- the
// identity a clusterseal.Monitor acknowledges a node-barrier seal under --
// sorted. Voters, observers and witnesses are all nodes that must read a seal
// before any of them wipes its key.
func SystemShardMembers(h featuregate.ShardMembershipReader, systemShard uint64) func(context.Context) ([]string, error) {
	src := featuregate.HostMembership(h)
	return func(context.Context) ([]string, error) {
		m, err := src.GroupMembership(systemShard)
		if err != nil {
			return nil, err
		}
		ids := make([]string, 0, len(m.Replicas)+len(m.Witnesses))
		for id := range m.Replicas {
			ids = append(ids, strconv.FormatUint(id, 10))
		}
		for id := range m.Witnesses {
			ids = append(ids, strconv.FormatUint(id, 10))
		}
		slices.Sort(ids)
		return ids, nil
	}
}

// ClusterSealParts is what a composer hands NewClusterSealMonitor. Gate,
// Shards, Hosts and Databases may be nil.
type ClusterSealParts struct {
	// KV is the replicated system store.
	KV service.KVServicer
	// Gate reports cluster-seal activation; nil means never active (a
	// standalone node, or a raft node with no transport).
	Gate *featuregate.Gate
	// Barriers holds this node's node, tenant and database barriers.
	Barriers *seal.BarrierRegistry
	// Shards stops a tenant's dedicated raft shards.
	Shards *RaftBackendFactory
	// Hosts stops a volume tenant's host.
	Hosts *TenantHostRegistry
	// Pauser pauses every Raft shard the node hosts before a node seal
	// wipes a key; nil pauses nothing.
	Pauser *ShardPauser
	// Databases seals a database's barrier on this node.
	Databases service.DatabaseServicer
	// Membership reads the system shard's membership.
	Membership featuregate.ShardMembershipReader
	// SystemShard is the system shard's ID.
	SystemShard uint64
	// ReplicaID is this node's system-shard replica ID.
	ReplicaID uint64
	// RaftAddress names this node in the records it writes.
	RaftAddress string
	// Timing is the lease, poll interval and acknowledgement timeout.
	Timing config.ClusterSealTiming
	// Streams is the registry streams subscribe to; a composer using
	// clusterseal.Deferred passes Deferred.Streams().
	Streams *clusterseal.Streams
	// Logger overrides slog.Default.
	Logger *slog.Logger
}

// NewClusterSealMonitor builds this node's cluster seal monitor from parts.
// Absent parts are left out of the enforcer as nil interfaces, never as typed
// nils.
func NewClusterSealMonitor(p ClusterSealParts) (*clusterseal.Monitor, error) {
	if p.KV == nil || p.Barriers == nil || p.Membership == nil {
		return nil, &clusterseal.ConfigError{Field: "parts", Reason: "KV, Barriers and Membership are required"}
	}
	enforcer := &SealEnforcer{Barriers: p.Barriers, Pauser: p.Pauser}
	if p.Shards != nil {
		enforcer.Shards = p.Shards
	}
	if p.Hosts != nil {
		enforcer.Hosts = p.Hosts
	}
	if ds, ok := p.Databases.(databaseSealer); ok {
		enforcer.Databases = ds
	}
	cfg := clusterseal.Config{
		KV:           NewClusterSealKV(p.KV),
		Retryable:    ClusterSealRetryable,
		Enforcer:     enforcer,
		Self:         strconv.FormatUint(p.ReplicaID, 10),
		Node:         p.RaftAddress,
		Members:      SystemShardMembers(p.Membership, p.SystemShard),
		Lease:        p.Timing.Lease,
		PollInterval: p.Timing.PollInterval,
		AckTimeout:   p.Timing.AckTimeout,
		Streams:      p.Streams,
		Logger:       p.Logger,
	}
	if p.Gate != nil {
		cfg.Gate = p.Gate
	}
	return clusterseal.NewMonitor(cfg)
}

// tenantShards is the slice of RaftBackendFactory the enforcer stops a
// tenant's dedicated shards through. It lists the shards on the host, starts
// in flight included, since a seal must stop every shard before it wipes the
// key they run on.
type tenantShards interface {
	ListOnHost() []StartedShard
	Close(tenantID, db string) error
}

// tenantHosts is the slice of TenantHostRegistry the enforcer stops a volume
// tenant's host through.
type tenantHosts interface {
	Close(tenantID string) error
}

// databaseSealer seals one database's barrier on this node.
// service.DatabaseService satisfies it.
type databaseSealer interface {
	SealDatabaseLocal(tenantID, name string) error
	// The database half of a whole-node seal (service.SealWholeNode).
	service.BarrierDatabases
}

// NewClusterSealMonitor finds databaseSealer by type assertion on a
// service.DatabaseServicer; this keeps a signature change from turning it
// into a silent miss.
var _ databaseSealer = (*service.DatabaseService)(nil)

// SealEnforcer seals barriers on this node for a clusterseal.Monitor, each
// with this node's own key. Every field but Barriers may be nil.
type SealEnforcer struct {
	// Barriers holds this node's node barrier and its tenant and database
	// barriers.
	Barriers *seal.BarrierRegistry
	// Shards stops a tenant's dedicated raft shards.
	Shards tenantShards
	// Hosts stops a volume tenant's host.
	Hosts tenantHosts
	// Databases seals a database's barrier and marks the database sealed.
	Databases databaseSealer
	// Pauser pauses every Raft shard the node hosts before a node seal
	// wipes a key, and resumes them once the node barrier is unsealed.
	Pauser *ShardPauser
}

var _ clusterseal.Enforcer = (*SealEnforcer)(nil)

// SealNode wipes this node's node key and every tenant and database key held
// on it, through the one whole-node seal (service.SealWholeNode), after
// pausing every Raft shard the node hosts, the system shard included. Nothing
// on a sealed node can be served; its shards stay paused, loaded, until the
// node barrier is unsealed here and Pauser resumes them.
func (e *SealEnforcer) SealNode(ctx context.Context) error {
	var dbs service.BarrierDatabases
	if e.Databases != nil {
		dbs = e.Databases
	}
	var shards service.ShardPauser
	if e.Pauser != nil {
		shards = e.Pauser
	}
	return service.SealWholeNode(ctx, shards, e.Barriers, nil, dbs)
}

// SealTenant stops tenantID's dedicated shards and volume host on this node
// -- closed, not purged -- and then wipes the tenant's key, so no apply ever
// runs against a sealed barrier. A tenant this node does not hold is sealed
// already.
func (e *SealEnforcer) SealTenant(_ context.Context, tenantID string) error {
	var errs []error
	if e.Shards != nil {
		for _, s := range e.Shards.ListOnHost() {
			if s.TenantID != tenantID {
				continue
			}
			if err := e.Shards.Close(s.TenantID, s.Database); err != nil {
				errs = append(errs, err)
			}
		}
	}
	if e.Hosts != nil {
		if err := e.Hosts.Close(tenantID); err != nil {
			errs = append(errs, err)
		}
	}
	if err := e.Barriers.SealTenant(tenantID); err != nil && !errors.Is(err, seal.ErrTenantNotFound) {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

// SealDatabase wipes the key of tenantID's database name on this node and
// marks it sealed. A database this node does not hold -- or holds for another
// tenant -- is sealed already.
func (e *SealEnforcer) SealDatabase(_ context.Context, tenantID, name string) error {
	if e.Databases == nil {
		return nil
	}
	err := e.Databases.SealDatabaseLocal(tenantID, name)
	var nf *database.NotFoundError
	if err == nil || errors.As(err, &nf) || errors.Is(err, seal.ErrTenantNotFound) {
		return nil
	}
	return err
}

// NodeUnsealed reports whether this node's node barrier is unsealed.
func (e *SealEnforcer) NodeUnsealed() bool {
	sb := e.Barriers.SystemBarrier()
	return sb != nil && !sb.IsSealed()
}

// TenantUnsealed reports whether tenantID's barrier is unsealed here.
func (e *SealEnforcer) TenantUnsealed(tenantID string) bool {
	tb, err := e.Barriers.GetTenant(tenantID)
	return err == nil && !tb.IsSealed()
}

// DatabaseUnsealed reports whether the database's barrier is unsealed here.
// A database barrier is registered under the database's name.
func (e *SealEnforcer) DatabaseUnsealed(_, name string) bool {
	return e.TenantUnsealed(name)
}

// clusterSealTenant canonicalizes a tenant for cluster sealing: "" and the
// system tenant are the system tenant.
func clusterSealTenant(tenantID string) string {
	return tenantid.Normalize(tenantID)
}

// sealedWatchService ends every watch it opens when the watched (tenant,
// database)'s barrier is sealed, or the node barrier, or this node is cut off
// from the seal records: a watch is a stream, and the gate only refuses one
// as it opens. The watch ends with the seal's typed refusal.
//
// Every open subscribes first and asks refuses after
// (clusterseal.Streams.SubscribeServing), so a seal landing between the
// request's resolution and the subscription refuses the open instead of
// leaving a stream nothing will end.
type sealedWatchService struct {
	inner    service.WatchServicer
	streams  *clusterseal.Streams
	tenantID string
	database string
	// refuses is the (tenant, database)'s refusal, re-asked once the stream
	// is subscribed; nil checks nothing.
	refuses func() error
}

var _ service.WatchServicer = (*sealedWatchService)(nil)

func newSealedWatchService(inner service.WatchServicer, streams *clusterseal.Streams, tenantID, db string, refuses func() error) *sealedWatchService {
	return &sealedWatchService{inner: inner, streams: streams, tenantID: clusterSealTenant(tenantID), database: db, refuses: refuses}
}

// Watch opens a watch on key that a seal ends.
func (s *sealedWatchService) Watch(ctx context.Context, key string, opts ...watch.WatchOption) (watch.Watcher, error) {
	return s.streams.GuardWatcher(s.tenantID, s.database, s.refuses, func() (watch.Watcher, error) {
		return s.inner.Watch(ctx, key, opts...)
	})
}

// WatchPrefix opens a watch on prefix that a seal ends.
func (s *sealedWatchService) WatchPrefix(ctx context.Context, prefix string, opts ...watch.WatchOption) (watch.Watcher, error) {
	return s.streams.GuardWatcher(s.tenantID, s.database, s.refuses, func() (watch.Watcher, error) {
		return s.inner.WatchPrefix(ctx, prefix, opts...)
	})
}

// WatchEntity opens an entity watch whose channel a seal closes.
func (s *sealedWatchService) WatchEntity(ctx context.Context, req *service.WatchEntityRequest) (<-chan service.WatchEntityEvent, error) {
	return s.guardEntities(ctx, func(ctx context.Context) (<-chan service.WatchEntityEvent, error) {
		return s.inner.WatchEntity(ctx, req)
	})
}

// WatchEntityType opens an entity-type watch whose channel a seal closes.
func (s *sealedWatchService) WatchEntityType(ctx context.Context, req *service.WatchEntityTypeRequest) (<-chan service.WatchEntityEvent, error) {
	return s.guardEntities(ctx, func(ctx context.Context) (<-chan service.WatchEntityEvent, error) {
		return s.inner.WatchEntityType(ctx, req)
	})
}

// guardEntities subscribes, checks the refusal, and only then opens the
// entity watch with open, whose channel a seal closes.
func (s *sealedWatchService) guardEntities(ctx context.Context, open func(context.Context) (<-chan service.WatchEntityEvent, error)) (<-chan service.WatchEntityEvent, error) {
	sub, err := s.streams.SubscribeServing(s.tenantID, s.database, s.refuses)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(ctx)
	ch, err := open(ctx)
	if err != nil {
		cancel()
		sub.Release()
		return nil, err
	}
	return servedEntityEvents(ch, sub.Done(), func() {
		cancel()
		sub.Release()
	}), nil
}
