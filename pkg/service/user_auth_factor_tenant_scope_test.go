// Copyright 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package service

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// ---------------------------------------------------------------------------
// Harness
//
// The harness is built from the PRODUCTION resolver over the PRODUCTION
// in-memory store and the production tenant service. The whole claim of
// go-qrdb#393 is a property of what tenant.NewStoreResolver hands back for a
// given tenant, so a stub resolver here would be a test of the stub: it would
// pass whether or not the confinement it models exists.
// ---------------------------------------------------------------------------

// factorTenantHarness is a routed user auth-factor service over a real
// resolver, plus the shared store beneath it so a test can assert on physical
// keys.
type factorTenantHarness struct {
	shared  *kvstore.MemoryKVStore
	tenants tenant.TenantServicer
	res     tenant.StoreResolver
	svc     UserAuthFactorServicer
}

// newFactorTenantHarness builds the harness and provisions the named prefix
// tenants.
func newFactorTenantHarness(t *testing.T, tenantIDs ...string) *factorTenantHarness {
	t.Helper()
	shared := kvstore.NewMemoryKVStore()
	tenants, err := tenant.NewKVTenantService(shared, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatalf("NewKVTenantService: %v", err)
	}
	for _, id := range tenantIDs {
		if _, err := tenants.CreateTenant(context.Background(), id, id, tenant.IsolationPrefix); err != nil {
			t.Fatalf("CreateTenant(%s): %v", id, err)
		}
	}
	res, err := tenant.NewStoreResolver(shared, nil, tenants)
	if err != nil {
		t.Fatalf("NewStoreResolver: %v", err)
	}
	svc, err := NewRoutingUserAuthFactorService(RoutingUserAuthFactorServiceConfig{Resolver: res})
	if err != nil {
		t.Fatalf("NewRoutingUserAuthFactorService: %v", err)
	}
	return &factorTenantHarness{shared: shared, tenants: tenants, res: res, svc: svc}
}

// ctxFor returns a context bound to the named tenant.
func ctxFor(id string) context.Context {
	return tenant.WithTenantID(context.Background(), id)
}

// ---------------------------------------------------------------------------
// The go-qrdb#393 gate
// ---------------------------------------------------------------------------

// TestRoutedFactorLookupDoesNotCrossTenants is the issue's reproduction, now
// asserting the fix: two tenants, the same (type, credential), and a login
// begun under tenant B must not resolve the factor enrolled under tenant A.
//
// Before the routing change this failed — the lookup key
// "__sys:auth/lookup/{type}/{credKeyHex}" carried no tenant and the service
// read none from its context, so both tenants addressed one record.
func TestRoutedFactorLookupDoesNotCrossTenants(t *testing.T) {
	h := newFactorTenantHarness(t, "acme", "globex")
	cred := []byte("the-same-credential-blob")

	if err := h.svc.Add(ctxFor("acme"), 1001, mtlsFactor("factor-a", 1001, cred)); err != nil {
		t.Fatalf("Add under acme: %v", err)
	}

	got, err := h.svc.LookupByCredentialKey(ctxFor("globex"), auth.AuthFactorMTLS, cred)
	if err == nil {
		t.Fatalf("globex resolved acme's factor %q (user %d)", got.ID, got.UserID)
	}
	var notFound *auth.FactorNotFoundError
	if !errors.As(err, &notFound) {
		t.Fatalf("want *auth.FactorNotFoundError, got %T: %v", err, err)
	}

	// The enrolling tenant still resolves it, so the boundary is a boundary
	// and not an outage.
	own, err := h.svc.LookupByCredentialKey(ctxFor("acme"), auth.AuthFactorMTLS, cred)
	if err != nil {
		t.Fatalf("acme could not resolve its own factor: %v", err)
	}
	if own.ID != "factor-a" {
		t.Fatalf("acme resolved %q, want factor-a", own.ID)
	}
}

// TestRoutedFactorKeysCarryTheTenantNamespace pins the physical layout the
// routing produces: the records live under the tenant's identity namespace,
// which is the same prefix pkg/user's routed store writes under, and nothing
// is left at the flat root.
func TestRoutedFactorKeysCarryTheTenantNamespace(t *testing.T) {
	h := newFactorTenantHarness(t, "acme")
	if err := h.svc.Add(ctxFor("acme"), 7, mtlsFactor("factor-ns", 7, []byte("cred-ns"))); err != nil {
		t.Fatalf("Add: %v", err)
	}

	want := tenant.IdentityNamespace("acme") + userFactorPrimaryPrefix + "factor-ns"
	if v, err := h.shared.Get(context.Background(), want); err != nil || len(v) == 0 {
		t.Fatalf("no record at %q (err=%v)", want, err)
	}
	flat, err := h.shared.List(context.Background(), userFactorPrimaryPrefix)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(flat) != 0 {
		t.Fatalf("routed Add wrote %d record(s) at the untenanted root: %v", len(flat), flat)
	}
}

// TestRoutedFactorEnrolmentIsNotBlockedByAnotherTenant asserts the other half
// of the shared keyspace: with one record per credential, tenant B enrolling
// the same credential collided with tenant A's record and was refused
// *auth.FactorAlreadyExistsError — which both denied a legitimate enrolment
// and disclosed a factor id belonging to another tenant.
func TestRoutedFactorEnrolmentIsNotBlockedByAnotherTenant(t *testing.T) {
	h := newFactorTenantHarness(t, "acme", "globex")
	cred := []byte("a-credential-two-tenants-both-use")

	if err := h.svc.Add(ctxFor("acme"), 1, mtlsFactor("acme-factor", 1, cred)); err != nil {
		t.Fatalf("Add under acme: %v", err)
	}
	if err := h.svc.Add(ctxFor("globex"), 2, mtlsFactor("globex-factor", 2, cred)); err != nil {
		t.Fatalf("globex could not enrol the same credential: %v", err)
	}

	got, err := h.svc.LookupByCredentialKey(ctxFor("globex"), auth.AuthFactorMTLS, cred)
	if err != nil {
		t.Fatalf("globex lookup: %v", err)
	}
	if got.ID != "globex-factor" || got.UserID != 2 {
		t.Fatalf("globex resolved %q (user %d), want globex-factor (user 2)", got.ID, got.UserID)
	}
}

// TestRoutedFactorByIDStaysInTenant covers every by-id verb at once: a factor
// id is issued inside a tenant, and presenting it from another tenant finds
// nothing rather than acting on someone else's record.
func TestRoutedFactorByIDStaysInTenant(t *testing.T) {
	h := newFactorTenantHarness(t, "acme", "globex")
	if err := h.svc.Add(ctxFor("acme"), 11, mtlsFactor("only-in-acme", 11, []byte("cred-by-id"))); err != nil {
		t.Fatalf("Add: %v", err)
	}
	intruder := ctxFor("globex")

	if _, err := h.svc.Get(intruder, "only-in-acme"); err == nil {
		t.Fatal("Get from another tenant returned a record")
	}
	if err := h.svc.Relabel(intruder, "only-in-acme", "stolen"); err == nil {
		t.Fatal("Relabel from another tenant succeeded")
	}
	if err := h.svc.Remove(intruder, "only-in-acme", true); err == nil {
		t.Fatal("Remove from another tenant succeeded")
	}
	if err := h.svc.Rotate(intruder, "only-in-acme", mtlsFactor("rotated", 11, []byte("new-cred"))); err == nil {
		t.Fatal("Rotate from another tenant succeeded")
	}
	if err := h.svc.MarkUsed(intruder, "only-in-acme", time.Now()); err == nil {
		t.Fatal("MarkUsed from another tenant succeeded")
	}

	// The owning tenant is unaffected by any of it.
	f, err := h.svc.Get(ctxFor("acme"), "only-in-acme")
	if err != nil {
		t.Fatalf("owning tenant Get: %v", err)
	}
	if f.Label != "mtls-only-in-acme" || f.RevokedAt != nil {
		t.Fatalf("record was modified across the boundary: %+v", f)
	}
}

// TestRoutedFactorListIsPerTenant asserts List enumerates only the caller's
// tenant, even when both tenants happen to use the same numeric user id.
func TestRoutedFactorListIsPerTenant(t *testing.T) {
	h := newFactorTenantHarness(t, "acme", "globex")
	const sharedUserID = 42

	if err := h.svc.Add(ctxFor("acme"), sharedUserID, mtlsFactor("a1", sharedUserID, []byte("a-cred"))); err != nil {
		t.Fatalf("Add acme: %v", err)
	}
	if err := h.svc.Add(ctxFor("globex"), sharedUserID, mtlsFactor("g1", sharedUserID, []byte("g-cred"))); err != nil {
		t.Fatalf("Add globex: %v", err)
	}

	for _, tc := range []struct{ tenantID, wantID string }{
		{"acme", "a1"},
		{"globex", "g1"},
	} {
		got, err := h.svc.List(ctxFor(tc.tenantID), sharedUserID)
		if err != nil {
			t.Fatalf("List(%s): %v", tc.tenantID, err)
		}
		if len(got) != 1 || got[0].ID != tc.wantID {
			t.Fatalf("List(%s) = %v, want exactly [%s]", tc.tenantID, factorIDs(got), tc.wantID)
		}
	}
}

// TestRoutedHasActiveFactorIsPerTenant asserts the fail-closed active-factor
// gate is reached through the router and answers per tenant. A gate that read
// the flat keyspace would report another tenant's factor as this user's.
func TestRoutedHasActiveFactorIsPerTenant(t *testing.T) {
	h := newFactorTenantHarness(t, "acme", "globex")
	checker, ok := h.svc.(ActiveFactorChecker)
	if !ok {
		t.Fatal("routed service is not an ActiveFactorChecker; the type assertion consumers make would skip the gate")
	}
	if err := h.svc.Add(ctxFor("acme"), 5, mtlsFactor("only-acme", 5, []byte("cred-active"))); err != nil {
		t.Fatalf("Add: %v", err)
	}

	has, err := checker.HasActiveFactor(ctxFor("acme"), 5)
	if err != nil || !has {
		t.Fatalf("acme HasActiveFactor = %v, %v; want true, nil", has, err)
	}
	has, err = checker.HasActiveFactor(ctxFor("globex"), 5)
	if err != nil {
		t.Fatalf("globex HasActiveFactor: %v", err)
	}
	if has {
		t.Fatal("globex sees acme's factor as an active factor of its own user 5")
	}
}

// TestRoutedAttestationRecorderReachesTheTenant asserts the in-process
// attestation path is reachable through the router and lands in the caller's
// tenant. AttestationRecorder is reached by type assertion, so a router that
// failed to implement it would silently remove the only way a verified
// attestation can be recorded.
func TestRoutedAttestationRecorderReachesTheTenant(t *testing.T) {
	h := newFactorTenantHarness(t, "acme", "globex")
	recorder, ok := h.svc.(AttestationRecorder)
	if !ok {
		t.Fatal("routed service is not an AttestationRecorder")
	}
	attested := auth.VerifiedAttestation{Level: auth.AttestationLevelHardware}
	if err := recorder.AddAttested(ctxFor("acme"), 9, mtlsFactor("attested", 9, []byte("cred-attested")), attested); err != nil {
		t.Fatalf("AddAttested: %v", err)
	}
	if _, err := h.svc.Get(ctxFor("globex"), "attested"); err == nil {
		t.Fatal("an attested factor added under acme is readable from globex")
	}
	got, err := h.svc.Get(ctxFor("acme"), "attested")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Metadata[auth.MetadataAttestLevel] != string(auth.AttestationLevelHardware) {
		t.Fatalf("attestation not recorded: %v", got.Metadata)
	}
}

// TestRoutedFactorServiceReusesOneServicePerTenant asserts the per-tenant
// cache returns one instance per tenant. It matters beyond allocation: that
// instance owns the mutex serialising Add / Rotate / Remove within a tenant,
// so a router that rebuilt it per call would drop the serialization.
func TestRoutedFactorServiceReusesOneServicePerTenant(t *testing.T) {
	h := newFactorTenantHarness(t, "acme", "globex")
	router, ok := h.svc.(*routingUserAuthFactorService)
	if !ok {
		t.Fatalf("harness built %T, want *routingUserAuthFactorService", h.svc)
	}
	first, err := router.serviceFor(ctxFor("acme"))
	if err != nil {
		t.Fatalf("serviceFor: %v", err)
	}
	second, err := router.serviceFor(ctxFor("acme"))
	if err != nil {
		t.Fatalf("serviceFor: %v", err)
	}
	if first != second {
		t.Fatal("the same tenant resolved two different per-tenant services")
	}
	other, err := router.serviceFor(ctxFor("globex"))
	if err != nil {
		t.Fatalf("serviceFor: %v", err)
	}
	if other == first {
		t.Fatal("two tenants share one per-tenant service")
	}
}

// ---------------------------------------------------------------------------
// Failure propagation and construction
// ---------------------------------------------------------------------------

// sealedResolver is a StoreResolver that refuses every tenant the way a
// dedicated-isolation tenant with a sealed barrier does.
type sealedResolver struct{}

func (sealedResolver) Resolve(context.Context, string) (kvstore.KVStore, error) {
	return nil, tenant.ErrTenantSealed
}

// TestRoutedFactorServiceFailsClosedOnSealedTenant asserts a resolver refusal
// reaches the caller on every verb rather than being folded into a not-found
// or, worse, a "no active factor" a security gate would read as permission to
// proceed.
func TestRoutedFactorServiceFailsClosedOnSealedTenant(t *testing.T) {
	svc, err := NewRoutingUserAuthFactorService(RoutingUserAuthFactorServiceConfig{Resolver: sealedResolver{}})
	if err != nil {
		t.Fatalf("NewRoutingUserAuthFactorService: %v", err)
	}
	ctx := ctxFor("sealed-tenant")

	checks := map[string]func() error{
		"Add":                   func() error { return svc.Add(ctx, 1, mtlsFactor("x", 1, []byte("c"))) },
		"Get":                   func() error { _, err := svc.Get(ctx, "x"); return err },
		"List":                  func() error { _, err := svc.List(ctx, 1); return err },
		"LookupByCredentialKey": func() error { _, e := svc.LookupByCredentialKey(ctx, auth.AuthFactorMTLS, []byte("c")); return e },
		"Remove":                func() error { return svc.Remove(ctx, "x", true) },
		"Rotate":                func() error { return svc.Rotate(ctx, "x", mtlsFactor("y", 1, []byte("d"))) },
		"Relabel":               func() error { return svc.Relabel(ctx, "x", "l") },
		"MarkUsed":              func() error { return svc.MarkUsed(ctx, "x", time.Now()) },
		"HasActiveFactor": func() error {
			_, e := svc.(ActiveFactorChecker).HasActiveFactor(ctx, 1)
			return e
		},
		"AddAttested": func() error {
			return svc.(AttestationRecorder).AddAttested(ctx, 1, mtlsFactor("x", 1, []byte("c")), auth.Unattested())
		},
		"RotateAttested": func() error {
			return svc.(AttestationRecorder).RotateAttested(ctx, "x", mtlsFactor("y", 1, []byte("d")), auth.Unattested())
		},
	}
	for name, call := range checks {
		if err := call(); !errors.Is(err, tenant.ErrTenantSealed) {
			t.Errorf("%s: want tenant.ErrTenantSealed, got %v", name, err)
		}
	}
}

// TestNewRoutingUserAuthFactorServiceRequiresResolver asserts the constructor
// refuses rather than producing a service with no tenant dimension.
func TestNewRoutingUserAuthFactorServiceRequiresResolver(t *testing.T) {
	svc, err := NewRoutingUserAuthFactorService(RoutingUserAuthFactorServiceConfig{})
	if err == nil {
		t.Fatal("nil resolver was accepted")
	}
	if svc != nil {
		t.Fatal("a service was returned alongside the error")
	}
	var invalid *auth.InvalidFactorError
	if !errors.As(err, &invalid) {
		t.Fatalf("want *auth.InvalidFactorError, got %T", err)
	}
}

// TestUnroutedFactorServiceIsOneTenantsStore records the contract of the
// per-tenant service, which is what made go-qrdb#393 possible: it reads NO
// tenant from its context, by design, because the tenant is the store it was
// built over. This is the reproduction the issue described — the same
// assertion reversed — and it is kept so a future change that wires the bare
// service onto a shared store has to delete a test that says why not to.
func TestUnroutedFactorServiceIsOneTenantsStore(t *testing.T) {
	svc, err := NewUserAuthFactorService(UserAuthFactorServiceConfig{KV: newAFMockKV()})
	if err != nil {
		t.Fatalf("NewUserAuthFactorService: %v", err)
	}
	cred := []byte("the-same-credential-blob")
	if err := svc.Add(ctxFor("acme"), 1001, mtlsFactor("factor-a", 1001, cred)); err != nil {
		t.Fatalf("Add: %v", err)
	}
	got, err := svc.LookupByCredentialKey(ctxFor("globex"), auth.AuthFactorMTLS, cred)
	if err != nil {
		t.Fatalf("the per-tenant service is context-blind and must still resolve: %v", err)
	}
	if got.ID != "factor-a" {
		t.Fatalf("resolved %q, want factor-a", got.ID)
	}
}

// TestUnroutedFactorServiceAcceptsAKVStore asserts the per-tenant service
// builds over a kvstore.KVStore as well as a KVServicer — the narrowing that
// lets the router hand it a resolved store directly — and that a missing key
// reported as (nil, nil) rather than as an error still reads as absent.
func TestUnroutedFactorServiceAcceptsAKVStore(t *testing.T) {
	store := kvstore.NewMemoryKVStore()
	svc, err := NewUserAuthFactorService(UserAuthFactorServiceConfig{KV: store})
	if err != nil {
		t.Fatalf("NewUserAuthFactorService over a KVStore: %v", err)
	}
	if _, err := svc.Get(context.Background(), "absent"); err == nil {
		t.Fatal("an absent factor read back as present")
	}
	if err := svc.Add(context.Background(), 3, mtlsFactor("kvstore-factor", 3, []byte("c"))); err != nil {
		t.Fatalf("Add: %v", err)
	}
	got, err := svc.LookupByCredentialKey(context.Background(), auth.AuthFactorMTLS, []byte("c"))
	if err != nil {
		t.Fatalf("LookupByCredentialKey: %v", err)
	}
	if got.ID != "kvstore-factor" {
		t.Fatalf("resolved %q", got.ID)
	}
}

// factorIDs renders a factor slice as its ids, for failure messages.
func factorIDs(factors []*auth.AuthFactor) string {
	ids := make([]string, 0, len(factors))
	for _, f := range factors {
		ids = append(ids, f.ID)
	}
	return "[" + strings.Join(ids, " ") + "]"
}

// ---------------------------------------------------------------------------
// Routing by the USER id the call names
// ---------------------------------------------------------------------------

// TestRoutedFactorEnrolmentFollowsTheUsersTenant asserts an enrolment lands in
// the tenant that OWNS the user, not in whatever tenant the enrolling context
// happened to name.
//
// This is what keeps the sequencing safe. Enrolment for a user is performed by
// a composer out of this repository, on a context whose tenant binding is its
// own business; if the record landed there, the login that follows — begun
// under the USER's tenant — would find nothing, and the failure would look
// like a bad credential rather than a misplaced record.
func TestRoutedFactorEnrolmentFollowsTheUsersTenant(t *testing.T) {
	h := newFactorTenantHarness(t, "acme", "globex")
	owners, err := tenant.NewResolvedIdentityIndex(h.res)
	if err != nil {
		t.Fatalf("NewResolvedIdentityIndex: %v", err)
	}
	if _, err := owners.Record(context.Background(), tenant.IdentityUser, tenant.IdentityID(55), "acme"); err != nil {
		t.Fatalf("Record: %v", err)
	}

	cred := []byte("enrolled-without-a-tenant-context")
	// A privileged (system) context naming no tenant of its own is refused
	// the same as any other foreign-tenant caller: the system-tenant lift
	// was removed (owner ruling, 2026-10-01). Routing to the owning tenant
	// happens only for a caller already admitted to reach it.
	err = h.svc.Add(context.Background(), 55, mtlsFactor("owner-routed", 55, cred))
	var denied *tenant.CrossTenantDeniedError
	if !errors.As(err, &denied) {
		t.Fatalf("Add = %v, want *tenant.CrossTenantDeniedError", err)
	}

	if _, err := h.svc.LookupByCredentialKey(ctxFor("acme"), auth.AuthFactorMTLS, cred); err == nil {
		t.Fatal("a refused enrolment must not resolve from the owning tenant")
	}
}

// TestRoutedFactorEnrolmentRefusesAnotherTenantsUser asserts a tenant caller
// cannot bind a credential of its choosing to a user id owned by another
// tenant. Routing such a write to the caller's own tenant would leave the
// boundary resting on the downstream user lookup refusing the same read.
func TestRoutedFactorEnrolmentRefusesAnotherTenantsUser(t *testing.T) {
	h := newFactorTenantHarness(t, "acme", "globex")
	owners, err := tenant.NewResolvedIdentityIndex(h.res)
	if err != nil {
		t.Fatalf("NewResolvedIdentityIndex: %v", err)
	}
	if _, err := owners.Record(context.Background(), tenant.IdentityUser, tenant.IdentityID(66), "acme"); err != nil {
		t.Fatalf("Record: %v", err)
	}

	cred := []byte("a-credential-globex-chose")
	err = h.svc.Add(ctxFor("globex"), 66, mtlsFactor("forged", 66, cred))
	var denied *tenant.CrossTenantDeniedError
	if !errors.As(err, &denied) {
		t.Fatalf("want *tenant.CrossTenantDeniedError, got %T: %v", err, err)
	}
	for _, tenantID := range []string{"acme", "globex"} {
		if _, err := h.svc.LookupByCredentialKey(ctxFor(tenantID), auth.AuthFactorMTLS, cred); err == nil {
			t.Fatalf("the refused enrolment is resolvable under %s", tenantID)
		}
	}

	// The same refusal on the attested path, which is the other way a factor
	// is written.
	err = h.svc.(AttestationRecorder).AddAttested(ctxFor("globex"), 66,
		mtlsFactor("forged-attested", 66, []byte("another-cred")), auth.Unattested())
	if !errors.As(err, &denied) {
		t.Fatalf("AddAttested: want *tenant.CrossTenantDeniedError, got %T: %v", err, err)
	}
}

// TestRoutedFactorReadsOfAnotherTenantsUserFindNothing asserts the READ side
// takes the forgiving branch pkg/user's by-id reads take: a caller that may
// not reach the owning tenant is routed to its own, where it finds nothing.
// It must not error, and it must not disclose where the id lives.
func TestRoutedFactorReadsOfAnotherTenantsUserFindNothing(t *testing.T) {
	h := newFactorTenantHarness(t, "acme", "globex")
	owners, err := tenant.NewResolvedIdentityIndex(h.res)
	if err != nil {
		t.Fatalf("NewResolvedIdentityIndex: %v", err)
	}
	if _, err := owners.Record(context.Background(), tenant.IdentityUser, tenant.IdentityID(77), "acme"); err != nil {
		t.Fatalf("Record: %v", err)
	}
	if err := h.svc.Add(ctxFor("acme"), 77, mtlsFactor("acme-owned", 77, []byte("cred-77"))); err != nil {
		t.Fatalf("Add: %v", err)
	}

	factors, err := h.svc.List(ctxFor("globex"), 77)
	if err != nil {
		t.Fatalf("List from a foreign tenant errored: %v", err)
	}
	if len(factors) != 0 {
		t.Fatalf("List from a foreign tenant returned %s", factorIDs(factors))
	}
	has, err := h.svc.(ActiveFactorChecker).HasActiveFactor(ctxFor("globex"), 77)
	if err != nil {
		t.Fatalf("HasActiveFactor from a foreign tenant errored: %v", err)
	}
	if has {
		t.Fatal("a foreign tenant sees acme's user as having an active factor")
	}

	// The owning tenant still sees it.
	factors, err = h.svc.List(ctxFor("acme"), 77)
	if err != nil || len(factors) != 1 {
		t.Fatalf("owning tenant List = %v, %v", factorIDs(factors), err)
	}
}

// TestRoutedFactorUnindexedUserStaysWithTheCaller asserts a user id with no
// claim — a single-tenant composition records none — routes to the caller's
// own tenant on both the read and the write path, which is the behaviour that
// keeps an unrouted-looking deployment working.
func TestRoutedFactorUnindexedUserStaysWithTheCaller(t *testing.T) {
	h := newFactorTenantHarness(t, "acme")
	cred := []byte("unindexed-user-credential")
	if err := h.svc.Add(ctxFor("acme"), 4242, mtlsFactor("unindexed", 4242, cred)); err != nil {
		t.Fatalf("Add for an unindexed user: %v", err)
	}
	got, err := h.svc.LookupByCredentialKey(ctxFor("acme"), auth.AuthFactorMTLS, cred)
	if err != nil {
		t.Fatalf("LookupByCredentialKey: %v", err)
	}
	if got.ID != "unindexed" {
		t.Fatalf("resolved %q", got.ID)
	}
	factors, err := h.svc.List(ctxFor("acme"), 4242)
	if err != nil || len(factors) != 1 {
		t.Fatalf("List = %v, %v", factorIDs(factors), err)
	}
}

// TestRoutedFactorZeroUserIDReachesTheStoresValidation asserts a zero user id
// is not turned into an index lookup: the store answers with its own
// validation error, as every other caller of it expects.
func TestRoutedFactorZeroUserIDReachesTheStoresValidation(t *testing.T) {
	h := newFactorTenantHarness(t, "acme")
	var invalid *auth.InvalidFactorError

	err := h.svc.Add(ctxFor("acme"), 0, mtlsFactor("zero", 0, []byte("c")))
	if !errors.As(err, &invalid) {
		t.Fatalf("Add: want *auth.InvalidFactorError, got %T: %v", err, err)
	}
	if _, err := h.svc.List(ctxFor("acme"), 0); !errors.As(err, &invalid) {
		t.Fatalf("List: want *auth.InvalidFactorError, got %T: %v", err, err)
	}
	if _, err := h.svc.(ActiveFactorChecker).HasActiveFactor(ctxFor("acme"), 0); !errors.As(err, &invalid) {
		t.Fatalf("HasActiveFactor: want *auth.InvalidFactorError, got %T: %v", err, err)
	}
}
