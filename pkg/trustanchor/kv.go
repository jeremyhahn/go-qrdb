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

package trustanchor

import (
	"context"
	"crypto/x509"
	"encoding/json"
	"sort"
	"strings"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/audit"
	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
)

// Trust-anchor actions recorded on the compliance audit trail.
const (
	// ActionTrustAnchorAdd records Add: one or more CA certificates
	// registered as a tenant's trust anchors.
	ActionTrustAnchorAdd = "trust_anchor.add"

	// ActionTrustAnchorRemove records Remove: a trust anchor withdrawn by
	// fingerprint.
	ActionTrustAnchorRemove = "trust_anchor.remove"
)

// trustAnchorAuditComponent labels a trust-anchor record that could not be
// written, on qrdb_audit_write_failures_total{component=...}.
const trustAnchorAuditComponent = "trust-anchor"

// anchorSetKey is the single key holding a tenant's ENTIRE anchor set. Every
// anchor lives inside this one record rather than one key per fingerprint so
// Add and Remove -- including the MaxAnchorsPerTenant cap check -- apply in
// one atomic compare-and-swap, with no window between reading how many
// anchors exist and committing a write that would exceed the cap. A
// concurrent Add that raced the read is detected by the swap failing on the
// now-stale expected value, never by a lost update (go-qrdb security
// review).
const anchorSetKey = "__sys:trust-anchor-set"

// maxCASAttempts bounds Add and Remove's optimistic-concurrency retry loop.
// Each attempt either applies -- an exclusive, atomic win -- or finds the
// compare-and-swap no longer holds, because another Add or Remove won first,
// and rereads to retry. This many consecutive losses is far beyond what
// contention on one tenant's anchor set should ever produce under a
// consistent store, so reaching it is itself a signal rather than a
// plausible outcome of ordinary concurrency.
const maxCASAttempts = 1000

// storedAnchor is an anchor's persisted form.
type storedAnchor struct {
	CertPEM []byte    `json:"cert_pem"`
	AddedAt time.Time `json:"added_at"`
}

// anchorSet is the persisted, whole-tenant record stored under
// anchorSetKey, keyed by fingerprint.
type anchorSet struct {
	Anchors map[string]storedAnchor `json:"anchors"`
}

// KVRegistry is the built-in Registry. It keeps each tenant's anchors in a
// replicated KV store confined to that tenant's identity namespace
// (tenant.IdentityNamespace) -- the same boundary the tenant's identity
// records are confined to on the shared store, never the tenant's own barrier,
// so a tenant whose barrier is sealed still has readable anchors.
type KVRegistry struct {
	store       kvstore.KVStore
	now         func() time.Time
	audit       audit.AuditLogger
	auditPolicy audit.FailurePolicy
}

// Compile-time interface check.
var _ Registry = (*KVRegistry)(nil)

// KVOption configures a KVRegistry.
type KVOption func(*KVRegistry)

// WithClock overrides the registry's clock (validity checks and AddedAt).
func WithClock(now func() time.Time) KVOption {
	return func(r *KVRegistry) {
		if now != nil {
			r.now = now
		}
	}
}

// WithAnchorAuditLogger installs the compliance audit trail Add and Remove
// record trust_anchor.add and trust_anchor.remove on. A nil logger disables
// emission, so the option is safe to apply unconditionally.
func WithAnchorAuditLogger(l audit.AuditLogger) KVOption {
	return func(r *KVRegistry) { r.audit = l }
}

// WithAnchorAuditPolicy installs the node's audit failure posture for
// trust-anchor's gating records (auth.audit_failure_policy): what Add and
// Remove do when the trail cannot record a change before it takes effect.
// The zero value, audit.FailClosed, is the default.
func WithAnchorAuditPolicy(p audit.FailurePolicy) KVOption {
	return func(r *KVRegistry) { r.auditPolicy = p }
}

// anchorEvent renders a trust-anchor event keyed by tenantID -- the tenant
// AFFECTED by the change, which Add and Remove already take as an explicit
// parameter rather than inferring from the caller.
func anchorEvent(ctx context.Context, tenantID, action string, attrs map[string]any) audit.AuditEvent {
	var actor string
	if id := auth.IdentityFromContext(ctx); id != nil {
		actor = id.Subject
	}
	return audit.AuditEvent{
		Actor:      actor,
		TenantID:   tenantid.Normalize(tenantID),
		Resource:   "trust-anchor",
		Action:     action,
		Attributes: attrs,
	}
}

// gateAnchorAudit writes ev as a GATING record before the change it
// describes is committed, following the node's one audit failure posture:
// fail-closed -- the default -- refuses the change with *audit.GateError
// when the record cannot be written; fail-open lets it proceed, counting
// the gap. Without a configured sink it is a no-op.
func (r *KVRegistry) gateAnchorAudit(ctx context.Context, ev audit.AuditEvent) error {
	if r.audit == nil {
		return nil
	}
	ev.Outcome = audit.OutcomePermitted
	return audit.EmitGated(ctx, r.audit, trustAnchorAuditComponent, r.auditPolicy, ev)
}

// settleAnchorAudit records ev's final outcome once the change it gated has
// run to completion. Best-effort: a sink that refuses it is counted and
// logged, never returned as the caller's error.
func (r *KVRegistry) settleAnchorAudit(ctx context.Context, ev audit.AuditEvent, err error) {
	if r.audit == nil {
		return
	}
	if err == nil {
		ev.Outcome = audit.OutcomeSuccess
	} else {
		ev.Outcome = audit.OutcomeFailure + ":trust-anchor-change-failed"
	}
	audit.EmitBestEffort(ctx, r.audit, trustAnchorAuditComponent, ev)
}

// NewKVRegistry returns the built-in Registry over store, which must be the
// replicated system store (not a tenant-confined one): the registry confines
// each tenant itself. Add and Remove additionally require store to offer
// kvstore.AtomicKVStore once scoped to a tenant; a store that does not is
// refused with *AtomicStoreRequiredError rather than silently falling back
// to a non-atomic write.
func NewKVRegistry(store kvstore.KVStore, opts ...KVOption) *KVRegistry {
	r := &KVRegistry{store: store, now: time.Now}
	for _, opt := range opts {
		opt(r)
	}
	return r
}

// scoped returns the store confined to tenantID's identity namespace.
func (r *KVRegistry) scoped(tenantID string) (kvstore.KVStore, error) {
	if !tenantid.IsSystem(tenantID) {
		if err := tenant.ValidateTenantIDShape(tenantID); err != nil {
			return nil, &InvalidTenantError{TenantID: tenantID, Err: err}
		}
	}
	s, err := kvstore.NewNamespacedKVStore(r.store, tenant.IdentityNamespace(tenantID))
	if err != nil {
		return nil, &StoreError{Op: "scope", Err: err}
	}
	return s, nil
}

// readSet returns tenantID's current anchor set and the exact raw bytes it
// was read as (nil when the tenant has never had an anchor), so a caller can
// use the raw bytes as a compare-and-swap's expected value.
func (r *KVRegistry) readSet(ctx context.Context, s kvstore.KVStore) (anchorSet, []byte, error) {
	raw, err := s.Get(ctx, anchorSetKey)
	if err != nil {
		return anchorSet{}, nil, &StoreError{Op: "get", Err: err}
	}
	if raw == nil {
		return anchorSet{Anchors: map[string]storedAnchor{}}, nil, nil
	}
	var set anchorSet
	if err := json.Unmarshal(raw, &set); err != nil {
		return anchorSet{}, nil, &StoreError{Op: "decode", Err: err}
	}
	if set.Anchors == nil {
		set.Anchors = map[string]storedAnchor{}
	}
	return set, raw, nil
}

// CABundle returns tenantID's anchors as one PEM bundle in fingerprint order,
// nil when it has none.
func (r *KVRegistry) CABundle(ctx context.Context, tenantID string) ([]byte, error) {
	s, err := r.scoped(tenantID)
	if err != nil {
		return nil, err
	}
	set, _, err := r.readSet(ctx, s)
	if err != nil {
		return nil, err
	}
	if len(set.Anchors) == 0 {
		return nil, nil
	}
	fps := sortedFingerprints(set.Anchors)
	var buf []byte
	for _, fp := range fps {
		buf = append(buf, set.Anchors[fp].CertPEM...)
	}
	return buf, nil
}

// Add validates every certificate in bundlePEM as an anchor and registers the
// ones tenantID does not already have, applying the registration and the
// MaxAnchorsPerTenant cap check as one atomic compare-and-swap of the
// tenant's whole anchor set. It returns every certificate of the bundle as
// registered, the already-present ones with their original AddedAt.
//
// A bundle that would push the tenant past the cap, or that loses the
// compare-and-swap race every retry, applies NOTHING: there is no partial
// registration, in bundle order or otherwise.
func (r *KVRegistry) Add(ctx context.Context, tenantID string, bundlePEM []byte) ([]Anchor, error) {
	now := r.now()
	certs, err := ParseAnchors(bundlePEM, now)
	if err != nil {
		return nil, err
	}
	s, err := r.scoped(tenantID)
	if err != nil {
		return nil, err
	}
	atomicStore, ok := s.(kvstore.AtomicKVStore)
	if !ok {
		return nil, &AtomicStoreRequiredError{Op: "add"}
	}

	fps := make([]string, 0, len(certs))
	for _, c := range certs {
		fps = append(fps, Fingerprint(c))
	}
	ev := anchorEvent(ctx, tenantID, ActionTrustAnchorAdd, map[string]any{"fingerprints": fps})
	if err := r.gateAnchorAudit(ctx, ev); err != nil {
		return nil, err
	}
	out, err := r.addCAS(ctx, s, atomicStore, tenantID, certs, now)
	r.settleAnchorAudit(ctx, ev, err)
	return out, err
}

// addCAS applies the optimistic-concurrency commit loop Add validated and
// gated, with no audit concerns of its own.
func (r *KVRegistry) addCAS(ctx context.Context, s kvstore.KVStore, atomicStore kvstore.AtomicKVStore, tenantID string, certs []*x509.Certificate, now time.Time) ([]Anchor, error) {
	for attempt := 0; ; attempt++ {
		if attempt >= maxCASAttempts {
			return nil, &ConcurrentUpdateError{TenantID: tenantID, Op: "add"}
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		set, raw, err := r.readSet(ctx, s)
		if err != nil {
			return nil, err
		}
		added := 0
		for _, c := range certs {
			if _, ok := set.Anchors[Fingerprint(c)]; !ok {
				added++
			}
		}
		if len(set.Anchors)+added > MaxAnchorsPerTenant {
			return nil, &TooManyAnchorsError{TenantID: tenantID, Max: MaxAnchorsPerTenant}
		}
		out := make([]Anchor, 0, len(certs))
		if added == 0 {
			for _, c := range certs {
				out = append(out, anchorOf(c, set.Anchors[Fingerprint(c)].AddedAt))
			}
			return out, nil
		}

		next := anchorSet{Anchors: make(map[string]storedAnchor, len(set.Anchors)+added)}
		for fp, sa := range set.Anchors {
			next.Anchors[fp] = sa
		}
		for _, c := range certs {
			fp := Fingerprint(c)
			if sa, ok := next.Anchors[fp]; ok {
				out = append(out, anchorOf(c, sa.AddedAt))
				continue
			}
			sa := storedAnchor{CertPEM: EncodePEM(c), AddedAt: now.UTC()}
			next.Anchors[fp] = sa
			out = append(out, anchorOf(c, sa.AddedAt))
		}
		value, err := json.Marshal(next)
		if err != nil {
			return nil, &StoreError{Op: "encode", Err: err}
		}

		swapped, err := casAnchorSet(ctx, atomicStore, raw, value)
		if err != nil {
			return nil, &StoreError{Op: "put", Err: err}
		}
		if swapped {
			return out, nil
		}
		// Lost the race: another Add or Remove changed the tenant's anchor
		// set between the read above and this attempt. Reread and retry --
		// the fresh read re-evaluates the cap, so a bundle that would now
		// overflow it is refused instead of retried forever.
	}
}

// casAnchorSet applies next to anchorSetKey only while its stored value still
// equals raw. A nil raw means the key was absent at the read this attempt is
// based on, so the key must still be absent now.
func casAnchorSet(ctx context.Context, atomicStore kvstore.AtomicKVStore, raw, next []byte) (bool, error) {
	if raw == nil {
		return atomicStore.PutIfAbsent(ctx, anchorSetKey, next)
	}
	return atomicStore.CompareAndSwap(ctx, anchorSetKey, raw, next)
}

// List returns tenantID's anchors in fingerprint order.
func (r *KVRegistry) List(ctx context.Context, tenantID string) ([]Anchor, error) {
	s, err := r.scoped(tenantID)
	if err != nil {
		return nil, err
	}
	set, _, err := r.readSet(ctx, s)
	if err != nil {
		return nil, err
	}
	fps := sortedFingerprints(set.Anchors)
	out := make([]Anchor, 0, len(fps))
	for _, fp := range fps {
		sa := set.Anchors[fp]
		certs, err := ParseCertificates(sa.CertPEM)
		if err != nil {
			return nil, &StoreError{Op: "decode", Err: err}
		}
		out = append(out, anchorOf(certs[0], sa.AddedAt))
	}
	return out, nil
}

// Remove withdraws tenantID's anchor with the given fingerprint, applying the
// withdrawal as one atomic compare-and-swap of the tenant's whole anchor set.
func (r *KVRegistry) Remove(ctx context.Context, tenantID, fingerprint string) error {
	s, err := r.scoped(tenantID)
	if err != nil {
		return err
	}
	fp := strings.ToLower(strings.TrimSpace(fingerprint))
	if fp == "" || strings.ContainsAny(fp, "/") {
		return &AnchorNotFoundError{TenantID: tenantID, Fingerprint: fingerprint}
	}
	atomicStore, ok := s.(kvstore.AtomicKVStore)
	if !ok {
		return &AtomicStoreRequiredError{Op: "remove"}
	}

	ev := anchorEvent(ctx, tenantID, ActionTrustAnchorRemove, map[string]any{"fingerprint": fp})
	if err := r.gateAnchorAudit(ctx, ev); err != nil {
		return err
	}
	err = r.removeCAS(ctx, s, atomicStore, tenantID, fingerprint, fp)
	r.settleAnchorAudit(ctx, ev, err)
	return err
}

// removeCAS applies the optimistic-concurrency commit loop Remove validated
// and gated, with no audit concerns of its own.
func (r *KVRegistry) removeCAS(ctx context.Context, s kvstore.KVStore, atomicStore kvstore.AtomicKVStore, tenantID, fingerprint, fp string) error {
	for attempt := 0; ; attempt++ {
		if attempt >= maxCASAttempts {
			return &ConcurrentUpdateError{TenantID: tenantID, Op: "remove"}
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		set, raw, err := r.readSet(ctx, s)
		if err != nil {
			return err
		}
		if _, ok := set.Anchors[fp]; !ok {
			return &AnchorNotFoundError{TenantID: tenantID, Fingerprint: fingerprint}
		}
		next := anchorSet{Anchors: make(map[string]storedAnchor, len(set.Anchors)-1)}
		for k, v := range set.Anchors {
			if k == fp {
				continue
			}
			next.Anchors[k] = v
		}
		value, err := json.Marshal(next)
		if err != nil {
			return &StoreError{Op: "encode", Err: err}
		}
		swapped, err := atomicStore.CompareAndSwap(ctx, anchorSetKey, raw, value)
		if err != nil {
			return &StoreError{Op: "put", Err: err}
		}
		if swapped {
			return nil
		}
		// Lost the race against a concurrent Add or Remove; reread and retry.
	}
}

// sortedFingerprints returns anchors' keys in ascending order.
func sortedFingerprints(anchors map[string]storedAnchor) []string {
	fps := make([]string, 0, len(anchors))
	for fp := range anchors {
		fps = append(fps, fp)
	}
	sort.Strings(fps)
	return fps
}
