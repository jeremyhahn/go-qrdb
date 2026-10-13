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

package clusterseal

import (
	"context"
	"log/slog"
	"maps"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/cluster/featuregate"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
)

// Defaults for Config.
const (
	DefaultLease        = 15 * time.Second
	DefaultPollInterval = time.Second
	DefaultAckTimeout   = 5 * time.Second
)

// KV is the slice of the replicated system store the monitor needs. Get and
// Scan must be linearizable reads; PutIfAbsent and CompareAndSwap must be
// evaluated by the state machine, so replicas cannot diverge.
type KV interface {
	// Get returns the value under key and whether it exists.
	Get(ctx context.Context, key string) (value []byte, found bool, err error)
	// PutIfAbsent stores value only when key is absent.
	PutIfAbsent(ctx context.Context, key string, value []byte) (stored bool, err error)
	// CompareAndSwap replaces key's value only while it equals expected.
	CompareAndSwap(ctx context.Context, key string, expected, next []byte) (swapped bool, err error)
	// Put stores value under key.
	Put(ctx context.Context, key string, value []byte) error
	// Scan returns every key and value under prefix.
	Scan(ctx context.Context, prefix string) (map[string][]byte, error)
}

// Enforcer seals one barrier on this node with this node's own key, and
// reports which barriers this node has unsealed. The composer implements it
// over the barrier registry and the tenant shards. Every Seal method is
// idempotent and must succeed for a barrier this node does not hold.
type Enforcer interface {
	// SealNode stops this node's data plane from serving and wipes its
	// node key and every tenant and database key held on it.
	SealNode(ctx context.Context) error
	// SealTenant stops tenantID's shards on this node and wipes its key.
	SealTenant(ctx context.Context, tenantID string) error
	// SealDatabase wipes the key of tenantID's database on this node.
	SealDatabase(ctx context.Context, tenantID, database string) error
	// NodeUnsealed reports whether this node's node barrier is unsealed.
	NodeUnsealed() bool
	// TenantUnsealed reports whether tenantID's barrier is unsealed here.
	TenantUnsealed(tenantID string) bool
	// DatabaseUnsealed reports whether the database's barrier is unsealed
	// here.
	DatabaseUnsealed(tenantID, database string) bool
}

// ActivationReader reports cluster-wide feature activation;
// *featuregate.Gate satisfies it.
type ActivationReader interface {
	Activated(feature featuregate.Feature) bool
	// ActivationsLoaded reports whether the activations have been read.
	// Until they have, Activated answers false without knowing.
	ActivationsLoaded() bool
}

// Clock is the monitor's time source.
type Clock interface {
	Now() time.Time
	After(d time.Duration) <-chan time.Time
}

type realClock struct{}

func (realClock) Now() time.Time                         { return time.Now() }
func (realClock) After(d time.Duration) <-chan time.Time { return time.After(d) }

// Local is this node's own state for one barrier.
type Local string

// The states Status reports for this node.
const (
	// LocalServing: the record is clear and this node has unsealed the
	// barrier.
	LocalServing Local = "serving"
	// LocalSealed: this node's barrier is sealed, or about to be.
	LocalSealed Local = "sealed"
	// LocalHeld: the node barrier is unsealed here but its record is
	// sealed; the node refuses requests until the record is cleared.
	LocalHeld Local = "held"
	// LocalCutOff: this node's lease on the records ran out.
	LocalCutOff Local = "cut_off"
	// LocalUnconfirmed: this node has not read the records since it
	// started.
	LocalUnconfirmed Local = "unconfirmed"
)

// Status is a barrier's cluster seal state as this node sees it.
type Status struct {
	// Active is true once the cluster has activated cluster-seal. When it
	// is false every seal is node-local and the other fields are zero.
	Active bool
	// Record is the barrier's record; the zero Record when none exists.
	Record Record
	// Local is this node's own state for the barrier.
	Local Local
}

// Sealer is what the barrier, tenant and database services call. *Monitor
// implements it.
type Sealer interface {
	// Active reports whether seals are cluster-wide.
	Active() bool
	// Seal records a seal of b and seals it on this node.
	Seal(ctx context.Context, b Barrier, by Principal) (Record, error)
	// Clear records the end of b's cluster seal. It unseals nothing.
	Clear(ctx context.Context, b Barrier, by Principal) (Record, error)
	// Status reports b's record and this node's state for it.
	Status(b Barrier) Status
	// Refuses reports whether b's record keeps it from being served here.
	Refuses(b Barrier) bool
}

var _ Sealer = (*Monitor)(nil)

// Config configures a Monitor.
type Config struct {
	// KV is the replicated system store.
	KV KV
	// Gate reports cluster-seal activation. Nil means never active: a
	// standalone node, or a raft node with no transport.
	Gate ActivationReader
	// Enforcer seals barriers on this node.
	Enforcer Enforcer
	// Self is this node's identity in the system shard -- its replica ID --
	// under which it acknowledges a node-barrier seal.
	Self string
	// Node names this node in the records it writes; its raft address.
	// Defaults to Self.
	Node string
	// Members lists the current system-shard members by the identity Self
	// uses.
	Members func(ctx context.Context) ([]string, error)
	// Lease is how long a successful read lets this node serve. Default
	// DefaultLease.
	Lease time.Duration
	// PollInterval is how often the records are read. Default
	// DefaultPollInterval; at most a third of Lease.
	PollInterval time.Duration
	// AckTimeout bounds the wait for every node's acknowledgement before a
	// node key is wiped. Default DefaultAckTimeout.
	AckTimeout time.Duration
	// Clock overrides the real clock in tests.
	Clock Clock
	// Logger overrides slog.Default.
	Logger *slog.Logger
	// Streams is the stream registry to end streams through. Nil builds a
	// new one; a composer using Deferred passes Deferred.Streams().
	Streams *Streams
}

// view is the records as this node last knew them, replaced whole.
type view struct {
	records map[Barrier]Record
	// corrupt names barriers whose stored record cannot be decoded; each
	// is refused until the record is rewritten.
	corrupt map[Barrier]struct{}
}

// Monitor reads the cluster seal records, enforces them on this node, and
// gates this node's service on a fresh lease. Its gate and status methods are
// lock-free; enforcement, Seal and Clear are serialized.
type Monitor struct {
	cfg     Config
	streams *Streams
	logger  *slog.Logger

	mu      sync.Mutex // serializes enforcement, Seal and Clear
	applied map[Barrier]uint64
	// primed is set by this monitor's first read made once the gate knows
	// whether cluster-seal is active: the read after boot that the
	// first-read exemption covers. A read made before then cannot tell
	// whether records may exist and primes nothing. m.mu guards it.
	primed bool

	view     atomic.Pointer[view]
	deadline atomic.Pointer[time.Time] // lease end; nil before the first read
	cutOff   atomic.Bool               // the lease ran out and streams were ended
}

// NewMonitor validates cfg, applies its defaults, and builds a Monitor.
func NewMonitor(cfg Config) (*Monitor, error) {
	switch {
	case cfg.KV == nil:
		return nil, &ConfigError{Field: "KV", Reason: "required"}
	case cfg.Enforcer == nil:
		return nil, &ConfigError{Field: "Enforcer", Reason: "required"}
	case cfg.Self == "":
		return nil, &ConfigError{Field: "Self", Reason: "required"}
	case cfg.Members == nil:
		return nil, &ConfigError{Field: "Members", Reason: "required"}
	case cfg.Lease < 0:
		return nil, &ConfigError{Field: "Lease", Reason: "must be positive"}
	case cfg.PollInterval < 0:
		return nil, &ConfigError{Field: "PollInterval", Reason: "must be positive"}
	case cfg.AckTimeout < 0:
		return nil, &ConfigError{Field: "AckTimeout", Reason: "must be positive"}
	}
	if cfg.Lease == 0 {
		cfg.Lease = DefaultLease
	}
	if cfg.PollInterval == 0 {
		cfg.PollInterval = DefaultPollInterval
	}
	if cfg.AckTimeout == 0 {
		cfg.AckTimeout = DefaultAckTimeout
	}
	if 3*cfg.PollInterval > cfg.Lease {
		return nil, &ConfigError{Field: "PollInterval", Reason: "must be at most a third of Lease"}
	}
	if cfg.Clock == nil {
		cfg.Clock = realClock{}
	}
	if cfg.Node == "" {
		cfg.Node = cfg.Self
	}
	logger := cfg.Logger
	if logger == nil {
		logger = slog.Default()
	}
	streams := cfg.Streams
	if streams == nil {
		streams = NewStreams()
	}
	m := &Monitor{
		cfg:     cfg,
		streams: streams,
		logger:  logger,
		applied: make(map[Barrier]uint64),
	}
	m.view.Store(&view{records: map[Barrier]Record{}, corrupt: map[Barrier]struct{}{}})
	return m, nil
}

// Streams returns the registry every long-lived stream on this node
// subscribes to.
func (m *Monitor) Streams() *Streams { return m.streams }

// Active reports whether the cluster has activated cluster-seal.
func (m *Monitor) Active() bool {
	return m.cfg.Gate != nil && m.cfg.Gate.Activated(featuregate.ClusterSeal)
}

// Refusing reports whether this node must refuse every request outside the
// sealed-window allowlist: cluster sealing is active and the node holds no
// fresh lease on the records, or the node barrier's record is sealed.
func (m *Monitor) Refusing() bool {
	// A node that has not read the cluster's activations cannot tell an
	// inactive feature from one it has not learned of yet -- nor, then,
	// whether its node record says sealed. It refuses until it knows.
	if m.cfg.Gate != nil && !m.cfg.Gate.ActivationsLoaded() {
		return true
	}
	if !m.Active() {
		return false
	}
	return !m.leaseFresh() || m.refuses(NodeBarrier())
}

// IsSealed makes the monitor a sealed-window probe on its own; see
// NodeProbe for the one the composer installs.
func (m *Monitor) IsSealed() bool { return m.Refusing() }

// Refuses reports whether b's record keeps it from being served on this
// node: the record is sealed or cannot be read, or this node does not know it
// yet -- its gate has not read the activations, or cluster sealing is active
// and the monitor has not made its first read. Request resolution and the
// tenant shard reconciler both ask, and nothing else starts a tenant's
// runtime: the composers start the reconciler only after Deferred.Set, and
// BuildTenantInfra starts no tenant at boot when a sealer is wired. So a
// sealed tenant's or database's shards never start before this node has read
// its record.
func (m *Monitor) Refuses(b Barrier) bool {
	if m.cfg.Gate != nil && !m.cfg.Gate.ActivationsLoaded() {
		return true
	}
	if !m.Active() {
		return false
	}
	return m.deadline.Load() == nil || m.refuses(b)
}

func (m *Monitor) refuses(b Barrier) bool {
	v := m.view.Load()
	if _, bad := v.corrupt[b]; bad {
		return true
	}
	return v.records[b].Sealed
}

func (m *Monitor) leaseFresh() bool {
	d := m.deadline.Load()
	return d != nil && m.cfg.Clock.Now().Before(*d)
}

// Status reports b's record and this node's state for it.
func (m *Monitor) Status(b Barrier) Status {
	if !m.Active() {
		return Status{}
	}
	rec := m.view.Load().records[b]
	return Status{Active: true, Record: rec, Local: m.local(b)}
}

// unsealedHere maps each barrier level to this node's question about it.
var unsealedHere = map[Kind]func(Enforcer, Barrier) bool{
	KindNode:     func(e Enforcer, _ Barrier) bool { return e.NodeUnsealed() },
	KindTenant:   func(e Enforcer, b Barrier) bool { return e.TenantUnsealed(b.Tenant()) },
	KindDatabase: func(e Enforcer, b Barrier) bool { return e.DatabaseUnsealed(b.Tenant(), b.Database()) },
}

func (m *Monitor) local(b Barrier) Local {
	switch {
	case m.deadline.Load() == nil:
		return LocalUnconfirmed
	case !m.leaseFresh():
		return LocalCutOff
	}
	unsealed, ok := unsealedHere[b.Kind()]
	if !ok || !unsealed(m.cfg.Enforcer, b) {
		return LocalSealed
	}
	if m.refuses(b) {
		if b.Kind() == KindNode {
			return LocalHeld
		}
		return LocalSealed
	}
	return LocalServing
}

// Run reads the records every poll interval until ctx is cancelled.
func (m *Monitor) Run(ctx context.Context) {
	for {
		m.poll(ctx)
		select {
		case <-ctx.Done():
			return
		case <-m.cfg.Clock.After(m.cfg.PollInterval):
		}
	}
}

// poll reads every record, publishes them, enforces each newly sealed epoch,
// and renews the lease. A failed read renews nothing and may cut the node
// off.
//
// Before cluster-seal is activated it still reads and publishes the records
// and renews the lease, but enforces nothing and cuts nothing off: no record
// can exist before activation, so the read is accurate, and a lease already
// fresh when the feature turns on means the node serves at once rather than
// refusing every request until its next read. The first-read-after-boot
// exemption (applyLocked) covers only the monitor's first read made once the
// gate has loaded the activations: a node that was reading before activation
// unsealed before any seal, and enforces one it reads later. A monitor
// without a gate can never activate and reads nothing.
func (m *Monitor) poll(ctx context.Context) {
	if m.cfg.Gate == nil {
		return
	}
	// What the gate knew when this read began, sampled once: it loads the
	// activations on its own goroutine, and a read that saw the feature
	// inactive applied nothing, so it must not spend the boot exemption
	// because the gate finished loading while it was in flight. Loaded is
	// sampled first; once true, Active is the cluster's answer.
	loaded := m.cfg.Gate.ActivationsLoaded()
	active := m.Active()
	if active {
		m.checkLease()
	}
	start := m.cfg.Clock.Now()
	rctx, cancel := context.WithTimeout(ctx, 2*m.cfg.PollInterval)
	raw, err := m.cfg.KV.Scan(rctx, RecordsPrefix)
	cancel()
	if err != nil {
		m.logger.Debug("cluster seal: records unreadable", slog.Any("error", err))
		if active {
			m.checkLease()
		}
		return
	}
	records, corrupt := m.decode(raw)

	m.mu.Lock()
	defer m.mu.Unlock()
	m.publish(records, corrupt)
	if active {
		initial := !m.primed
		for _, rec := range records {
			m.applyLocked(ctx, rec, initial)
		}
	}
	if active || loaded {
		m.primed = true
	}
	deadline := start.Add(m.cfg.Lease)
	m.deadline.Store(&deadline)
	if active && m.cutOff.Swap(false) {
		m.logger.Info("cluster seal: records readable again; serving")
	}
}

// checkLease ends every open stream, once, when a lease this node held runs
// out. Its keys stay in memory.
func (m *Monitor) checkLease() {
	if m.deadline.Load() == nil || m.leaseFresh() {
		return
	}
	if m.cutOff.CompareAndSwap(false, true) {
		m.logger.Warn("cluster seal: cannot read the seal records; refusing requests until it can",
			slog.Duration("lease", m.cfg.Lease))
		m.streams.EndAll(&SealStateUnconfirmedError{})
	}
}

func (m *Monitor) decode(raw map[string][]byte) (map[Barrier]Record, map[Barrier]struct{}) {
	records := make(map[Barrier]Record, len(raw))
	corrupt := map[Barrier]struct{}{}
	for key, val := range raw {
		b, err := ParseBarrier(strings.TrimPrefix(key, RecordsPrefix))
		if err != nil {
			m.logger.Error("cluster seal: record under an invalid key", slog.String("key", key))
			continue
		}
		rec, err := decodeRecord(val)
		if err != nil || rec.Barrier != b {
			m.logger.Error("cluster seal: corrupt record; refusing its barrier",
				slog.String("barrier", string(b)), slog.Any("error", err))
			corrupt[b] = struct{}{}
			continue
		}
		records[b] = rec
	}
	return records, corrupt
}

// publish merges records into the view, keeping the higher epoch per
// barrier: a read that started before a local write must not reopen a gate
// the write closed. Corruption is taken from the latest read.
func (m *Monitor) publish(records map[Barrier]Record, corrupt map[Barrier]struct{}) {
	cur := m.view.Load()
	next := &view{records: maps.Clone(cur.records), corrupt: corrupt}
	for b, rec := range records {
		if rec.Epoch >= next.records[b].Epoch {
			next.records[b] = rec
		}
	}
	m.view.Store(next)
}

// publishOne merges one record into the view; m.mu is held.
func (m *Monitor) publishOne(rec Record) {
	cur := m.view.Load()
	corrupt := maps.Clone(cur.corrupt)
	delete(corrupt, rec.Barrier)
	m.publish(map[Barrier]Record{rec.Barrier: rec}, corrupt)
}

// applyLocked acts on one record; m.mu is held. A clear is noted; a newer
// sealed epoch is enforced, except that a node barrier found sealed on this
// node's first read after it started is held: its key comes from this
// boot's own unseal.
func (m *Monitor) applyLocked(ctx context.Context, rec Record, initial bool) {
	if rec.Epoch <= m.applied[rec.Barrier] {
		return
	}
	if !rec.Sealed {
		// Seal and clear alternate, each one epoch past the last, so a clear
		// two or more epochs past the last applied one means this node
		// missed a seal. Every node seals on a seal: wipe the key now. The
		// first read after a boot is exempt -- its keys come from this boot's
		// own unseal, after any seal it missed.
		if !initial && rec.Epoch > m.applied[rec.Barrier]+1 {
			m.logger.Warn("cluster seal: sealing a barrier whose seal this node missed",
				slog.String("barrier", string(rec.Barrier)), slog.Uint64("epoch", rec.Epoch))
			if err := m.enforceMissedLocked(ctx, rec.Barrier); err != nil {
				m.logger.Error("cluster seal: enforcing a missed seal failed; retrying on the next read",
					slog.String("barrier", string(rec.Barrier)), slog.Any("error", err))
				return
			}
		}
		m.applied[rec.Barrier] = rec.Epoch
		return
	}
	if initial && rec.Barrier.Kind() == KindNode {
		m.logger.Warn("cluster seal: the node barrier is sealed cluster-wide; holding until it is cleared",
			slog.Uint64("epoch", rec.Epoch))
		m.applied[rec.Barrier] = rec.Epoch
		return
	}
	if err := m.enforceLocked(ctx, rec.Barrier, rec.Epoch); err != nil {
		m.logger.Error("cluster seal: enforcement failed; retrying on the next read",
			slog.String("barrier", string(rec.Barrier)), slog.Uint64("epoch", rec.Epoch), slog.Any("error", err))
		return
	}
	m.applied[rec.Barrier] = rec.Epoch
}

// enforceLocked seals b on this node in the fixed order: the gate is already
// closed by the published record; end streams; then the enforcer stops the
// barrier's shards and wipes its key. A node barrier first acknowledges and
// waits, bounded, for every member's acknowledgement.
func (m *Monitor) enforceLocked(ctx context.Context, b Barrier, epoch uint64) error {
	return m.sealHereLocked(ctx, b, func() {
		if b.Kind() == KindNode {
			m.awaitAcks(ctx, epoch)
		}
	})
}

// enforceMissedLocked seals b on this node for a seal it missed, already
// cleared cluster-wide: streams end and the key is wiped, with no
// acknowledgement round -- the other nodes acted on that seal long ago.
func (m *Monitor) enforceMissedLocked(ctx context.Context, b Barrier) error {
	return m.sealHereLocked(ctx, b, func() {})
}

// sealHereLocked ends b's streams, runs beforeWipe, has the enforcer stop
// b's shards and wipe its key on this node, and ends b's streams once more.
//
// The second pass closes the one window the gate does not: a missed seal is
// enforced on a CLEAR record, so no record closes the gate first -- the wipe
// does, by sealing the barrier, which every stream open also checks. A stream
// that subscribed between the first pass and the wipe passed that check and
// would outlive the seal; the pass after the wipe ends it, and any stream
// subscribing after that sees the sealed barrier and is refused.
func (m *Monitor) sealHereLocked(ctx context.Context, b Barrier, beforeWipe func()) error {
	cause := &SealedError{Barrier: b}
	ends, ok := sealEnds[b.Kind()]
	if !ok {
		return &InvalidBarrierError{Name: string(b)}
	}
	end := func() { ends.end(m.streams, b, cause) }
	end()
	beforeWipe()
	err := ends.wipe(ctx, m.cfg.Enforcer, b)
	end()
	return err
}

// sealEnd is how one barrier level ends its streams and wipes its key.
type sealEnd struct {
	end  func(*Streams, Barrier, error)
	wipe func(context.Context, Enforcer, Barrier) error
}

// sealEnds maps each barrier level to its streams and its wipe.
var sealEnds = map[Kind]sealEnd{
	KindNode: {
		end:  func(s *Streams, _ Barrier, c error) { s.EndAll(c) },
		wipe: func(ctx context.Context, e Enforcer, _ Barrier) error { return e.SealNode(ctx) },
	},
	KindTenant: {
		end:  func(s *Streams, b Barrier, c error) { s.EndTenant(b.Tenant(), c) },
		wipe: func(ctx context.Context, e Enforcer, b Barrier) error { return e.SealTenant(ctx, b.Tenant()) },
	},
	KindDatabase: {
		end: func(s *Streams, b Barrier, c error) { s.EndDatabase(b.Tenant(), b.Database(), c) },
		wipe: func(ctx context.Context, e Enforcer, b Barrier) error {
			return e.SealDatabase(ctx, b.Tenant(), b.Database())
		},
	},
}

// awaitAcks writes this node's acknowledgement of epoch and waits until every
// system-shard member has acknowledged it, or until AckTimeout.
func (m *Monitor) awaitAcks(ctx context.Context, epoch uint64) {
	if err := m.cfg.KV.Put(ctx, ackKey(m.cfg.Self), []byte(strconv.FormatUint(epoch, 10))); err != nil {
		m.logger.Warn("cluster seal: could not acknowledge the node seal", slog.Any("error", &AckError{Err: err}))
	}
	clock := m.cfg.Clock
	deadline := clock.Now().Add(m.cfg.AckTimeout)
	step := m.cfg.PollInterval / 4
	for {
		if m.allAcked(ctx, epoch) {
			return
		}
		remaining := deadline.Sub(clock.Now())
		if remaining <= 0 {
			m.logger.Warn("cluster seal: not every node acknowledged the node seal; wiping the key",
				slog.Uint64("epoch", epoch), slog.Duration("timeout", m.cfg.AckTimeout))
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-clock.After(min(step, remaining)):
		}
	}
}

func (m *Monitor) allAcked(ctx context.Context, epoch uint64) bool {
	members, err := m.cfg.Members(ctx)
	if err != nil {
		return false
	}
	acks, err := m.cfg.KV.Scan(ctx, AcksPrefix)
	if err != nil {
		return false
	}
	for _, node := range members {
		if node == m.cfg.Self {
			continue
		}
		got, perr := strconv.ParseUint(string(acks[ackKey(node)]), 10, 64)
		if perr != nil || got < epoch {
			return false
		}
	}
	return true
}

// load reads b's record: its decoded value, its exact stored bytes, and
// whether it exists.
func (m *Monitor) load(ctx context.Context, b Barrier) (Record, []byte, bool, error) {
	raw, found, err := m.cfg.KV.Get(ctx, recordKey(b))
	if err != nil || !found {
		return Record{}, nil, false, err
	}
	rec, err := decodeRecord(raw)
	if err != nil {
		return Record{}, nil, false, err
	}
	return rec, raw, true, nil
}

// write stores next over the record read as raw: create-if-absent for the
// first epoch, compare-and-swap after that.
func (m *Monitor) write(ctx context.Context, next Record, raw []byte, found bool) (bool, error) {
	enc, err := encodeRecord(next)
	if err != nil {
		return false, err
	}
	if !found {
		return m.cfg.KV.PutIfAbsent(ctx, recordKey(next.Barrier), enc)
	}
	return m.cfg.KV.CompareAndSwap(ctx, recordKey(next.Barrier), raw, enc)
}

func (m *Monitor) next(cur Record, b Barrier, sealed bool, by Principal) Record {
	return Record{
		Barrier:         b,
		Epoch:           cur.Epoch + 1,
		Sealed:          sealed,
		Principal:       by.Name,
		PrincipalTenant: by.Tenant,
		Node:            m.cfg.Node,
		At:              m.cfg.Clock.Now().UTC(),
	}
}

// Seal records a seal of b, then seals b on this node. The record is written
// first because a node key also protects this node's Raft log. A barrier
// whose record is already sealed is not written again. If the record cannot
// be confirmed, this node seals b anyway -- sealing is the safe direction --
// and returns *RecordNotConfirmedError.
func (m *Monitor) Seal(ctx context.Context, b Barrier, by Principal) (Record, error) {
	if !m.Active() {
		return Record{}, ErrInactive
	}
	if b.Kind() == KindInvalid {
		return Record{}, &InvalidBarrierError{Name: string(b)}
	}
	m.mu.Lock()
	defer m.mu.Unlock()

	cur, raw, found, err := m.load(ctx, b)
	if err != nil {
		return Record{}, m.sealUnconfirmedLocked(ctx, b, cur.Epoch+1, err)
	}
	if found && cur.Sealed {
		return cur, m.adoptSealedLocked(ctx, cur)
	}
	next := m.next(cur, b, true, by)
	ok, err := m.write(ctx, next, raw, found)
	if err != nil {
		return Record{}, m.sealUnconfirmedLocked(ctx, b, next.Epoch, err)
	}
	if !ok {
		winner, _, wfound, rerr := m.load(ctx, b)
		if rerr == nil && wfound && winner.Sealed {
			return winner, m.adoptSealedLocked(ctx, winner)
		}
		return Record{}, &SealEpochConflictError{Barrier: b}
	}
	return next, m.adoptSealedLocked(ctx, next)
}

// adoptSealedLocked publishes a sealed record and enforces it here if it has
// not been already.
func (m *Monitor) adoptSealedLocked(ctx context.Context, rec Record) error {
	m.publishOne(rec)
	if rec.Epoch <= m.applied[rec.Barrier] {
		return nil
	}
	if err := m.enforceLocked(ctx, rec.Barrier, rec.Epoch); err != nil {
		return err
	}
	m.applied[rec.Barrier] = rec.Epoch
	return nil
}

// sealUnconfirmedLocked seals b on this node although its record could not be
// confirmed. The epoch is not marked applied: if the record did commit, the
// next read enforces it again, idempotently.
func (m *Monitor) sealUnconfirmedLocked(ctx context.Context, b Barrier, epoch uint64, cause error) error {
	if err := m.enforceLocked(ctx, b, epoch); err != nil {
		m.logger.Error("cluster seal: local seal failed", slog.String("barrier", string(b)), slog.Any("error", err))
	}
	return &RecordNotConfirmedError{Barrier: b, Op: "seal", Err: cause}
}

// Clear records the end of b's cluster seal. It unseals nothing: the caller
// has already unsealed b on this node, and for the node barrier this node
// must have unsealed itself (seal.ErrSealed otherwise). A record that is
// already clear, or absent, is returned without a write.
func (m *Monitor) Clear(ctx context.Context, b Barrier, by Principal) (Record, error) {
	if !m.Active() {
		return Record{}, ErrInactive
	}
	if b.Kind() == KindInvalid {
		return Record{}, &InvalidBarrierError{Name: string(b)}
	}
	m.mu.Lock()
	defer m.mu.Unlock()

	if b.Kind() == KindNode && !m.cfg.Enforcer.NodeUnsealed() {
		return Record{}, seal.ErrSealed
	}
	cur, raw, found, err := m.load(ctx, b)
	if err != nil {
		return Record{}, &RecordNotConfirmedError{Barrier: b, Op: "clear", Err: err}
	}
	if !found || !cur.Sealed {
		if found {
			m.publishOne(cur)
		}
		return cur, nil
	}
	next := m.next(cur, b, false, by)
	ok, err := m.write(ctx, next, raw, found)
	if err != nil {
		return Record{}, &RecordNotConfirmedError{Barrier: b, Op: "clear", Err: err}
	}
	if !ok {
		winner, _, wfound, rerr := m.load(ctx, b)
		if rerr == nil && wfound && !winner.Sealed {
			m.publishOne(winner)
			m.applied[b] = max(m.applied[b], winner.Epoch)
			return winner, nil
		}
		return Record{}, &SealEpochConflictError{Barrier: b}
	}
	m.publishOne(next)
	m.applied[b] = next.Epoch
	return next, nil
}
