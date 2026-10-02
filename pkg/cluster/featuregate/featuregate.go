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

// Package featuregate decides when a Raft group may be sent a command that
// only newer binaries can apply.
//
// A replica that meets a command its binary does not know refuses it and
// writes nothing, while its up-to-date peers apply it: in a group with mixed
// binaries the replicas diverge. The gate closes that window. Each node
// advertises the features its binary implements in a lease-backed record in
// the replicated system store (cluster.Advertiser over AdvertisementPrefix),
// keyed by its system replica ID and carrying its raft address, so a node
// that stops -- or is rolled back to a binary that does not advertise --
// stops counting when its lease expires.
//
// A feature is enabled for a group when every replica of the group that
// applies the log (voters and observers; witnesses keep no state machine)
// runs a binary that advertises it. A record counts only while its (replica
// ID, raft address) pair is a current member of the system shard, and a
// member of any other group is matched to a record by raft address, since
// placement assigns dedicated shards their own replica IDs.
//
// Once every system-shard member -- every node -- advertises a feature, the
// feature is ACTIVATED: recorded permanently under ActivationPrefix, and
// enabled for every group from then on with one atomic load. Activation is
// what makes a downgrade impossible: a binary that has this gate refuses to
// start while the cluster has activated a feature it lacks
// (*UnsupportedFeatureError). A binary that predates the gate cannot
// refuse, so a cluster that has activated a feature must never be rolled
// back below the release that introduced it.
//
// Before activation the decision is per group, from committed membership,
// cached per group on a generation that every advertisement change and every
// background refresh advances, so the write path does no membership read and
// no store read on a cache hit. A cached decision can lag a membership change
// by at most one refresh; that is safe only because a replica is never added
// to a group on a node that does not advertise every feature the group uses
// (AdmitReplica, enforced on every add path), so a membership change can only
// ever remove support the cache still assumes -- the lagging answer for a
// group that lost a non-supporting member is "not enabled", the side every
// binary understands.
//
// A node that is not yet a member can be admitted only when it advertises:
// either the feature advertisement or, under dynamic membership, the member
// registration carries its features (cluster.Member.Features), and both are
// lease-backed, so a node counts only while it is running the binary that
// wrote the record.
package featuregate

import (
	"context"
	"encoding/json"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/cluster"
)

// Feature names a cluster feature: a command or behaviour every replica of
// a group must understand before any replica may be sent it.
type Feature string

// GuardedWrites is protocol.OpGuarded: a write carried with compares the
// state machine evaluates at the write's own log position.
const GuardedWrites Feature = "guarded-writes"

// ClusterShardIDs is cluster-wide shard ID allocation: every raft database
// claims its shard ID from one replicated high-water mark (shard.Claimer)
// instead of a per-node counter, and no caller may name one. It adds no Raft command --
// the claim is the KV engine's existing compare-and-set -- so it gates nothing
// before activation. What activation buys is the gate's downgrade rule: once
// every node claims, a binary that allocates locally can neither start nor be
// admitted as a replica, so no node can re-issue a shard ID another holds.
const ClusterShardIDs Feature = "cluster-shard-ids"

// supported lists the features this binary implements. A new feature is
// added here, and to nothing else in this package.
var supported = []Feature{GuardedWrites, ClusterShardIDs}

// Supported returns the features this binary implements.
func Supported() []Feature {
	return slices.Clone(supported)
}

// SupportedNames returns Supported as strings, the form a cluster.Member
// advertises.
func SupportedNames() []string {
	names := make([]string, len(supported))
	for i, f := range supported {
		names[i] = string(f)
	}
	return names
}

const (
	// AdvertisementPrefix is where each node's lease-backed feature
	// advertisement lives: "__sys:cluster/features/nodes/{replicaID}".
	AdvertisementPrefix = "__sys:cluster/features/nodes/"

	// ActivationPrefix is where activated features are recorded, one
	// permanent key per feature: "__sys:cluster/features/activated/{feature}".
	ActivationPrefix = "__sys:cluster/features/activated/"

	// DefaultRefreshInterval is how often the gate re-reads advertisements,
	// activations and group membership, re-subscribes a lost watch, and
	// retries activation.
	DefaultRefreshInterval = 5 * time.Second
)

// Self identifies a node by its system-shard replica ID and raft address.
// The gate knows the local node by replica ID alone -- it always supports
// every feature of its own binary -- and advertises the address the system
// shard committed for it (AdvertisedSelf).
type Self struct {
	ReplicaID uint64
	Address   string
}

// GroupMembership is the committed membership of one Raft group.
type GroupMembership struct {
	// ConfigChangeID is the log index of the configuration change that
	// produced this membership.
	ConfigChangeID uint64

	// Replicas maps each replica that applies the group's log -- voters and
	// observers -- to its raft address.
	Replicas map[uint64]string

	// Witnesses maps each witness replica to its raft address. A witness
	// applies no command, so it does not decide whether a group may use a
	// feature; it runs on a node all the same, and activation -- which
	// enables a feature everywhere -- counts it.
	Witnesses map[uint64]string
}

// MembershipSource reports a group's committed membership from local state.
type MembershipSource interface {
	GroupMembership(shardID uint64) (GroupMembership, error)
}

// ActivationKV is the slice of the replicated system KV the gate records
// and reads activations through. ListAll must not filter reserved keys.
type ActivationKV interface {
	PutIfAbsent(ctx context.Context, key string, value []byte) (bool, error)
	ListAll(ctx context.Context, prefix string) ([]string, error)
}

// Config configures a Gate.
type Config struct {
	// Self is the local node.
	Self Self

	// SystemShardID is the shard every node is a member of; activation
	// requires every one of its members to support a feature.
	SystemShardID uint64

	// Members reports committed group membership.
	Members MembershipSource

	// RefreshInterval overrides DefaultRefreshInterval.
	RefreshInterval time.Duration

	// Logger overrides slog.Default.
	Logger *slog.Logger
}

// viewKey identifies one advertisement: the store it came from and the node
// that wrote it.
type viewKey struct {
	source int
	nodeID uint64
}

// Gate decides, per Raft group, whether a feature may be used, and whether a
// replica may be added on a node. It is safe for concurrent use. The zero
// Gate is not usable; build one with New.
type Gate struct {
	self        Self
	systemShard uint64
	members     MembershipSource
	refresh     time.Duration
	logger      *slog.Logger

	// activated holds one flag per supported feature; a feature this binary
	// does not implement has no flag and is never enabled.
	activated map[Feature]*atomic.Bool

	// view is the current advertisement snapshot across every store the
	// gate follows, replaced whole.
	mu   sync.Mutex // serializes view replacement
	view atomic.Pointer[map[viewKey]cluster.Member]

	// refusal is the activated feature this binary lacks, once
	// CheckActivated has found one; see Refusal.
	refusal atomic.Pointer[UnsupportedFeatureError]

	runMu   sync.Mutex
	started bool
	cancel  context.CancelFunc
	wg      sync.WaitGroup
}

// New returns a gate with no advertisements and nothing activated: until a
// feature is activated cluster-wide, Enabled answers false for every group,
// local-only or not.
func New(cfg Config) (*Gate, error) {
	if cfg.Members == nil {
		return nil, &ConfigError{Field: "Members"}
	}
	if cfg.Self.ReplicaID == 0 {
		return nil, &ConfigError{Field: "Self"}
	}
	g := &Gate{
		self:        cfg.Self,
		systemShard: cfg.SystemShardID,
		members:     cfg.Members,
		refresh:     cfg.RefreshInterval,
		logger:      cfg.Logger,
		activated:   make(map[Feature]*atomic.Bool, len(supported)),
	}
	if g.refresh <= 0 {
		g.refresh = DefaultRefreshInterval
	}
	if g.logger == nil {
		g.logger = slog.Default()
	}
	for _, f := range supported {
		g.activated[f] = new(atomic.Bool)
	}
	empty := map[viewKey]cluster.Member{}
	g.view.Store(&empty)
	return g, nil
}

// Enabled reports whether feature may be used in shardID's group: one atomic
// load of the cluster-wide activation flag, nothing else.
//
// It used to also answer true, ahead of cluster-wide activation, for a group
// whose CURRENT members all happened to advertise the feature (decide,
// below) -- etcd's rule for a cluster-version gate is that no member ever
// sees an opcode older members do not understand, and per-group early
// enablement broke exactly that: a later membership change can replace a
// supporting replica with one running an old binary -- a rolled-back node,
// or a replica removed and re-added after a downgrade -- and go-quicraft
// catches it up by REPLAYING THE GROUP'S COMMITTED LOG. An entry the group
// applied under the earlier, narrower membership that already used the
// feature is in that log; the old binary cannot apply it and diverges.
// Cluster-wide activation is permanent (CheckActivated refuses to start a
// binary that lacks an already-activated feature) specifically so this
// cannot happen: once true, Enabled can never go false for any group again.
// Before activation, a write that would use the feature keeps the documented
// resolve-time fallback instead (see docs/clustering).
func (g *Gate) Enabled(_ uint64, feature Feature) bool {
	return g.Activated(feature)
}

// decide reads shardID's committed membership and reports whether every
// replica that applies its log -- and, when withWitnesses, every witness --
// is on a node that supports feature.
func (g *Gate) decide(shardID uint64, feature Feature, withWitnesses bool) bool {
	group, err := g.members.GroupMembership(shardID)
	if err != nil {
		return false
	}
	system := group
	if shardID != g.systemShard {
		if system, err = g.members.GroupMembership(g.systemShard); err != nil {
			return false
		}
	}
	if len(group.Replicas) == 0 {
		return false
	}
	supporting := g.supportingAddresses(feature, system)
	for _, addr := range group.Replicas {
		if _, ok := supporting[addr]; !ok {
			return false
		}
	}
	if withWitnesses {
		for _, addr := range group.Witnesses {
			if _, ok := supporting[addr]; !ok {
				return false
			}
		}
	}
	return true
}

// supportingAddresses returns the raft addresses of the system-shard
// members -- this node included -- whose advertisement names feature. A
// record counts only while its (replica ID, address) is a current member.
func (g *Gate) supportingAddresses(feature Feature, system GroupMembership) map[string]struct{} {
	view := *g.view.Load()
	supporting := make(map[string]struct{}, len(system.Replicas)+len(system.Witnesses))
	consider := func(id uint64, addr string) {
		if id == g.self.ReplicaID {
			supporting[addr] = struct{}{}
			return
		}
		for k, rec := range view {
			if k.nodeID == id && rec.Address == addr && slices.Contains(rec.Features, string(feature)) {
				supporting[addr] = struct{}{}
				return
			}
		}
	}
	for id, addr := range system.Replicas {
		consider(id, addr)
	}
	for id, addr := range system.Witnesses {
		consider(id, addr)
	}
	return supporting
}

// Supports reports whether the node at raftAddr runs a binary that
// advertises feature: this node, or any node with a live advertisement --
// feature advertisement or member registration -- naming the address and
// the feature. A node that is not yet a member is judged by its own record,
// which is lease-backed: it counts only while the node keeps it alive.
func (g *Gate) Supports(raftAddr string, feature Feature) bool {
	if self, err := g.AdvertisedSelf(); err == nil && self.Address == raftAddr {
		return true
	}
	for _, rec := range *g.view.Load() {
		if rec.Address == raftAddr && slices.Contains(rec.Features, string(feature)) {
			return true
		}
	}
	return false
}

// AdmitReplica refuses, with *UnsupportedReplicaError, to add a replica of
// shardID -- voter, observer or witness -- on the node at raftAddr when that
// node does not advertise every feature the group uses: every activated
// feature, and every feature this group's CURRENT members all support.
// Go-quicraft catches a new member up by replaying the group's log, so a
// replica admitted on a node that cannot apply a feature would diverge on
// the first entry that uses it, and a proposal racing the config change
// could not be told apart from one before it. Refusing the add is what makes
// both impossible.
//
// The per-group check is decide, not Enabled, and the clearest case for why
// is the SYSTEM shard before activation: decide(systemShard, f, true) --
// every current member advertising f, witnesses included -- is the EXACT
// condition activate() waits for. Admitting a replica that does not
// advertise f onto the system shard right then would immediately make that
// condition false again, blocking activation on a node this check could
// have refused. Decide is what keeps "every current member advertises"
// true going forward rather than something activation can win and then
// immediately lose. The same decide call protects every OTHER shard the
// identical way, whether or not it is activated yet: a dedicated group's
// own current members already agreeing on a feature is what Enabled now
// deliberately ignores (see its doc comment) specifically because a FUTURE
// membership change could undo it, and admission on the join path is where
// that undoing is refused, not discovered later at apply time.
func (g *Gate) AdmitReplica(shardID uint64, raftAddr string) error {
	for _, f := range supported {
		if !g.Activated(f) && !g.decide(shardID, f, false) {
			continue
		}
		if !g.Supports(raftAddr, f) {
			return &UnsupportedReplicaError{ShardID: shardID, Address: raftAddr, Feature: f}
		}
	}
	return nil
}

// AdvertisedSelf returns what the local node advertises: its system-shard
// replica ID and the raft address the system shard has committed for it --
// the address other nodes match its record against.
func (g *Gate) AdvertisedSelf() (Self, error) {
	system, err := g.members.GroupMembership(g.systemShard)
	if err != nil {
		return Self{}, err
	}
	addr, ok := system.Replicas[g.self.ReplicaID]
	if !ok {
		addr, ok = system.Witnesses[g.self.ReplicaID]
	}
	if !ok {
		return Self{}, &NotAMemberError{ReplicaID: g.self.ReplicaID, ShardID: g.systemShard}
	}
	return Self{ReplicaID: g.self.ReplicaID, Address: addr}, nil
}

// Activated reports whether feature has been activated cluster-wide.
func (g *Gate) Activated(feature Feature) bool {
	flag, ok := g.activated[feature]
	return ok && flag.Load()
}

// CheckActivated reads the activated features and refuses with
// *UnsupportedFeatureError when the cluster has activated one this binary
// does not implement; otherwise it marks every activated feature enabled.
// It is the startup check that makes a downgrade impossible.
//
// An activation record is trusted as system-store state: it is written only
// by a node's gate, with PutIfAbsent, once every system-shard member
// advertises the feature, and the system namespace it lives in is writable
// only by a principal that can already rewrite any replicated system record.
// The recorded activated_by is kept for audit, not re-verified: whether it
// was a member when it wrote the record is not knowable from state that
// survives the membership change.
func (g *Gate) CheckActivated(ctx context.Context, kv ActivationKV) error {
	features, err := ActivatedFeatures(ctx, kv)
	if err != nil {
		return err
	}
	for _, f := range features {
		flag, ok := g.activated[f]
		if !ok {
			refusal := &UnsupportedFeatureError{Feature: f}
			g.refusal.Store(refusal)
			return refusal
		}
		flag.Store(true)
	}
	return nil
}

// Refusal returns the *UnsupportedFeatureError CheckActivated found -- the
// cluster has activated a feature this binary lacks, so this node must not
// serve -- or nil. A composer that cannot refuse to start reports it as a
// degraded node.
func (g *Gate) Refusal() error {
	if r := g.refusal.Load(); r != nil {
		return r
	}
	return nil
}

// ActivatedFeatures lists the features the cluster has activated.
func ActivatedFeatures(ctx context.Context, kv ActivationKV) ([]Feature, error) {
	keys, err := kv.ListAll(ctx, ActivationPrefix)
	if err != nil {
		return nil, &StoreError{Op: "list activations", Err: err}
	}
	features := make([]Feature, 0, len(keys))
	for _, k := range keys {
		if name := strings.TrimPrefix(k, ActivationPrefix); name != "" && name != k {
			features = append(features, Feature(name))
		}
	}
	return features, nil
}

// CheckActivated refuses with *UnsupportedFeatureError when the cluster
// has activated a feature this binary does not implement. It is the check
// a node that runs no Gate -- one whose groups cannot hold another binary --
// still makes at startup.
func CheckActivated(ctx context.Context, kv ActivationKV) error {
	features, err := ActivatedFeatures(ctx, kv)
	if err != nil {
		return err
	}
	for _, f := range features {
		if !slices.Contains(supported, f) {
			return &UnsupportedFeatureError{Feature: f}
		}
	}
	return nil
}

// activationRecord is the value stored under an activation key.
type activationRecord struct {
	Feature   Feature `json:"feature"`
	ReplicaID uint64  `json:"activated_by"`
}

// sourcedEvent is an advertisement event tagged with the store it came from.
type sourcedEvent struct {
	source int
	event  cluster.MemberEvent
}

// Start runs the startup check, seeds the advertisement view from every
// store in adverts -- the feature advertisements, and the member
// registrations when dynamic membership keeps them -- and starts the
// background loop that follows advertisement changes and activates
// features. It fails -- and the node must not start -- when the cluster has
// activated a feature this binary lacks.
func (g *Gate) Start(ctx context.Context, kv ActivationKV, adverts ...cluster.MembershipStore) error {
	if kv == nil || len(adverts) == 0 || slices.Contains(adverts, nil) {
		return &ConfigError{Field: "store"}
	}
	g.runMu.Lock()
	defer g.runMu.Unlock()
	if g.started {
		return &ConfigError{Field: "already started"}
	}
	if err := g.CheckActivated(ctx, kv); err != nil {
		return err
	}
	// Subscribe before seeding: a record written between the two is then
	// in the seed, the watch, or both, never neither.
	runCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	watches := make([]<-chan cluster.MemberEvent, len(adverts))
	for i, store := range adverts {
		ch, err := store.WatchMembers(runCtx)
		if err != nil {
			g.logger.Warn("feature gate: advertisement watch unavailable; relying on refresh", "error", err)
		}
		watches[i] = ch
	}
	for i, store := range adverts {
		if err := g.reseed(ctx, i, store); err != nil {
			cancel()
			return err
		}
	}
	g.activate(ctx, kv)

	events := make(chan sourcedEvent, 64)
	g.started = true
	g.cancel = cancel
	for i, store := range adverts {
		g.wg.Add(1)
		go g.follow(runCtx, i, store, watches[i], events)
	}
	g.wg.Add(1)
	go g.run(runCtx, kv, adverts, events)
	return nil
}

// Close stops the background loop. It is idempotent.
func (g *Gate) Close() error {
	g.runMu.Lock()
	cancel := g.cancel
	g.cancel = nil
	g.runMu.Unlock()
	if cancel != nil {
		cancel()
	}
	g.wg.Wait()
	return nil
}

// follow forwards one store's advertisement events, re-subscribing on the
// refresh interval when its watch is unavailable or ends.
func (g *Gate) follow(ctx context.Context, source int, store cluster.MembershipStore, ch <-chan cluster.MemberEvent, out chan<- sourcedEvent) {
	defer g.wg.Done()
	for {
		if ch != nil && !g.forward(ctx, source, ch, out) {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(g.refresh):
		}
		var err error
		if ch, err = store.WatchMembers(ctx); err != nil {
			g.logger.Warn("feature gate: advertisement watch unavailable; relying on refresh", "error", err)
			ch = nil
		}
	}
}

// forward relays ch until it closes (true: re-subscribe) or ctx ends
// (false). It does not rely on the store closing ch when ctx ends.
func (g *Gate) forward(ctx context.Context, source int, ch <-chan cluster.MemberEvent, out chan<- sourcedEvent) bool {
	for {
		select {
		case <-ctx.Done():
			return false
		case ev, ok := <-ch:
			if !ok {
				return true
			}
			select {
			case out <- sourcedEvent{source: source, event: ev}:
			case <-ctx.Done():
				return false
			}
		}
	}
}

// run applies advertisement events and refreshes on a timer.
func (g *Gate) run(ctx context.Context, kv ActivationKV, adverts []cluster.MembershipStore, events <-chan sourcedEvent) {
	defer g.wg.Done()
	ticker := time.NewTicker(g.refresh)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case ev := <-events:
			g.apply(ev.source, ev.event)
			g.activate(ctx, kv)
		case <-ticker.C:
			for i, store := range adverts {
				if err := g.reseed(ctx, i, store); err != nil {
					g.logger.Warn("feature gate: advertisement refresh failed", "error", err)
				}
			}
			if err := g.CheckActivated(ctx, kv); err != nil {
				g.logger.Error("feature gate: activation refresh failed", "error", err)
			}
			g.activate(ctx, kv)
		}
	}
}

// apply folds one advertisement event into the view.
func (g *Gate) apply(source int, ev cluster.MemberEvent) {
	g.mu.Lock()
	defer g.mu.Unlock()
	current := *g.view.Load()
	next := make(map[viewKey]cluster.Member, len(current)+1)
	for k, m := range current {
		next[k] = m
	}
	key := viewKey{source: source, nodeID: ev.Member.NodeID}
	switch ev.Type {
	case cluster.MemberJoined:
		next[key] = ev.Member
	case cluster.MemberLeft:
		if _, ok := next[key]; ok {
			g.logger.Info("feature gate: node withdrew its advertisement", "node_id", ev.Member.NodeID)
		}
		delete(next, key)
	}
	g.replaceLocked(next)
}

// reseed replaces one store's part of the view with its current records.
func (g *Gate) reseed(ctx context.Context, source int, store cluster.MembershipStore) error {
	members, err := store.ListMembers(ctx)
	if err != nil {
		return &StoreError{Op: "list advertisements", Err: err}
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	current := *g.view.Load()
	next := make(map[viewKey]cluster.Member, len(current)+len(members))
	for k, m := range current {
		if k.source != source {
			next[k] = m
		}
	}
	for _, m := range members {
		next[viewKey{source: source, nodeID: m.NodeID}] = m
	}
	g.replaceLocked(next)
	return nil
}

// replaceLocked installs next when it differs from the current view. The
// caller holds g.mu.
func (g *Gate) replaceLocked(next map[viewKey]cluster.Member) {
	if sameView(*g.view.Load(), next) {
		return
	}
	g.view.Store(&next)
}

// sameView reports whether two views advertise the same thing.
func sameView(a, b map[viewKey]cluster.Member) bool {
	if len(a) != len(b) {
		return false
	}
	for k, am := range a {
		bm, ok := b[k]
		if !ok || am.Address != bm.Address || !slices.Equal(am.Features, bm.Features) {
			return false
		}
	}
	return true
}

// activate records every supported feature that every system-shard member
// -- witnesses included -- now supports. Activation is permanent; a failure
// is retried on the next refresh.
func (g *Gate) activate(ctx context.Context, kv ActivationKV) {
	for _, f := range supported {
		if g.activated[f].Load() || !g.decide(g.systemShard, f, true) {
			continue
		}
		value, err := json.Marshal(activationRecord{Feature: f, ReplicaID: g.self.ReplicaID})
		if err != nil {
			continue
		}
		stored, err := kv.PutIfAbsent(ctx, ActivationPrefix+string(f), value)
		if err != nil {
			g.logger.Warn("feature gate: activation not recorded; retrying on refresh",
				"feature", string(f), "error", err)
			continue
		}
		g.activated[f].Store(true)
		if stored {
			g.logger.Info("feature gate: feature activated cluster-wide", "feature", string(f))
		} else {
			g.logger.Info("feature gate: feature activation observed", "feature", string(f))
		}
	}
}
