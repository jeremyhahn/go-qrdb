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

package cluster

import (
	"context"
	"log/slog"
	"sync"
	"time"
)

const (
	// MembershipPrefix is the key prefix under which member registrations are
	// stored. It lives in qrdb's "__sys:" system namespace so the entries are
	// hidden from user-facing KV list/scan (which filter that prefix) and kept
	// out of the tenant key space. Each member writes
	// "__sys:cluster/members/{nodeID}".
	MembershipPrefix = "__sys:cluster/members/"

	// DefaultLeaseTTL is how long a member registration survives without a
	// keepalive refresh before the backing lease expires.
	DefaultLeaseTTL = 10 * time.Second

	// DefaultKeepAliveInterval is how often a member refreshes its
	// registration. It must be comfortably shorter than the lease TTL.
	DefaultKeepAliveInterval = 3 * time.Second

	// DefaultReconfigMaxRetries bounds Raft add/remove retry attempts.
	DefaultReconfigMaxRetries = 3

	// DefaultReconfigBaseDelay is the base delay for reconfiguration
	// exponential backoff.
	DefaultReconfigBaseDelay = 100 * time.Millisecond

	// maxReconfigDelay caps the exponential backoff between retries.
	maxReconfigDelay = 5 * time.Second

	// reconfigTimeout bounds a single Raft add/remove attempt.
	reconfigTimeout = 30 * time.Second
)

// Member is a cluster member registration.
type Member struct {
	// NodeID is the member's unique Raft replica identifier.
	NodeID uint64 `json:"node_id"`

	// Address is the member's Raft network address.
	Address string `json:"address"`

	// APIEndpoints maps each client-facing API protocol this member serves to
	// the address clients should dial for it (for example
	// {"rest": "10.0.0.2:63002", "grpc": "10.0.0.2:63003"}).
	//
	// It is a protocol-keyed set rather than one canonical address because a
	// replica serves several protocols at once and a client that has located
	// the leader must reach it over the SAME protocol it is already speaking:
	// the payload codec, streaming semantics, TLS configuration and
	// credential-presentation shape all differ per protocol. Omitting a
	// protocol is how a node states that it does not serve it.
	//
	// The value is authoritative — each node publishes it from its own
	// configuration — and is never derived from Address by port arithmetic,
	// which breaks under NAT, split networking and non-default ports.
	//
	// The field is optional. A member registered before API advertisement
	// existed, or by a node whose operator configured no advertise address,
	// carries a nil map; consumers see no endpoint and fall back to their
	// pre-advertisement behavior rather than failing.
	APIEndpoints map[APIProtocol]string `json:"api_endpoints,omitempty"`

	// Metadata carries optional descriptive key-value pairs.
	Metadata map[string]string `json:"metadata,omitempty"`

	// Features names the cluster features the member's binary implements
	// (see pkg/cluster/featuregate). A member registered by a binary that
	// predates feature advertisement carries none, which is exactly what it
	// supports.
	Features []string `json:"features,omitempty"`
}

// MemberEventType distinguishes member join from member leave events.
type MemberEventType uint8

const (
	// MemberJoined indicates a member registration appeared.
	MemberJoined MemberEventType = iota

	// MemberLeft indicates a member registration expired or was removed.
	MemberLeft
)

// MemberEvent is a membership change surfaced by a MembershipStore.
type MemberEvent struct {
	// Type is the kind of change.
	Type MemberEventType

	// Member is the member the change pertains to.
	Member Member
}

// Reconfigurer applies Raft membership changes for a single shard and reports
// the current leader. The qrdb AdminService satisfies it (AddMember /
// RemoveMember plus a leader lookup wrapping GetLeaderID).
type Reconfigurer interface {
	// AddMember adds a replica to the shard's Raft group.
	AddMember(ctx context.Context, shardID, replicaID uint64, raftAddr string) error

	// RemoveMember removes a replica from the shard's Raft group.
	RemoveMember(ctx context.Context, shardID, replicaID uint64) error

	// Leader returns the current leader replica ID for the shard and whether
	// a leader is currently known.
	Leader(shardID uint64) (replicaID uint64, hasLeader bool, err error)

	// IsMember reports whether replicaID is currently part of the shard's
	// Raft group. The manager consults it to keep reconfiguration idempotent:
	// it skips AddMember for replicas already in the group (e.g. the initial
	// bootstrap members) and skips RemoveMember for replicas already gone.
	IsMember(shardID, replicaID uint64) (bool, error)
}

// MembershipStore persists member registrations and surfaces membership
// change events. The qrdb service layer satisfies it with the in-process MVCC
// lessor (lease-backed entries that auto-expire on node death) and the watch
// hub (a prefix watch over MembershipPrefix). Tests use an in-memory fake.
type MembershipStore interface {
	// PutMember writes (or refreshes) a member entry that expires after ttl
	// unless refreshed by a subsequent PutMember. Implementations back the
	// entry with a lease so that node death triggers automatic expiry.
	PutMember(ctx context.Context, nodeID uint64, data []byte, ttl time.Duration) error

	// DeleteMember removes a member entry.
	DeleteMember(ctx context.Context, nodeID uint64) error

	// WatchMembers returns a channel of membership change events. The channel
	// is closed when ctx is cancelled.
	WatchMembers(ctx context.Context) (<-chan MemberEvent, error)

	// ListMembers returns the current membership snapshot. The manager seeds
	// itself from this on Start and after a watch re-subscribe so members that
	// registered before this node began watching are visible — a keepalive
	// refreshes a lease without re-emitting a PUT, so a watch-only view would
	// miss them.
	ListMembers(ctx context.Context) ([]Member, error)
}

// MembershipConfig tunes the dynamic membership manager.
type MembershipConfig struct {
	// LeaseTTL is the registration TTL. Zero uses DefaultLeaseTTL.
	LeaseTTL time.Duration

	// KeepAliveInterval is the self-refresh cadence. Zero uses
	// DefaultKeepAliveInterval.
	KeepAliveInterval time.Duration

	// MaxRetries bounds Raft reconfiguration retries. Zero uses
	// DefaultReconfigMaxRetries.
	MaxRetries int

	// RetryBaseDelay is the reconfiguration backoff base. Zero uses
	// DefaultReconfigBaseDelay.
	RetryBaseDelay time.Duration

	// MinQuorum guards automatic removal: the manager refuses to remove a
	// replica from the Raft group when doing so would drop active membership
	// below MinQuorum, preventing a transient liveness blip from destroying
	// quorum. Zero disables the guard and is not recommended in production.
	MinQuorum int
}

func (c MembershipConfig) withDefaults() MembershipConfig {
	if c.LeaseTTL <= 0 {
		c.LeaseTTL = DefaultLeaseTTL
	}
	if c.KeepAliveInterval <= 0 {
		c.KeepAliveInterval = DefaultKeepAliveInterval
	}
	if c.MaxRetries <= 0 {
		c.MaxRetries = DefaultReconfigMaxRetries
	}
	if c.RetryBaseDelay <= 0 {
		c.RetryBaseDelay = DefaultReconfigBaseDelay
	}
	return c
}

// MembershipOption customizes a MembershipManager.
type MembershipOption func(*MembershipManager)

// WithReconfigurer enables Raft dynamic membership: when the local node is the
// leader, member join/leave events drive AddMember/RemoveMember on the shard.
// Without a Reconfigurer the manager only tracks membership (no Raft changes),
// which keeps the feature strictly opt-in.
func WithReconfigurer(r Reconfigurer) MembershipOption {
	return func(m *MembershipManager) {
		m.reconfig = r
	}
}

// WithMembershipLogger sets the structured logger. Defaults to slog.Default().
func WithMembershipLogger(l *slog.Logger) MembershipOption {
	return func(m *MembershipManager) {
		if l != nil {
			m.logger = l
		}
	}
}

// WithMembershipSealProbe hands p to the manager's advertiser (see
// WithAdvertiseSealProbe), so the local node's registration and its refresh
// are never proposed while the node barrier is sealed.
func WithMembershipSealProbe(p SealProbe) MembershipOption {
	return func(m *MembershipManager) {
		m.seal = p
	}
}

// MembershipManager maintains a node's cluster membership registration and,
// when a Reconfigurer is supplied and the local node is the leader, reconciles
// the Raft group as members join and leave. It is built on a MembershipStore
// (lease-backed entries + prefix watch), realizing the etcd-style lease+watch
// membership pattern on qrdb's own primitives.
//
// The manager is OPT-IN: callers construct it explicitly and Raft
// reconfiguration only occurs when WithReconfigurer is provided. The qrdbd
// daemon gates construction behind configuration that is off by default.
type MembershipManager struct {
	store    MembershipStore
	reconfig Reconfigurer
	self     Member
	shardID  uint64
	cfg      MembershipConfig
	logger   *slog.Logger
	seal     SealProbe

	// advertiser owns the local node's registration: writing the Member
	// descriptor (Raft address + advertised API endpoints) and refreshing its
	// lease. Delegating keeps exactly one registration code path shared with
	// callers that want advertisement without Raft reconfiguration.
	advertiser *Advertiser

	membersMu sync.RWMutex
	members   map[uint64]Member

	// reconfigWant holds the latest desired reconfiguration per replica,
	// drained by the single reconfigWorker goroutine. Last write wins.
	reconfigMu   sync.Mutex
	reconfigWant map[uint64]reconfigIntent
	reconfigWake chan struct{}

	mu        sync.Mutex
	started   bool
	closed    bool
	closeOnce sync.Once
	runCtx    context.Context
	cancel    context.CancelFunc
	wg        sync.WaitGroup
}

// reconfigIntent is a desired Raft reconfiguration for a single replica.
type reconfigIntent struct {
	member Member
	remove bool // true = remove from the group, false = add
}

// NewMembershipManager creates a membership manager for the given node and
// shard. store must be non-nil. Supply WithReconfigurer to enable Raft
// reconfiguration.
func NewMembershipManager(store MembershipStore, self Member, shardID uint64, cfg MembershipConfig, opts ...MembershipOption) (*MembershipManager, error) {
	if store == nil {
		return nil, &MembershipError{Reason: "store must not be nil"}
	}
	if err := ValidateAPIEndpoints(self.APIEndpoints); err != nil {
		return nil, err
	}
	m := &MembershipManager{
		store:        store,
		self:         self,
		shardID:      shardID,
		cfg:          cfg.withDefaults(),
		logger:       slog.Default(),
		members:      map[uint64]Member{self.NodeID: self},
		reconfigWant: make(map[uint64]reconfigIntent),
		reconfigWake: make(chan struct{}, 1),
	}
	for _, opt := range opts {
		opt(m)
	}
	return m, nil
}

// Start registers the local node and begins tracking membership. When a
// Reconfigurer is configured and the node is the leader, it also reconciles
// the Raft group. Start launches background goroutines that run until Close.
func (m *MembershipManager) Start(ctx context.Context) error {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return &MembershipError{Reason: "manager is closed"}
	}
	if m.started {
		m.mu.Unlock()
		return &MembershipError{Reason: "manager already started"}
	}
	// The manager owns a run context derived from the caller's: cancelling
	// either the caller's ctx OR calling Close stops every goroutine.
	runCtx, cancel := context.WithCancel(ctx)
	m.runCtx, m.cancel = runCtx, cancel
	m.mu.Unlock()

	// A fresh Advertiser per Start attempt: an Advertiser is single-use
	// (Close is terminal), and a rolled-back Start must remain retryable.
	advertiser, err := NewAdvertiser(m.store, m.self, AdvertiseConfig{
		LeaseTTL:          m.cfg.LeaseTTL,
		KeepAliveInterval: m.cfg.KeepAliveInterval,
	}, WithAdvertiseLogger(m.logger), WithAdvertiseSealProbe(m.seal))
	if err != nil {
		m.abortStart(runCtx)
		return err
	}
	m.mu.Lock()
	m.advertiser = advertiser
	m.mu.Unlock()

	if err := advertiser.Start(runCtx); err != nil {
		m.abortStart(runCtx)
		return err
	}

	// Subscribe before seeding so no change between the snapshot and the
	// subscription is lost; an initial subscription failure surfaces config
	// errors to the caller.
	events, err := m.store.WatchMembers(runCtx)
	if err != nil {
		m.abortStart(runCtx)
		return &MembershipCauseError{Reason: "failed to watch members", Cause: err}
	}

	// Mark started only after registration + subscription succeed, so a failed
	// Start can be retried and does not strand a lease.
	m.mu.Lock()
	m.started = true
	m.mu.Unlock()

	m.reconcile(runCtx)

	m.wg.Add(2)
	go m.watchLoop(runCtx, events)
	go m.reconfigWorker(runCtx)

	return nil
}

// abortStart rolls back a failed Start: cancel the run context, deregister any
// lease the Advertiser may have created, and reset start state so a retry is
// possible.
func (m *MembershipManager) abortStart(_ context.Context) {
	if m.cancel != nil {
		m.cancel()
	}
	m.mu.Lock()
	advertiser := m.advertiser
	m.advertiser = nil
	m.started = false
	m.runCtx = nil
	m.cancel = nil
	m.mu.Unlock()

	if advertiser != nil {
		// Close stops the refresh loop and deregisters on a fresh context.
		if err := advertiser.Close(); err != nil {
			m.logger.Warn("membership rollback deregister failed", "node_id", m.self.NodeID, "error", err)
		}
		return
	}
	_ = m.deregister()
}

// reconcile loads the current membership snapshot and converges local state to
// it: members present in the snapshot are joined; tracked members absent from
// it are treated as departed. This catches changes that happened before this
// node began watching (a keepalive refreshes a lease without re-emitting a PUT)
// and changes missed during a watch disconnect. A snapshot failure is
// non-fatal — the watch still runs.
func (m *MembershipManager) reconcile(ctx context.Context) {
	snapshot, err := m.store.ListMembers(ctx)
	if err != nil {
		m.logger.Warn("membership snapshot failed; continuing watch-only", "error", err)
		return
	}

	present := make(map[uint64]bool, len(snapshot))
	for _, member := range snapshot {
		present[member.NodeID] = true
		m.handleJoin(member)
	}

	// Remove tracked members no longer present in the snapshot (self excluded).
	m.membersMu.RLock()
	var departed []Member
	for id, member := range m.members {
		if id != m.self.NodeID && !present[id] {
			departed = append(departed, member)
		}
	}
	m.membersMu.RUnlock()
	for _, member := range departed {
		m.handleLeave(member)
	}
}

// watchLoop consumes membership events and re-subscribes (with backoff) if the
// feed closes — a transport watch closes on stream/read errors (server
// restart, leader failover, network blip), so without re-subscription the
// manager would go permanently blind while keepalives continued.
func (m *MembershipManager) watchLoop(runCtx context.Context, events <-chan MemberEvent) {
	defer m.wg.Done()

	backoff := m.cfg.RetryBaseDelay
	for {
		m.consume(runCtx, events)

		if runCtx.Err() != nil {
			return
		}
		// The feed closed unexpectedly. Back off, re-subscribe, and re-seed to
		// catch any change missed while disconnected.
		select {
		case <-runCtx.Done():
			return
		case <-time.After(backoff):
		}
		if backoff < maxReconfigDelay {
			backoff *= 2
		}

		next, err := m.store.WatchMembers(runCtx)
		if err != nil {
			m.logger.Warn("membership watch re-subscribe failed; will retry", "error", err)
			continue
		}
		backoff = m.cfg.RetryBaseDelay
		m.reconcile(runCtx)
		events = next
	}
}

// consume drains one membership-event channel until it closes or the run
// context is cancelled.
func (m *MembershipManager) consume(runCtx context.Context, events <-chan MemberEvent) {
	for {
		select {
		case <-runCtx.Done():
			return
		case ev, ok := <-events:
			if !ok {
				return
			}
			switch ev.Type {
			case MemberJoined:
				m.handleJoin(ev.Member)
			case MemberLeft:
				m.handleLeave(ev.Member)
			}
		}
	}
}

// handleJoin records a joining member and enqueues a reconfiguration intent.
func (m *MembershipManager) handleJoin(member Member) {
	m.membersMu.Lock()
	_, existed := m.members[member.NodeID]
	m.members[member.NodeID] = member
	m.membersMu.Unlock()

	if existed || member.NodeID == m.self.NodeID {
		return
	}
	m.logger.Info("member joined", "node_id", member.NodeID, "address", member.Address)
	if m.reconfig != nil {
		m.enqueueReconfig(reconfigIntent{member: member, remove: false})
	}
}

// handleLeave records a departing member and enqueues a removal intent.
func (m *MembershipManager) handleLeave(member Member) {
	m.membersMu.Lock()
	delete(m.members, member.NodeID)
	m.membersMu.Unlock()

	if member.NodeID == m.self.NodeID {
		return
	}
	m.logger.Info("member left", "node_id", member.NodeID)
	if m.reconfig != nil {
		m.enqueueReconfig(reconfigIntent{member: member, remove: true})
	}
}

// enqueueReconfig records the latest desired reconfiguration for a replica and
// wakes the worker. Last write wins, so a join quickly followed by a leave (or
// vice versa) supersedes the earlier intent rather than racing it.
func (m *MembershipManager) enqueueReconfig(intent reconfigIntent) {
	m.reconfigMu.Lock()
	m.reconfigWant[intent.member.NodeID] = intent
	m.reconfigMu.Unlock()
	select {
	case m.reconfigWake <- struct{}{}:
	default:
	}
}

// reconfigWorker is the single goroutine that applies Raft reconfiguration. It
// serializes all add/remove operations (one in-flight at a time, keyed by the
// latest per-replica intent), so flapping cannot run opposite-direction
// changes concurrently, and Close waits for it via the WaitGroup.
func (m *MembershipManager) reconfigWorker(runCtx context.Context) {
	defer m.wg.Done()
	for {
		select {
		case <-runCtx.Done():
			return
		case <-m.reconfigWake:
		}

		for {
			if runCtx.Err() != nil {
				return
			}
			m.reconfigMu.Lock()
			var id uint64
			var intent reconfigIntent
			found := false
			for k, v := range m.reconfigWant {
				id, intent, found = k, v, true
				break
			}
			m.reconfigMu.Unlock()
			if !found {
				break
			}

			m.applyReconfig(intent)

			// Clear the intent only if it has not been superseded by a newer
			// one of the opposite direction while we were applying it.
			m.reconfigMu.Lock()
			if cur, ok := m.reconfigWant[id]; ok && cur.remove == intent.remove {
				delete(m.reconfigWant, id)
			}
			m.reconfigMu.Unlock()
		}
	}
}

// applyReconfig validates and applies a single reconfiguration intent. All
// leader/membership/quorum checks happen here, at execution time, so a stale
// queued intent re-validates against current state before mutating the group.
func (m *MembershipManager) applyReconfig(intent reconfigIntent) {
	if !m.isLeader() {
		return // only the leader reconfigures; nothing to do as a follower
	}
	member := intent.member

	present, err := m.reconfig.IsMember(m.shardID, member.NodeID)
	if err != nil {
		m.logger.Warn("membership probe failed; deferring reconfiguration",
			"node_id", member.NodeID, "error", err)
		return
	}

	if intent.remove {
		if !present {
			return // already gone
		}
		// Quorum guard: refuse to shrink below MinQuorum so a transient blip
		// cannot destroy quorum.
		m.membersMu.RLock()
		remaining := len(m.members)
		m.membersMu.RUnlock()
		if m.cfg.MinQuorum > 0 && remaining < m.cfg.MinQuorum {
			m.logger.Warn("skipping auto-removal: would drop below quorum",
				"node_id", member.NodeID, "remaining", remaining, "min_quorum", m.cfg.MinQuorum)
			return
		}
		m.reconfigWithRetry("remove", member, func(ctx context.Context) error {
			return m.reconfig.RemoveMember(ctx, m.shardID, member.NodeID)
		})
		return
	}

	if present {
		return // already in the group (e.g. initial bootstrap members)
	}
	m.reconfigWithRetry("add", member, func(ctx context.Context) error {
		return m.reconfig.AddMember(ctx, m.shardID, member.NodeID, member.Address)
	})
}

// isLeader reports whether the local node is the shard leader.
func (m *MembershipManager) isLeader() bool {
	leaderID, hasLeader, err := m.reconfig.Leader(m.shardID)
	if err != nil || !hasLeader {
		return false
	}
	return leaderID == m.self.NodeID
}

// reconfigWithRetry runs op up to MaxRetries times with capped exponential
// backoff, aborting if the manager closes. It runs only on the worker
// goroutine, so reconfiguration is never concurrent.
func (m *MembershipManager) reconfigWithRetry(kind string, member Member, op func(context.Context) error) {
	for attempt := 1; attempt <= m.cfg.MaxRetries; attempt++ {
		// Derive the op context from runCtx so Close aborts an in-flight
		// reconfiguration instead of blocking up to the full timeout.
		ctx, cancel := context.WithTimeout(m.runCtx, reconfigTimeout)
		err := op(ctx)
		cancel()
		if err == nil {
			m.logger.Info("raft reconfiguration succeeded",
				"op", kind, "node_id", member.NodeID, "shard_id", m.shardID)
			return
		}

		m.logger.Warn("raft reconfiguration failed",
			"op", kind, "node_id", member.NodeID, "attempt", attempt, "error", err)
		if attempt >= m.cfg.MaxRetries {
			m.logger.Error("raft reconfiguration exhausted retries",
				"op", kind, "node_id", member.NodeID, "shard_id", m.shardID)
			return
		}

		delay := m.cfg.RetryBaseDelay * time.Duration(1<<uint(attempt-1))
		if delay > maxReconfigDelay {
			delay = maxReconfigDelay
		}
		select {
		case <-time.After(delay):
		case <-m.runCtx.Done():
			return
		}
	}
}

// Members returns a snapshot of the currently tracked members.
func (m *MembershipManager) Members() []Member {
	m.membersMu.RLock()
	defer m.membersMu.RUnlock()
	members := make([]Member, 0, len(m.members))
	for _, member := range m.members {
		members = append(members, member)
	}
	return members
}

// Close cancels the manager's run context, waits for its background goroutines
// to exit, and deregisters the local node. It is safe to call multiple times
// and safe to call without a prior Start.
func (m *MembershipManager) Close() error {
	m.closeOnce.Do(func() {
		m.mu.Lock()
		m.closed = true
		cancel := m.cancel
		m.mu.Unlock()
		if cancel != nil {
			cancel()
		}
		m.wg.Wait()
	})

	// Deregister on a fresh context: the run context is now cancelled. The
	// store clears its lease state, so a second Close is a no-op.
	return m.deregister()
}

// deregister withdraws this node's record on a fresh context. While the node
// barrier is sealed the withdrawal is not proposed: the record is left to
// expire with its lease, and the refusal is reported as *NodeSealedError.
func (m *MembershipManager) deregister() error {
	if m.seal != nil && m.seal.IsSealed() {
		return &NodeSealedError{Op: "deregister"}
	}
	ctx, cancel := context.WithTimeout(context.Background(), reconfigTimeout)
	defer cancel()
	if err := m.store.DeleteMember(ctx, m.self.NodeID); err != nil {
		return &MembershipCauseError{Reason: "failed to deregister self", Cause: err}
	}
	return nil
}
