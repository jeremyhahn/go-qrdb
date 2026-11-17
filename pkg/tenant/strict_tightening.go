// Copyright (c) 2025 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//	http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package tenant

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
)

// ---------------------------------------------------------------------------
// Tightening to strict over logs under a node key
//
// A strict tenant never has a log under a node key: every one of its raft
// databases is on a dedicated, tenant-keyed WAL. Database create refuses a
// strict tenant's database without one, but a tenant that tightens to strict
// (its own policy, or the cluster floor) keeps the databases it created
// before. So the tightening itself is refused while any raft database of an
// affected tenant has its log on the shared, node-keyed WAL.
//
// A tightening and a concurrent create are serialized by a marker, as in
// Dekker's algorithm: the tightening writes its marker, then lists the
// databases; a create of a database on the shared WAL writes its catalog
// record, then reads the markers and, after them, the policy (a tightening
// removes its marker only after committing, so a create that misses the
// marker reads the committed policy). Whichever reads second sees
// the other's write, so either the tightening finds the new database and is
// refused, or the create finds the marker (or the strict policy the
// tightening committed) and rolls itself back. Both may refuse; neither can
// leave a strict tenant with a node-keyed log.
// ---------------------------------------------------------------------------

// tighteningPrefix keys the markers of tightenings in flight:
// tighteningPrefix + tenant + "/" + nonce for a tenant's own policy,
// clusterTighteningPrefix + nonce for the cluster floor. Each tightening
// writes a key of its own, so one ending never clears another's.
const (
	tighteningPrefix        = "__sys:isolation-tightening/"
	clusterTighteningPrefix = tighteningPrefix + "__cluster/"
)

// NodeKeyLogProbe lists a tenant's raft databases whose log is on the
// shared, node-keyed WAL. service.NodeKeyLogProbe is the implementation.
type NodeKeyLogProbe interface {
	NodeKeyLogDatabases(ctx context.Context, tenantID string) ([]string, error)
}

// StrictTighteningWiring is the post-construction wiring surface of the
// tightening check, implemented by both tenant services.
type StrictTighteningWiring interface {
	// SetNodeKeyLogProbe sets the probe a tightening to strict consults.
	// With none wired, a tightening that affects a raft tenant is refused
	// (ErrNodeKeyLogsUnverifiable).
	SetNodeKeyLogProbe(NodeKeyLogProbe)
}

// IsolationTighteningReader reports whether a tightening to strict that
// would govern tenantID is in flight. Database create reads it after
// persisting a database on the shared WAL; see the section comment above.
type IsolationTighteningReader interface {
	IsolationTightening(ctx context.Context, tenantID string) (bool, error)
}

var (
	_ StrictTighteningWiring    = (*TenantService)(nil)
	_ StrictTighteningWiring    = (*kvTenantService)(nil)
	_ IsolationTighteningReader = (*TenantService)(nil)
	_ IsolationTighteningReader = (*kvTenantService)(nil)
)

// ErrNodeKeyLogsUnverifiable refuses a tightening to strict that affects a
// raft tenant when no NodeKeyLogProbe is wired to list its databases.
var ErrNodeKeyLogsUnverifiable = errors.New("tenant: the databases on the node-keyed WAL cannot be listed; tightening to strict is refused")

// NodeKeyLogsError refuses tightening a tenant to strict while some of its
// raft databases have their log on the shared, node-keyed WAL.
//
// Databases names them only on a refusal returned inside the owning tenant's
// scope (a tenant policy update). A refusal returned to the platform caller
// (the cluster floor) carries the tenant ID and Count alone: the caller has no
// right to read another tenant's catalog.
type NodeKeyLogsError struct {
	TenantID  string
	Databases []string
	Count     int
}

func (e *NodeKeyLogsError) Error() string {
	held := strconv.Itoa(e.Count) + " database(s)"
	if len(e.Databases) > 0 {
		held = strings.Join(e.Databases, ", ")
	}
	return "tenant: tenant " + strconv.Quote(e.TenantID) + " has databases on the node-keyed WAL (" +
		held + "); a strict tenant's every log is under its own key, so tightening to strict is refused"
}

// nodeKeyProbeRef boxes a NodeKeyLogProbe for atomic storage.
type nodeKeyProbeRef struct{ probe NodeKeyLogProbe }

// markerStore is what the tightening markers are written to: either tenant
// service's store.
type markerStore interface {
	Put(ctx context.Context, key string, value []byte) error
	Get(ctx context.Context, key string) ([]byte, error)
	Delete(ctx context.Context, key string) error
	List(ctx context.Context, prefix string) ([]string, error)
}

// SetNodeKeyLogProbe implements StrictTighteningWiring. A nil probe unwires
// it.
func (g *isolationChangeGuard) SetNodeKeyLogProbe(p NodeKeyLogProbe) {
	if p == nil {
		g.nodeKeyProbe.Store(nil)
		return
	}
	g.nodeKeyProbe.Store(&nodeKeyProbeRef{probe: p})
}

// WithNodeKeyLogProbe injects the probe a tightening to strict consults. See
// StrictTighteningWiring.
func WithNodeKeyLogProbe(p NodeKeyLogProbe) TenantServiceOption {
	return func(s *TenantService) { s.SetNodeKeyLogProbe(p) }
}

// tighteningLease bounds how long a tightening's marker governs creates. A
// marker records the deadline of its lease; one past it is stale -- left by a
// crash between writing the marker and removing it, or by a failed removal --
// and is ignored and removed by the next reader. A tightening commits only
// while at least tighteningCommitMargin of its lease remains, so a create
// that ignores an expired marker reads the policy after the commit landed.
const (
	tighteningLease        = 2 * time.Minute
	tighteningCommitMargin = 30 * time.Second
)

// tighteningNow is the clock marker leases are read against.
var tighteningNow = time.Now

// ErrTighteningLeaseExpired refuses a tightening whose checks outlasted its
// marker's lease: committing then could race a create that ignored the
// expired marker. Retrying is safe.
var ErrTighteningLeaseExpired = errors.New("tenant: the tightening to strict outlasted its marker's lease; retry")

// ErrTighteningNotAtomic refuses a tightening to strict on a store that
// cannot write the policy in one batch guarded on the tightening marker
// (kvstore.MultiGuardedBatchKVStore): without it a create that reaped the
// expired marker could still race the policy write.
var ErrTighteningNotAtomic = errors.New("tenant: the store cannot fence the strict policy write on its tightening marker")

// fencedBatcher is the store capability a fenced policy write needs: the
// multi-guard batch of kvstore.MultiGuardedBatchKVStore (which seal storage
// backends implement too) and Get.
type fencedBatcher interface {
	PutAllIfAllUnchanged(ctx context.Context, guards []kvstore.Guard, writes []kvstore.KV) (bool, error)
	Get(ctx context.Context, key string) ([]byte, error)
}

// tighteningFenceKey carries, in a commit's context, the marker the
// tightening's policy write is fenced on.
type tighteningFenceKey struct{}

func withTighteningFence(ctx context.Context, marker kvstore.Guard) context.Context {
	return context.WithValue(ctx, tighteningFenceKey{}, marker)
}

// fencedSwap writes next at key in place of expected (nil: key absent) when
// ctx carries a tightening's marker: one PutAllIfAllUnchanged guarded on
// both the marker and the key's expected value, so the policy is committed
// only while the marker still stands. A create that saw the marker expired
// deletes it before it reads the policy (isolationTightening), so either
// the write lands first and the create's policy read sees strict, or the
// marker is gone and the write is refused with ErrTighteningLeaseExpired.
// handled is false when ctx carries no fence: the caller writes as before.
func fencedSwap(ctx context.Context, store any, key string, expected, next []byte) (handled, swapped bool, err error) {
	marker, ok := ctx.Value(tighteningFenceKey{}).(kvstore.Guard)
	if !ok {
		return false, false, nil
	}
	batch, ok := store.(fencedBatcher)
	if !ok {
		return true, false, ErrTighteningNotAtomic
	}
	applied, err := batch.PutAllIfAllUnchanged(ctx,
		[]kvstore.Guard{marker, {Key: key, Value: expected}},
		[]kvstore.KV{{Key: key, Value: next}})
	if err != nil || applied {
		return true, applied, err
	}
	value, err := batch.Get(ctx, marker.Key)
	if err != nil && !errors.Is(err, seal.ErrSecretNotFound) {
		return true, false, err
	}
	if err != nil || string(value) != string(marker.Value) {
		return true, false, ErrTighteningLeaseExpired
	}
	return true, false, nil
}

// markerValue encodes a marker's lease deadline.
func markerValue(deadline time.Time) []byte {
	return binary.BigEndian.AppendUint64(nil, uint64(deadline.UnixNano()))
}

// markerLive reports whether value, a marker's content, holds a lease not
// yet past at now. A value that is not a lease (a marker of an earlier
// format) is stale.
func markerLive(value []byte, now time.Time) bool {
	if len(value) != 8 {
		return false
	}
	return now.UnixNano() < int64(binary.BigEndian.Uint64(value))
}

// tightenToStrict runs commit, the tightening, between writing a marker
// under markerPrefix and removing it, once no tenant that tenants lists has
// a database on the node-keyed WAL. tenants is called after the marker is
// written, so a tenant or database created concurrently is either listed or
// sees the marker.
func (g *isolationChangeGuard) tightenToStrict(ctx context.Context, store markerStore, markerPrefix string, discloseNames bool, tenants func(context.Context) ([]string, error), commit func(context.Context) error) error {
	var nonce [8]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return &StoreError{Op: "isolation-tightening-mark", Err: err}
	}
	key := markerPrefix + hex.EncodeToString(nonce[:])
	deadline := tighteningNow().Add(tighteningLease)
	if err := store.Put(ctx, key, markerValue(deadline)); err != nil {
		return &StoreError{Op: "isolation-tightening-mark", Err: err}
	}
	defer func() { _ = store.Delete(context.WithoutCancel(ctx), key) }()
	ids, err := tenants(ctx)
	if err != nil {
		return err
	}
	if len(ids) > 0 {
		ref := g.nodeKeyProbe.Load()
		if ref == nil {
			return ErrNodeKeyLogsUnverifiable
		}
		for _, id := range ids {
			dbs, err := ref.probe.NodeKeyLogDatabases(ctx, id)
			if err != nil {
				return &TenantDataProbeError{TenantID: id, Err: err}
			}
			if len(dbs) > 0 {
				nk := &NodeKeyLogsError{TenantID: id, Count: len(dbs)}
				if discloseNames {
					nk.Databases = dbs
				}
				return nk
			}
		}
	}
	if !tighteningNow().Add(tighteningCommitMargin).Before(deadline) {
		return ErrTighteningLeaseExpired
	}
	return commit(withTighteningFence(ctx, kvstore.Guard{Key: key, Value: markerValue(deadline)}))
}

// governedByStrict reports whether kind is an isolation type whose
// databases can be raft groups on the shared, node-keyed WAL: a raft
// tenant's, or a barrier tenant's served from the shared system store. A
// volume tenant's log is already under its own key; a prefix tenant is
// never strict.
func governedByStrict(kind IsolationType) bool {
	return kind == IsolationRaft || kind == IsolationBarrier
}

// tightensToStrict reports whether patch tightens info's own policy to
// strict for a tenant whose databases can be raft groups on the shared WAL.
func tightensToStrict(info *TenantInfo, patch TenantPatch) bool {
	if patch.IsolationPolicy != TenantIsolationPolicyStrict || info.IsolationPolicy == TenantIsolationPolicyStrict {
		return false
	}
	kind := info.EffectiveIsolationType()
	if patch.IsolationType != "" {
		kind = patch.IsolationType
	}
	return governedByStrict(kind)
}

// raftTenantsUnderFloor lists the raft and barrier tenants a strict floor
// would govern anew: those whose own policy is not already strict.
func raftTenantsUnderFloor(tenants []TenantInfo) []string {
	var out []string
	for i := range tenants {
		if governedByStrict(tenants[i].EffectiveIsolationType()) && tenants[i].IsolationPolicy != TenantIsolationPolicyStrict {
			out = append(out, tenants[i].ID)
		}
	}
	return out
}

// isolationTightening reports whether a live marker of a tightening
// governing tenantID -- its own, or the cluster floor's -- is in store. A
// stale marker is ignored and removed (best effort: the next reader retries).
func isolationTightening(ctx context.Context, store markerStore, tenantID string) (bool, error) {
	now := tighteningNow()
	for _, prefix := range []string{clusterTighteningPrefix, tighteningPrefix + tenantID + "/"} {
		keys, err := store.List(ctx, prefix)
		if err != nil {
			if errors.Is(err, seal.ErrSecretNotFound) {
				continue
			}
			return false, &StoreError{Op: "isolation-tightening-list", Err: err}
		}
		for _, key := range keys {
			value, err := store.Get(ctx, key)
			if err != nil {
				if errors.Is(err, seal.ErrSecretNotFound) {
					continue
				}
				return false, &StoreError{Op: "isolation-tightening-read", Err: err}
			}
			if markerLive(value, now) {
				return true, nil
			}
			// The expired marker is deleted BEFORE the caller reads the
			// policy: its tightening's write is fenced on the marker
			// (fencedSwap), so once it is gone that write cannot land
			// after the policy read. A delete that fails leaves the fence
			// standing, so the marker still counts.
			if err := store.Delete(ctx, key); err != nil && !errors.Is(err, seal.ErrSecretNotFound) {
				return false, &StoreError{Op: "isolation-tightening-reap", Err: err}
			}
		}
	}
	return false, nil
}

// IsolationTightening implements IsolationTighteningReader.
func (s *TenantService) IsolationTightening(ctx context.Context, tenantID string) (bool, error) {
	return isolationTightening(ctx, s.store, tenantID)
}

// IsolationTightening implements IsolationTighteningReader.
func (s *kvTenantService) IsolationTightening(ctx context.Context, tenantID string) (bool, error) {
	return isolationTightening(ctx, s.store, tenantID)
}

// guardedUpdate runs update, UpdateTenant's commit, under tightenToStrict
// when patch tightens tenantID's own policy to strict.
func (g *isolationChangeGuard) guardedUpdate(ctx context.Context, store markerStore, info *TenantInfo, patch TenantPatch, update func(context.Context) error) error {
	if info == nil {
		return update(ctx)
	}
	id := info.ID
	// Only an authenticated member of the target tenant may read its
	// database names in the refusal. A cross-tenant caller (a platform
	// operator holding tenants:update:cross) changes the registry, not the
	// tenant's catalog, so it gets the count-only refusal guardedFloor uses.
	disclose := callerBelongsTo(ctx, id)
	tighten := func() error {
		return g.tightenToStrict(ctx, store, tighteningPrefix+id+"/", disclose, func(context.Context) ([]string, error) {
			return []string{id}, nil
		}, update)
	}
	if tightensToStrict(info, patch) {
		return tighten()
	}
	// The initial record looked exempt, but the swap rereads the record and
	// commits over that version: when the record it commits over tightens to
	// strict, requireTighteningFence refuses the unfenced attempt and the
	// update runs again under the marker and probe.
	if err := update(ctx); !errors.Is(err, errTighteningRecheck) {
		return err
	}
	return tighten()
}

// callerBelongsTo reports whether ctx carries an authenticated identity of
// tenantID. A context with no identity, or one of another tenant (the system
// tenant included), does not.
func callerBelongsTo(ctx context.Context, tenantID string) bool {
	id := auth.IdentityFromContext(ctx)
	return id != nil && tenantid.Normalize(id.TenantID) == tenantid.Normalize(tenantID)
}

// errTighteningRecheck refuses an unfenced tenant swap whose current record
// tightens to strict; guardedUpdate retries it under the tightening marker.
var errTighteningRecheck = errors.New("tenant: strict tightening must be rechecked under its marker")

// requireTighteningFence is the swap-time half of the strict-tightening
// guard: it judges the record the compare-and-swap commits over (reread on
// every conflict retry), so a stale exempt read cannot skip the marker and
// node-keyed-log probe.
func requireTighteningFence(ctx context.Context, current *TenantInfo, patch TenantPatch) error {
	if !tightensToStrict(current, patch) {
		return nil
	}
	if _, ok := ctx.Value(tighteningFenceKey{}).(kvstore.Guard); ok {
		return nil
	}
	return errTighteningRecheck
}

// guardedFloor runs set, the cluster floor's tightening, under
// tightenToStrict for every raft or barrier tenant it would govern anew. The
// tenants are listed after the cluster marker is written -- even when none
// exist yet -- so a tenant created concurrently is either listed or its
// databases' creates see the marker.
func (g *isolationChangeGuard) guardedFloor(ctx context.Context, store markerStore, next ClusterIsolationPolicy, list func(context.Context) ([]TenantInfo, error), set func(context.Context) error) error {
	if next != ClusterIsolationPolicyStrict {
		return set(ctx)
	}
	return g.tightenToStrict(ctx, store, clusterTighteningPrefix, false, func(ctx context.Context) ([]string, error) {
		tenants, err := list(ctx)
		if err != nil {
			return nil, err
		}
		return raftTenantsUnderFloor(tenants), nil
	}, set)
}
