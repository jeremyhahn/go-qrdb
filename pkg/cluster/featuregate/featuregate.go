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
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/cluster"
	"github.com/jeremyhahn/go-qrdb/pkg/feature"
	"github.com/jeremyhahn/go-qrdb/pkg/release"
)

// GuardedWrites is protocol.OpGuarded: a write carried with compares the
// state machine evaluates at the write's own log position.
const GuardedWrites feature.Feature = "guarded-writes"

// VectorEngine is the vector command set (protocol.OpVectorNamespaceCreate
// and its siblings, 70-77): new Raft opcodes an old binary would refuse as
// unknown while its up-to-date peers applied them, diverging the group
// exactly as an unguarded OpGuarded would. Unlike GuardedWrites there is no
// older, ungated command these fall back to proposing -- a database's vector
// writes are refused with a typed feature-gate error until the feature is
// enabled for its group, rather than proposed unguarded
// (docs/vectors/engineering.md "Target: the vector engine", "Commands").
const VectorEngine feature.Feature = "vector-engine"

// ClusterShardIDs is cluster-wide shard ID allocation: every raft database
// claims its shard ID from one replicated high-water mark (shard.Claimer)
// instead of a per-node counter, and no caller may name one. It adds no Raft
// command -- the claim is the KV engine's existing compare-and-set -- but the
// claim itself waits for it: on a host that gates features, every claim, and
// so every raft database create, is refused with *NotActiveError
// until the feature is active cluster-wide (lifecycle's shard claimer).
// Activation also applies the gate's downgrade rule: once every node claims, a
// binary that allocates locally can neither start nor be admitted as a
// replica, so no node can re-issue a shard ID another holds.
const ClusterShardIDs feature.Feature = "cluster-shard-ids"

// PrefixCompaction is protocol.OpMVCCCompactPrefix -- a compaction confined
// to one database's keyspace prefix, with a compaction floor per prefix --
// and protocol.OpCompactionState, the replicated scheduled-compaction state
// (policy and samples) those compactions run from. One gate: both arrived
// together, and a replica that applies one applies the other.
const PrefixCompaction feature.Feature = "prefix-compaction"

// NodeCompaction is protocol.OpMVCCCompactNode: a compaction of the node
// keyspace -- every key outside all database namespaces.
const NodeCompaction feature.Feature = "node-compaction"

// ClusterSeal is cluster-wide sealing: every node reads the replicated seal
// records under "__sys:cluster/seal/" and seals itself when one says so (see
// pkg/clusterseal). A node that does not read them would keep serving after a
// cluster seal, so the records are written and enforced only once the feature
// is ACTIVATED -- enforcement is per node, not per Raft group, so per-group
// enablement is not the right test.
const ClusterSeal feature.Feature = "cluster-seal"

// ImportStaging is protocol.OpImportStage: a chunked import session's
// metadata and staged chunks, written directly into the state machine's
// raw keyspace (bound to the session's lease) rather than through the MVCC
// store or the KV engine, so they are structurally unreachable from the
// client data plane -- the fix for go-qrdb#576's security review finding
// that the session's earlier MVCC-backed staging was only a discovery
// guard, not storage isolation. ImportBegin refuses with
// *feature.NotActiveError until every replica of the group can apply
// the opcode.
const ImportStaging feature.Feature = "import-staging"

// RangeDelete is protocol.OpDeleteRange: the deletion of every live key in a
// range as one entry -- the batch of single deletes over them, never a
// storage range tombstone -- which keyspace reclaim uses to remove a dropped
// database's keys in one proposal per chunk. A replica that does not know
// the opcode would refuse it while its peers deleted the keys, so it is
// proposed only once every replica of the group can apply it.
const RangeDelete feature.Feature = "range-delete"

// TransactionReads is a read (protocol.OpMVCCRange) in a transaction branch,
// answered in the transaction's apply result beside its succeeded flag. A
// replica built before reads refuses the op while its peers answer it, so a
// transaction holding one is proposed only once every replica of the group
// can apply it.
const TransactionReads feature.Feature = "transaction-reads"

// AuditAppend is protocol.OpAuditAppend: a deterministic, apply-time-assigned
// append (or retention prune) to a tenant's replicated audit hash chain. A
// replica that does not implement it would apply nothing for an opcode it
// cannot decode -- UnknownOpError in both engine families -- while its peers
// commit the chain, so no replica may be sent it until every replica of the
// group can apply it. See docs/audit/architecture.md#cluster-wide-replicated-trail.
const AuditAppend feature.Feature = "audit-append"

// TimeSeriesRetention is protocol.OpTSApplyRetention: a new Raft opcode an
// old binary would refuse as unknown while its up-to-date peers applied it,
// diverging the group exactly as an unguarded OpGuarded would. The
// server's leader-driven retention scheduler (pkg/server's
// retentionScheduler) proposes it only once every replica of the group can
// apply it (docs/time-series/engineering.md "Retention enforcement").
const TimeSeriesRetention feature.Feature = "timeseries-retention"

// LeaseCheckpoint is protocol.OpLeaseCheckpoint: the leader's replicated
// record of how much TTL each long-lived lease had left, which a new leader and
// a restarted replica count the lease's deadline from. A replica that does not
// know the opcode would apply nothing while its peers record the checkpoint,
// so a leader checkpoints no lease until the feature is enabled for the group;
// until then a new leader grants every lease a full TTL, as it did before
// (docs/leases/architecture.md "Keep-alive").
const LeaseCheckpoint feature.Feature = "lease-checkpoint"

// PerNodeBarrierKeys is per-node barrier initialisation: an isolated tenant's
// barrier is initialised by every serving node from the barrier owner's signed
// init statement under "__sys:cluster/barrier/init/" (see pkg/barrierinit),
// each node holding its own key rather than one carried between them. It adds
// no Raft command -- the statement rides the existing guarded write -- but a
// node that predates it neither reads the statement nor initialises its own
// key, so it would never serve the tenant. Like ClusterSeal it is enforced per
// node, so it is judged by ACTIVATION: until then an isolated tenant create is
// refused (docs/multi-tenancy/architecture.md "Feature gate and upgrade").
const PerNodeBarrierKeys feature.Feature = "per-node-barrier-keys"

// VerifiableLog is protocol.OpVerifiableLog: a deterministic, apply-time
// create, append, checkpoint or body expiry on a named verifiable log. A
// replica that does not implement it answers the opcode with UnknownOpError
// while its peers grow the log, so no replica may be sent it until every
// replica of the group can apply it. Its apply carries no build tag, so every
// binary that carries it advertises it. See
// docs/decisions/verifiable-log.md, section 3.
const VerifiableLog feature.Feature = "verifiable-log"

// RollingMigration is the per-node step record of a cluster encryption
// migration (clustermig.ClusterMigration.Steps): the coordinator hands the
// recycle to one owner at a time, and each owner recycles only on its turn.
// A node that predates it ignores the steps and recycles at once, which with
// a peer down loses quorum, so no request carries steps until every node
// can honour them. Like ClusterSeal it is enforced per node by each node's
// reconciler, so it is judged by ACTIVATION.
const RollingMigration feature.Feature = "rolling-migration"

// TenantUndo is the undone tenant tombstone and its per-node cleanup
// acknowledgements: an undone tenant create leaves a tombstone in an undone
// state, every serving node records under "__sys:tenants-undone-ack/" that it
// has cleaned up its own leftovers, and a create reuses the undone ID only
// once every serving node has. A node that predates it never writes an
// acknowledgement, so a reuse would wait on it forever; it is judged by
// ACTIVATION, and until then an undone create retires its ID as a deletion
// does (docs/multi-tenancy/architecture.md).
const TenantUndo feature.Feature = "tenant-undo"

// VerifiableLogKeys is the head-signing-key wire of protocol.OpVerifiableLog:
// the Create body that frames the descriptor and the log's sealed key record,
// and the RotateKey sub-type. A replica that implements only VerifiableLog
// refuses both, and its peers would create or re-key the log without it, so
// neither is proposed until every replica of the group can apply them. Its
// apply carries no build tag, so every binary that carries it advertises it.
// See docs/databases/architecture.md.
const VerifiableLogKeys feature.Feature = "verifiable-log-keys"

// VerifiableLogAudit is the audit instance of protocol.OpVerifiableLog: the
// trusted AuditAppend sub-type that chains an audit event into the tenant's
// log instance "system/audit". A replica that predates it refuses the
// sub-type while its peers advance the instance's chain, and the replicas
// diverge, so the replicated audit logger keeps
// proposing OpAuditAppend until every replica of the group can apply it. Its
// apply carries no build tag, so every binary that carries it advertises it.
// See docs/databases/architecture.md.
const VerifiableLogAudit feature.Feature = "verifiable-log-audit"

// PendingUserTTL is the pending-user expiry: User.PendingExpiresAt and the
// reaper's ExpiryClaim on the user record, and the "__reap/" markers the
// pending-user reaper writes. A binary without it decodes a user record
// without those fields and drops them on its next update of the record, so
// a TTL-bearing create is refused (feature.NotActiveError) and the reaper
// does not sweep until the feature is active cluster-wide.
const PendingUserTTL feature.Feature = "pending-user-ttl"

// VectorManhattan is the Manhattan (L1) distance metric on a FLAT or IVF
// vector namespace: record metric value 3 (pkg/vector DistanceManhattan),
// which a binary from before it refuses as an unknown metric while its
// up-to-date peers accept it. A Manhattan FLAT or IVF create is refused with
// a typed failed-precondition error until every replica of the group
// advertises it (docs/vectors/architecture.md).
const VectorManhattan feature.Feature = "vector-manhattan"

// CommandCaps is the admission cap a capped command carries: the
// timeseries.max_metrics trailer on OpTSWrite, OpTSWritePoint and
// OpTSWriteBatch, and the index.max_indexes field of OpIndexCreate. Every
// replica enforces the cap the command carries, so a binary from before it,
// which ignores the trailer, would admit a write its up-to-date peers refuse
// and diverge the group. Until every replica of the group advertises it the
// commands are proposed uncapped, exactly as an older binary encodes them
// (docs/time-series/engineering.md, docs/secondary-indexes/engineering.md).
const CommandCaps feature.Feature = "command-caps"

// LockCreateRevisionToken is the distributed lock's fencing token read from
// the lock key's MVCC create revision rather than from the fencing_token
// field of its stored value. A binary from before it reads the stored field,
// so until every replica of the group advertises it acquisition and reentry
// keep writing the field, with the same value (docs/distributed-locks/
// engineering.md).
const LockCreateRevisionToken feature.Feature = "lock-create-revision-token"

// supported lists the features this binary implements. A new feature is
// added here, and to nothing else in this package.
var supported = []feature.Feature{GuardedWrites, ClusterShardIDs, PrefixCompaction, NodeCompaction, ClusterSeal, ImportStaging, RangeDelete, AuditAppend, VectorEngine, TimeSeriesRetention, LeaseCheckpoint, PerNodeBarrierKeys, VerifiableLog, RollingMigration, TenantUndo, VerifiableLogKeys, VectorManhattan, CommandCaps, LockCreateRevisionToken, TransactionReads, VerifiableLogAudit, PendingUserTTL}

// Supported returns the features this binary implements.
func Supported() []feature.Feature {
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

	// ReleasePrefix is the reserved key prefix of recorded releases: one
	// replicated key "{ReleasePrefix}{replicaID}/{release}" per system-shard
	// member, valued with its raft address. A member's gate writes its own
	// key once it is committed, so its release outlives its advertisement
	// lease and a joiner is still compared against an offline member.
	ReleasePrefix = "__sys:cluster/features/releases/"

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
// and reads activations through. ListAll is a linearizable read; ListStale
// reads what this node's replica has already applied, without a leader.
// Neither may filter reserved keys.
type ActivationKV interface {
	PutIfAbsent(ctx context.Context, key string, value []byte) (bool, error)
	ListAll(ctx context.Context, prefix string) ([]string, error)
	ListStale(ctx context.Context, prefix string) ([]string, error)
	Delete(ctx context.Context, key string) error
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
	activated map[feature.Feature]*atomic.Bool

	// view is the current advertisement snapshot across every store the
	// gate follows, replaced whole.
	mu sync.Mutex // serializes view replacement
	// proposeMu serializes bound system-shard additions on this node from
	// the CheckConfigBinding recheck through the add's commit or refusal
	// (ProposeBound), so no other add through this node commits between a
	// recheck and its append.
	proposeMu sync.Mutex
	view      atomic.Pointer[map[viewKey]cluster.Member]

	// releases maps a system-shard replica ID to the releases recorded
	// under ReleasePrefix for it, replaced whole by recordRelease. It
	// stands in for a committed member whose advertisement has lapsed.
	releases atomic.Pointer[map[uint64][]string]

	// stores are the activation KV and advertisement stores Start was
	// given; admission reads them afresh rather than through view and
	// releases (see admitRelease). Nil before Start.
	stores atomic.Pointer[admissionStores]

	// refusal is the activated feature this binary lacks, once
	// CheckActivated has found one; see Refusal.
	refusal atomic.Pointer[UnsupportedFeatureError]

	// releaseRefusal is the release gap Start refused a restart with; see
	// Refusal.
	releaseRefusal atomic.Pointer[release.GapError]

	// loaded is set once CheckActivated has read the activations without
	// finding one this binary lacks; see ActivationsLoaded.
	loaded atomic.Bool

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
		activated:   make(map[feature.Feature]*atomic.Bool, len(supported)),
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
	noReleases := map[uint64][]string{}
	g.releases.Store(&noReleases)
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
func (g *Gate) Enabled(_ uint64, feature feature.Feature) bool {
	return g.Activated(feature)
}

// decide reads shardID's committed membership and reports whether every
// replica that applies its log -- and, when withWitnesses, every witness --
// is on a node that supports feature.
func (g *Gate) decide(shardID uint64, feature feature.Feature, withWitnesses bool) bool {
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
func (g *Gate) supportingAddresses(feature feature.Feature, system GroupMembership) map[string]struct{} {
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
func (g *Gate) Supports(raftAddr string, feature feature.Feature) bool {
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
	_, err := g.AdmitReplicaAt(shardID, raftAddr)
	return err
}

// ConfigBinding is the system-shard configuration an admission verdict holds
// for. Required says the add must land on exactly that configuration;
// ConfigChangeID is its change ID, which is 0 for a bootstrapped membership
// before its first change -- a configuration like any other, not "unbound".
//
// A member restart changes its release without changing the configuration,
// so the change ID alone cannot tell that the verdict no longer holds.
// ReleaseDigest is the release revision the verdict compared: a canonical
// digest of the candidate's announced releases and of every compared
// committed member's releases (members in replica-ID order, each member's
// releases sorted). Candidate is the admitted Raft address, for
// CheckConfigBinding to recompute that digest. admitRelease sets both on
// every Required binding it returns.
type ConfigBinding struct {
	ConfigChangeID uint64
	Required       bool
	Candidate      string
	ReleaseDigest  string
}

// StaleConfigurationError is CheckConfigBinding's refusal: the system-shard
// configuration an admission verdict was bound to is no longer current.
type StaleConfigurationError struct {
	ShardID  uint64
	Admitted uint64
	Current  uint64
	// ReleasesChanged says the configuration is unchanged but a compared
	// member's release changed since admission (a restart on another
	// release changes no configuration).
	ReleasesChanged bool
}

func (e *StaleConfigurationError) Error() string {
	if e.ReleasesChanged {
		return fmt.Sprintf("feature gate: shard %d member releases changed since admission (configuration change %d)",
			e.ShardID, e.Current)
	}
	return fmt.Sprintf("feature gate: shard %d configuration changed since admission (admitted at change %d, now %d)",
		e.ShardID, e.Admitted, e.Current)
}

// CheckConfigBinding refuses with *StaleConfigurationError when b is
// required and the system shard's configuration change ID is no longer the
// one it was admitted against, or when the releases the verdict compared
// changed since (ReleasesChanged: a member restarted on another release,
// which changes no configuration). For the latter it reads the
// advertisements and recorded releases afresh, bounded by admitReadTimeout,
// and refuses when it cannot. The change-ID comparison is the one
// go-quicraft cannot make for change ID 0 (its configChangeIndex 0 skips the
// check), and go-quicraft makes no release comparison at all, so a caller
// runs it immediately before proposing a bound add; on the refusal it admits
// again. An unbound b, or another shard, always passes.
//
// A caller proposes a bound add through ProposeBound, which holds this node's
// lock from this check until the add commits or is refused, so no other add
// through this node commits in between. Two residuals wait for go-quicraft
// to offer a proposal-time configuration check (go-quicraft 1192,
// docs/plans/1628.md): a leadership change while an add is in flight (the
// new leader's adds do not share this node's lock), and a member restarted
// onto another release whose Gate.Start restart check ran before the add
// committed.
func (g *Gate) CheckConfigBinding(shardID uint64, b ConfigBinding) error {
	if !b.Required || shardID != g.systemShard {
		return nil
	}
	system, err := g.members.GroupMembership(g.systemShard)
	if err != nil {
		return err
	}
	if system.ConfigChangeID != b.ConfigChangeID {
		return &StaleConfigurationError{ShardID: shardID, Admitted: b.ConfigChangeID, Current: system.ConfigChangeID}
	}
	if b.ReleaseDigest == "" {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), admitReadTimeout)
	defer cancel()
	view, err := g.admissionView(ctx)
	if err != nil {
		return err
	}
	recorded, err := g.admissionReleases(ctx)
	if err != nil {
		return err
	}
	members := comparedMembers(view, recorded, system, g.self.ReplicaID, b.Candidate)
	if releaseDigest(candidateReleases(view, b.Candidate), members) != b.ReleaseDigest {
		return &StaleConfigurationError{ShardID: shardID, Admitted: b.ConfigChangeID, Current: system.ConfigChangeID, ReleasesChanged: true}
	}
	return nil
}

// ProposeBound runs propose, which proposes the add bound to b and returns
// only once that add commits or is refused. When b is Required and shardID is
// the system shard, it holds this node's proposal lock from CheckConfigBinding
// through propose's return: go-quicraft accepts a configuration change only on
// the shard leader's host and refuses a second one while one is uncommitted,
// so no other add through this node commits between the recheck and this
// add's append. The next add then rechecks against the committed
// configuration and is refused as stale. Any other binding runs propose with
// no lock and no check.
func (g *Gate) ProposeBound(shardID uint64, b ConfigBinding, propose func() error) error {
	if !b.Required || shardID != g.systemShard {
		return propose()
	}
	g.proposeMu.Lock()
	defer g.proposeMu.Unlock()
	if err := g.CheckConfigBinding(shardID, b); err != nil {
		return err
	}
	return propose()
}

// AdmitReplicaAt is AdmitReplica that also returns the system-shard
// configuration the release check compared against, for the caller to bind
// the add to: it passes ConfigChangeID as the add's configChangeIndex, so
// go-quicraft refuses the add with ErrStaleConfigChange when another
// membership change committed after admission, and, because go-quicraft
// skips that comparison for index 0, it runs CheckConfigBinding itself
// before proposing, which also refuses when a compared member's release
// changed since admission (ReleaseDigest); on any of these refusals it
// admits again against the new membership and releases. Without that binding two concurrent adds of non-adjacent
// releases (0.58 and 0.60 into an all-0.59 cluster) both pass, since neither
// is yet a committed member the other must stay adjacent to.
//
// The binding is not Required when shardID is not the system shard (release
// admission is about system-shard membership; another group's configuration
// change index is a different counter) or when no membership was compared
// (the node itself, or a candidate that advertises no release).
func (g *Gate) AdmitReplicaAt(shardID uint64, raftAddr string) (ConfigBinding, error) {
	binding, err := g.admitRelease(shardID, raftAddr)
	if err != nil {
		return ConfigBinding{}, err
	}
	for _, f := range supported {
		if !g.Activated(f) && !g.decide(shardID, f, false) {
			continue
		}
		if !g.Supports(raftAddr, f) {
			return ConfigBinding{}, &UnsupportedReplicaError{ShardID: shardID, Address: raftAddr, Feature: f}
		}
	}
	if shardID != g.systemShard {
		return ConfigBinding{}, nil
	}
	return binding, nil
}

// admitRelease refuses the node at raftAddr when its live advertisement
// announces a release that is not adjacent (pkg/release.Adjacent) to this
// binary's release or to the release of EVERY other member in the view:
// admitting a 0.60 replica through a 0.59 member while a 0.58 member is
// still in the cluster would leave two non-adjacent releases side by side.
// It returns the *release.GapError with Channel "raft-membership" whose
// Local is the release of the first member the candidate is not adjacent
// to (this node first). A record with an empty ReleaseVersion predates
// release announcement and is judged by its features alone, both as the
// candidate and as a member compared against; this node itself is never
// refused.
func (g *Gate) admitRelease(shardID uint64, raftAddr string) (ConfigBinding, error) {
	self, selfErr := g.AdvertisedSelf()
	if selfErr == nil && self.Address == raftAddr {
		return ConfigBinding{}, nil
	}
	// A restart changes a member's release without a configuration change,
	// so the cached view, refilled only as the watch and refresh catch up,
	// can hold a release the member no longer runs. Admission reads the
	// advertisements and recorded releases afresh, and refuses when it
	// cannot.
	ctx, cancel := context.WithTimeout(context.Background(), admitReadTimeout)
	defer cancel()
	view, err := g.admissionView(ctx)
	if err != nil {
		return ConfigBinding{}, err
	}
	// A candidate advertised from several sources may announce several
	// releases: each must be adjacent, or the verdict depends on which
	// record map order yields.
	refuse := func(err error, candidate, member, memberRelease string) (ConfigBinding, error) {
		g.logger.Warn("feature gate: refusing replica on a release gap",
			"address", raftAddr,
			"peer_release", candidate,
			"member", member,
			"member_release", memberRelease)
		return ConfigBinding{}, err
	}
	// No live record at all is not the same as a live record that predates
	// announcement: the feature check that follows reads the watch-fed
	// cache, which can still hold an advertisement the store has expired,
	// so a system-shard candidate with nothing live is refused here rather
	// than judged on stale features. A non-system shard's candidates are
	// committed members, judged by features alone.
	if shardID == g.systemShard && !hasLiveRecord(view, raftAddr) {
		return refuse(&UnannouncedCandidateError{ShardID: shardID, Address: raftAddr}, "", "self", release.Version())
	}
	candidates := candidateReleases(view, raftAddr)
	if len(candidates) == 0 {
		return ConfigBinding{}, nil
	}
	for _, candidate := range candidates {
		if err := release.Check(releaseChannel, candidate); err != nil {
			return refuse(err, candidate, "self", release.Version())
		}
	}
	// Compare only with committed system-shard members: a pre-membership
	// advertisement is another candidate, not a member it must stay
	// adjacent to. A member's releases are those its live advertisements
	// announce (every distinct one, when several sources disagree); one
	// whose advertisements all predate announcement is skipped; an offline
	// member's are the releases it recorded, and with neither the candidate
	// is refused. The verdict holds only for this configuration and these
	// releases, so its change ID and release digest are returned to bind
	// the add to them (see AdmitReplicaAt).
	system, err := g.members.GroupMembership(g.systemShard)
	if err != nil {
		return ConfigBinding{}, err
	}
	recorded, err := g.admissionReleases(ctx)
	if err != nil {
		return ConfigBinding{}, err
	}
	members := comparedMembers(view, recorded, system, g.self.ReplicaID, raftAddr)
	for _, m := range members {
		if !m.live && len(m.rels) == 0 {
			return refuse(&UnknownMemberReleaseError{ShardID: g.systemShard, ReplicaID: m.id, Address: m.addr}, candidates[0], m.addr, "")
		}
		for _, rel := range m.rels {
			for _, candidate := range candidates {
				if !releasesAdjacent(rel, candidate) {
					return refuse(&release.GapError{Channel: releaseChannel, Local: rel, Peer: candidate}, candidate, m.addr, rel)
				}
			}
		}
	}
	// Required even at ConfigChangeID 0: a bootstrapped membership before
	// its first change is a checked configuration, not the absence of one.
	return ConfigBinding{
		ConfigChangeID: system.ConfigChangeID,
		Required:       true,
		Candidate:      raftAddr,
		ReleaseDigest:  releaseDigest(candidates, members),
	}, nil
}

// admitReadTimeout bounds admission's fresh read of the advertisements and
// recorded releases.
const admitReadTimeout = 10 * time.Second

// admissionStores are the stores a started gate reads at admission.
type admissionStores struct {
	kv      ActivationKV
	adverts []cluster.MembershipStore
}

// admissionView reads every advertisement store afresh, keyed as view is.
// Before Start it has no stores and returns the cached view.
func (g *Gate) admissionView(ctx context.Context) (map[viewKey]cluster.Member, error) {
	stores := g.stores.Load()
	if stores == nil {
		return *g.view.Load(), nil
	}
	view := make(map[viewKey]cluster.Member)
	for i, store := range stores.adverts {
		members, err := store.ListMembers(ctx)
		if err != nil {
			return nil, &StoreError{Op: "list advertisements", Err: err}
		}
		for _, m := range members {
			view[viewKey{source: i, nodeID: m.NodeID}] = m
		}
	}
	return view, nil
}

// admissionReleases reads the recorded releases afresh. Before Start it
// has no store and returns the cached records.
func (g *Gate) admissionReleases(ctx context.Context) (map[uint64][]string, error) {
	stores := g.stores.Load()
	if stores == nil {
		return *g.releases.Load(), nil
	}
	keys, err := listReleases(ctx, stores.kv)
	if err != nil {
		return nil, err
	}
	return parseReleases(keys), nil
}

// committedMember is one replica or witness of a committed membership.
type committedMember struct {
	id   uint64
	addr string
}

// committedMembers lists the replicas, then the witnesses, of system, each
// in replica-ID order, so a refusal names the same member every time.
func committedMembers(system GroupMembership) []committedMember {
	members := make([]committedMember, 0, len(system.Replicas)+len(system.Witnesses))
	for _, group := range []map[uint64]string{system.Replicas, system.Witnesses} {
		for _, id := range slices.Sorted(maps.Keys(group)) {
			members = append(members, committedMember{id: id, addr: group[id]})
		}
	}
	return members
}

// liveRecords returns every advertisement in view from the member with
// replica ID id at addr, matched as supportingAddresses matches, in viewKey
// source order: when sources disagree, a caller weighs every record, not an
// arbitrary one.
func liveRecords(view map[viewKey]cluster.Member, id uint64, addr string) []cluster.Member {
	var keys []viewKey
	for k, rec := range view {
		if k.nodeID == id && rec.Address == addr {
			keys = append(keys, k)
		}
	}
	slices.SortFunc(keys, func(a, b viewKey) int { return cmp.Compare(a.source, b.source) })
	recs := make([]cluster.Member, len(keys))
	for i, k := range keys {
		recs[i] = view[k]
	}
	return recs
}

// announcedReleases returns the distinct non-empty releases recs announce,
// sorted: a record that predates announcement contributes none.
func announcedReleases(recs []cluster.Member) []string {
	var rels []string
	for _, rec := range recs {
		if rec.ReleaseVersion != "" {
			rels = append(rels, rec.ReleaseVersion)
		}
	}
	slices.Sort(rels)
	return slices.Compact(rels)
}

// hasLiveRecord reports whether view holds any advertisement at raftAddr.
func hasLiveRecord(view map[viewKey]cluster.Member, raftAddr string) bool {
	for _, rec := range view {
		if rec.Address == raftAddr {
			return true
		}
	}
	return false
}

// candidateReleases returns every distinct release the live records at
// raftAddr announce, sorted.
func candidateReleases(view map[viewKey]cluster.Member, raftAddr string) []string {
	var recs []cluster.Member
	for _, rec := range view {
		if rec.Address == raftAddr {
			recs = append(recs, rec)
		}
	}
	return announcedReleases(recs)
}

// comparedMember is a committed system-shard member admission compares a
// candidate with, and the releases it compares: those its live records
// announce (live; none when all predate announcement), else those it
// recorded.
type comparedMember struct {
	committedMember
	rels []string
	live bool
}

// comparedMembers returns, in replica-ID order, every committed member of
// system other than self and the candidate at raftAddr, with its releases.
func comparedMembers(view map[viewKey]cluster.Member, recorded map[uint64][]string, system GroupMembership, self uint64, raftAddr string) []comparedMember {
	var out []comparedMember
	for _, m := range committedMembers(system) {
		if m.id == self || m.addr == raftAddr {
			continue
		}
		if recs := liveRecords(view, m.id, m.addr); len(recs) > 0 {
			out = append(out, comparedMember{committedMember: m, rels: announcedReleases(recs), live: true})
			continue
		}
		rels := slices.Clone(recorded[m.id])
		slices.Sort(rels)
		out = append(out, comparedMember{committedMember: m, rels: slices.Compact(rels)})
	}
	slices.SortStableFunc(out, func(a, b comparedMember) int { return cmp.Compare(a.id, b.id) })
	return out
}

// releaseDigest is the canonical digest of the releases an admission
// compared: the candidate's, then each member's in replica-ID order. Any
// release a member gains or loses (a restart on another release) changes it.
func releaseDigest(candidates []string, members []comparedMember) string {
	var b strings.Builder
	b.WriteString("candidate=")
	b.WriteString(strings.Join(candidates, ","))
	for _, m := range members {
		fmt.Fprintf(&b, ";%d=%s", m.id, strings.Join(m.rels, ","))
	}
	sum := sha256.Sum256([]byte(b.String()))
	return hex.EncodeToString(sum[:])
}

// releasesAdjacent reports whether two announced releases may run side by
// side: a development build accepts any peer, as release.Check does.
func releasesAdjacent(a, b string) bool {
	return a == release.Dev || b == release.Dev || release.Adjacent(a, b)
}

// releaseChannel names the cluster-membership channel in a
// *release.GapError.
const releaseChannel = "raft-membership"

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

// recordRelease records this node's release under ReleasePrefix once the
// system shard has committed it, removes any release an earlier binary
// recorded for it, and reloads the recorded releases of every member. It
// writes only when the record is missing or stale.
func (g *Gate) recordRelease(ctx context.Context, kv ActivationKV) error {
	self, err := g.AdvertisedSelf()
	if err != nil {
		return err
	}
	own := ReleasePrefix + strconv.FormatUint(self.ReplicaID, 10) + "/"
	key := own + release.Version()
	keys, err := listReleases(ctx, kv)
	if err != nil {
		return err
	}
	// The running release is persisted before any earlier record of this
	// member is removed: a crash or a failed write between the two leaves
	// both on record, never neither, so the history a later restart checks
	// is never lost.
	recorded, stale := slices.Contains(keys, key), false
	if !recorded {
		if _, err := kv.PutIfAbsent(ctx, key, []byte(self.Address)); err != nil {
			return &StoreError{Op: "record release", Err: err}
		}
	}
	for _, k := range keys {
		if k == key || !strings.HasPrefix(k, own) {
			continue
		}
		stale = true
		if err := kv.Delete(ctx, k); err != nil {
			return &StoreError{Op: "delete release", Err: err}
		}
	}
	if !recorded || stale {
		if keys, err = listReleases(ctx, kv); err != nil {
			return err
		}
	}
	releases := parseReleases(keys)
	g.releases.Store(&releases)
	return nil
}

// parseReleases maps each replica ID to the releases recorded for it by
// release record keys "{ReleasePrefix}{replicaID}/{release}".
// listReleases lists the release history from applied state, or through the
// leader on a replica without readable applied state (a witness).
func listReleases(ctx context.Context, kv ActivationKV) ([]string, error) {
	keys, err := kv.ListStale(ctx, ReleasePrefix)
	if err != nil {
		if keys, err = kv.ListAll(ctx, ReleasePrefix); err != nil {
			return nil, &StoreError{Op: "list releases", Err: err}
		}
	}
	return keys, nil
}

func parseReleases(keys []string) map[uint64][]string {
	releases := make(map[uint64][]string, len(keys))
	for _, k := range keys {
		idText, rel, ok := strings.Cut(strings.TrimPrefix(k, ReleasePrefix), "/")
		id, err := strconv.ParseUint(idText, 10, 64)
		if !ok || err != nil || rel == "" || !strings.HasPrefix(k, ReleasePrefix) {
			continue
		}
		releases[id] = append(releases[id], rel)
	}
	return releases
}

// checkRestartRelease refuses, with the *release.GapError admission
// returns (Channel "raft-membership", Local this binary's release, Peer the
// member's), to start this node when it is already a committed system-shard
// member and its binary's release is not adjacent to the release of every
// committed member, or to the release this node itself last recorded.
// Admission judges a node only when it is added; a member restarting its
// data directory on a new binary -- 0.58 to 0.60 in an all-0.59 cluster, an
// upgrade that skips a release -- is judged here, before it records its
// release or serves. Another member's release is its live advertisement's,
// else the releases it recorded under ReleasePrefix; this node's is every
// release it recorded or still advertises. A member with neither, or whose record
// predates release announcement, is skipped: a cold restart of a cluster
// whose members have not recorded a release yet must still come up. A node
// that is not yet a member is left to admission. A membership or recorded
// release history this node cannot read refuses the start (a *StoreError
// for the history) rather than reading as no history: Start is retried, and
// a skipped-release restart must not pass on a transient read failure and
// then replace the release it last recorded.
func (g *Gate) checkRestartRelease(ctx context.Context, kv ActivationKV) error {
	if _, err := g.AdvertisedSelf(); err != nil {
		var notMember *NotAMemberError
		if errors.As(err, &notMember) {
			return nil
		}
		return err
	}
	system, err := g.members.GroupMembership(g.systemShard)
	if err != nil {
		return err
	}
	// A replica without readable applied state (a witness) reads the
	// history through the leader instead.
	keys, err := listReleases(ctx, kv)
	if err != nil {
		return err
	}
	recorded := parseReleases(keys)
	view := *g.view.Load()
	own := release.Version()
	for _, m := range committedMembers(system) {
		rels := recorded[m.id]
		// Every release any live record announces is checked: sources
		// that disagree must not be settled by map order.
		recs := liveRecords(view, m.id, m.addr)
		live := announcedReleases(recs)
		if m.id == g.self.ReplicaID {
			// This node's own record is the release its data directory
			// last ran: the upgrade itself must be to an adjacent release.
			rels = append(slices.Clone(rels), live...)
		} else if len(recs) > 0 {
			if len(live) == 0 {
				continue
			}
			rels = live
		}
		for _, rel := range rels {
			if !releasesAdjacent(own, rel) {
				g.logger.Error("feature gate: refusing to start on a release gap to a member",
					"release", own,
					"member", m.addr,
					"member_release", rel)
				return &release.GapError{Channel: releaseChannel, Local: own, Peer: rel}
			}
		}
	}
	return nil
}

// Activated reports whether feature has been activated cluster-wide.
func (g *Gate) Activated(feature feature.Feature) bool {
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
	if err := g.adopt(features); err != nil {
		return err
	}
	g.loaded.Store(true)
	return nil
}

// adoptApplied marks enabled every activated feature whose record this
// node's replica has already applied. It needs no leader and no quorum, so a
// node learns of an activation as soon as it applies the record -- in
// particular before it takes over a shard's leadership -- rather than at the
// next linearizable refresh that gets through. A local read can lag the
// cluster but never run ahead of it, and activation is permanent, so what it
// finds is activated. It leaves ActivationsLoaded alone: what it does not
// find may yet be activated.
func (g *Gate) adoptApplied(ctx context.Context, kv ActivationKV) {
	keys, err := kv.ListStale(ctx, ActivationPrefix)
	if err != nil {
		g.logger.Debug("feature gate: applied activations unreadable; relying on refresh", "error", err)
		return
	}
	if err := g.adopt(activationFeatures(keys)); err != nil {
		g.logger.Error("feature gate: activation refresh failed", "error", err)
	}
}

// adopt marks every feature in features enabled, and refuses with
// *UnsupportedFeatureError -- kept for Refusal -- at one this binary does
// not implement. It logs each feature it is first to enable, so every node
// logs each activation exactly once whichever path learns of it: activate,
// a linearizable refresh, or a replica's applied record read while its
// leader is unreachable.
func (g *Gate) adopt(features []feature.Feature) error {
	for _, f := range features {
		flag, ok := g.activated[f]
		if !ok {
			refusal := &UnsupportedFeatureError{Feature: f}
			g.refusal.Store(refusal)
			return refusal
		}
		if flag.CompareAndSwap(false, true) {
			g.logger.Info("feature gate: feature activation observed", "feature", string(f))
		}
	}
	return nil
}

// ActivationsLoaded reports whether CheckActivated has read the cluster's
// activations, so that Activated's false means "not activated" rather than
// "not read yet". A read that found a feature this binary lacks leaves it
// false: this node must not serve at all.
func (g *Gate) ActivationsLoaded() bool {
	return g.loaded.Load()
}

// Refusal returns the *UnsupportedFeatureError CheckActivated found -- the
// cluster has activated a feature this binary lacks -- or the
// *release.GapError Start refused a restart with -- this committed member's
// binary is not adjacent to a member's release or the release it last ran
// -- or nil. Either way this node must not serve: a composer that cannot
// refuse to start reports it as a degraded node.
func (g *Gate) Refusal() error {
	if r := g.refusal.Load(); r != nil {
		return r
	}
	if r := g.releaseRefusal.Load(); r != nil {
		return r
	}
	return nil
}

// ActivatedFeatures lists the features the cluster has activated.
func ActivatedFeatures(ctx context.Context, kv ActivationKV) ([]feature.Feature, error) {
	keys, err := kv.ListAll(ctx, ActivationPrefix)
	if err != nil {
		return nil, &StoreError{Op: "list activations", Err: err}
	}
	return activationFeatures(keys), nil
}

// activationFeatures returns the features named by activation record keys.
func activationFeatures(keys []string) []feature.Feature {
	features := make([]feature.Feature, 0, len(keys))
	for _, k := range keys {
		if name := strings.TrimPrefix(k, ActivationPrefix); name != "" && name != k {
			features = append(features, feature.Feature(name))
		}
	}
	return features
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
	Feature   feature.Feature `json:"feature"`
	ReplicaID uint64          `json:"activated_by"`
}

// EnforceRelease refuses -- with *release.GapError, kept for Refusal --
// when this node is a committed member restarting on a release not
// adjacent to a member's release or the release it last ran
// (checkRestartRelease), and otherwise records this binary's release. Start
// runs it after seeding the advertisement view; a node whose state machine
// has no leases, and so cannot advertise or Start, runs it on its own: the
// release history needs no lease, and every node start enforces it. A failed
// record fails the check (a *StoreError): the node does not start on a
// release the history does not hold.
func (g *Gate) EnforceRelease(ctx context.Context, kv ActivationKV) error {
	if kv == nil {
		return &ConfigError{Field: "store"}
	}
	if err := g.checkRestartRelease(ctx, kv); err != nil {
		var gap *release.GapError
		if errors.As(err, &gap) {
			g.releaseRefusal.Store(gap)
		}
		return err
	}
	// The running release must be on record before the node reports the
	// check passed: an unrecorded release would let the next binary skip
	// the gap check against it. A joiner that is not yet a member of the
	// system shard has no release to record: admission judges it, and the
	// refresh records its release once it is a member. Every other record
	// failure refuses the start.
	if err := g.recordRelease(ctx, kv); err != nil {
		var notMember *NotAMemberError
		if errors.As(err, &notMember) {
			return nil
		}
		return err
	}
	return nil
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
// activated a feature this binary lacks, or when this node is a committed
// member restarting on a release not adjacent to another member's
// (checkRestartRelease).
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
	if err := g.EnforceRelease(ctx, kv); err != nil {
		cancel()
		return err
	}
	g.activate(ctx, kv)
	g.stores.Store(&admissionStores{kv: kv, adverts: slices.Clone(adverts)})

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

// run applies advertisement events and refreshes on a timer. Each pass first
// adopts the activations this replica has applied (adoptApplied): the
// linearizable reads after it wait on a leader, and can take many seconds on
// a slow disk or across a leader change, while the local read does not.
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
			g.adoptApplied(ctx, kv)
			g.activate(ctx, kv)
		case <-ticker.C:
			g.adoptApplied(ctx, kv)
			for i, store := range adverts {
				if err := g.reseed(ctx, i, store); err != nil {
					g.logger.Warn("feature gate: advertisement refresh failed", "error", err)
				}
			}
			if err := g.recordRelease(ctx, kv); err != nil {
				g.logger.Warn("feature gate: release record failed; retrying on refresh", "error", err)
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
		if !ok || am.Address != bm.Address || am.ReleaseVersion != bm.ReleaseVersion || !slices.Equal(am.Features, bm.Features) {
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
		if !g.activated[f].CompareAndSwap(false, true) {
			continue // adopted meanwhile, and logged there
		}
		if stored {
			g.logger.Info("feature gate: feature activated cluster-wide", "feature", string(f))
		} else {
			g.logger.Info("feature gate: feature activation observed", "feature", string(f))
		}
	}
}
