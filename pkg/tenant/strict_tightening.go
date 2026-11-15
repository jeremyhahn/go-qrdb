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
	"encoding/hex"
	"errors"
	"strconv"
	"strings"

	"github.com/jeremyhahn/go-qrdb/pkg/seal"
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
// record, then reads the policy and the markers. Whichever reads second sees
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
type NodeKeyLogsError struct {
	TenantID  string
	Databases []string
}

func (e *NodeKeyLogsError) Error() string {
	return "tenant: tenant " + strconv.Quote(e.TenantID) + " has databases on the node-keyed WAL (" +
		strings.Join(e.Databases, ", ") + "); a strict tenant's every log is under its own key, so tightening to strict is refused"
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

// tightenToStrict runs commit, the tightening, between writing a marker
// under markerPrefix and removing it, once no tenant in tenants has a
// database on the node-keyed WAL.
func (g *isolationChangeGuard) tightenToStrict(ctx context.Context, store markerStore, markerPrefix string, tenants []string, commit func() error) error {
	if len(tenants) == 0 {
		return commit()
	}
	var nonce [8]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return &StoreError{Op: "isolation-tightening-mark", Err: err}
	}
	key := markerPrefix + hex.EncodeToString(nonce[:])
	if err := store.Put(ctx, key, []byte{1}); err != nil {
		return &StoreError{Op: "isolation-tightening-mark", Err: err}
	}
	defer func() { _ = store.Delete(context.WithoutCancel(ctx), key) }()
	ref := g.nodeKeyProbe.Load()
	if ref == nil {
		return ErrNodeKeyLogsUnverifiable
	}
	for _, id := range tenants {
		dbs, err := ref.probe.NodeKeyLogDatabases(ctx, id)
		if err != nil {
			return &TenantDataProbeError{TenantID: id, Err: err}
		}
		if len(dbs) > 0 {
			return &NodeKeyLogsError{TenantID: id, Databases: dbs}
		}
	}
	return commit()
}

// tightensToStrict reports whether patch tightens info's own policy to
// strict for a tenant whose databases are raft groups (a volume tenant's
// log is already under its own key; a prefix tenant is never strict).
func tightensToStrict(info *TenantInfo, patch TenantPatch) bool {
	if patch.IsolationPolicy != TenantIsolationPolicyStrict || info.IsolationPolicy == TenantIsolationPolicyStrict {
		return false
	}
	kind := info.EffectiveIsolationType()
	if patch.IsolationType != "" {
		kind = patch.IsolationType
	}
	return kind == IsolationRaft
}

// raftTenantsUnderFloor lists the raft tenants a strict floor would govern
// anew: those whose own policy is not already strict.
func raftTenantsUnderFloor(tenants []TenantInfo) []string {
	var out []string
	for i := range tenants {
		if tenants[i].EffectiveIsolationType() == IsolationRaft && tenants[i].IsolationPolicy != TenantIsolationPolicyStrict {
			out = append(out, tenants[i].ID)
		}
	}
	return out
}

// isolationTightening reports whether a marker of a tightening governing
// tenantID -- its own, or the cluster floor's -- is in store.
func isolationTightening(ctx context.Context, store markerStore, tenantID string) (bool, error) {
	for _, prefix := range []string{clusterTighteningPrefix, tighteningPrefix + tenantID + "/"} {
		keys, err := store.List(ctx, prefix)
		if err != nil {
			if errors.Is(err, seal.ErrSecretNotFound) {
				continue
			}
			return false, &StoreError{Op: "isolation-tightening-list", Err: err}
		}
		if len(keys) > 0 {
			return true, nil
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
func (g *isolationChangeGuard) guardedUpdate(ctx context.Context, store markerStore, info *TenantInfo, patch TenantPatch, update func() error) error {
	if info == nil || !tightensToStrict(info, patch) {
		return update()
	}
	return g.tightenToStrict(ctx, store, tighteningPrefix+info.ID+"/", []string{info.ID}, update)
}

// guardedFloor runs set, the cluster floor's tightening, under
// tightenToStrict for every raft tenant it would govern anew.
func (g *isolationChangeGuard) guardedFloor(ctx context.Context, store markerStore, next ClusterIsolationPolicy, list func(context.Context) ([]TenantInfo, error), set func() error) error {
	if next != ClusterIsolationPolicyStrict {
		return set()
	}
	tenants, err := list(ctx)
	if err != nil {
		return err
	}
	return g.tightenToStrict(ctx, store, clusterTighteningPrefix, raftTenantsUnderFloor(tenants), set)
}
