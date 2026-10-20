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
	"sync/atomic"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/authfactors"
	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// ---------------------------------------------------------------------------
// stubAuthFactor — a trivial authfactors.Factor used only to exercise
// routing. It performs no real cryptography; EnrollmentID and ChallengeID are
// derived deterministically from the enrolling subject so a test can name
// them without threading return values around.
// ---------------------------------------------------------------------------

type stubAuthFactor struct{ name string }

func (f *stubAuthFactor) Name() string { return f.name }

func (f *stubAuthFactor) Enroll(_ context.Context, params *authfactors.EnrollParams) (*authfactors.Enrollment, error) {
	return &authfactors.Enrollment{
		EnrollmentID: "enroll-" + params.UserID,
		UserID:       params.UserID,
		FactorName:   f.name,
		TenantID:     params.TenantID,
		EnrolledAt:   time.Now().UTC(),
	}, nil
}

func (f *stubAuthFactor) Challenge(_ context.Context, e *authfactors.Enrollment) (*authfactors.Challenge, error) {
	return &authfactors.Challenge{
		ChallengeID:  "challenge-" + e.EnrollmentID,
		EnrollmentID: e.EnrollmentID,
		Nonce:        []byte("nonce"),
		ExpiresAt:    time.Now().Add(time.Minute).UTC(),
	}, nil
}

func (f *stubAuthFactor) Verify(_ context.Context, _ *authfactors.Challenge, _ *authfactors.Response) error {
	return nil
}

var _ authfactors.Factor = (*stubAuthFactor)(nil)

// ---------------------------------------------------------------------------
// Harness
//
// Built from the PRODUCTION resolver over the PRODUCTION in-memory store, for
// the same reason user_auth_factor_tenant_scope_test.go's harness is: the
// whole claim under test is a property of what tenant.NewStoreResolver hands
// back for a given tenant, and a stub resolver would test the stub.
// ---------------------------------------------------------------------------

type authFactorTenantHarness struct {
	shared *kvstore.MemoryKVStore
	res    tenant.StoreResolver
	svc    AuthFactorServicer
}

func newAuthFactorTenantHarness(t *testing.T, tenantIDs ...string) *authFactorTenantHarness {
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
	svc, err := NewRoutingAuthFactorService(RoutingAuthFactorServiceConfig{
		Factors:  []authfactors.Factor{&stubAuthFactor{name: "stub"}},
		Resolver: res,
	})
	if err != nil {
		t.Fatalf("NewRoutingAuthFactorService: %v", err)
	}
	return &authFactorTenantHarness{shared: shared, res: res, svc: svc}
}

// ---------------------------------------------------------------------------
// The go-qrdb#464 gate
// ---------------------------------------------------------------------------

// TestRoutedAuthFactorEnrollmentDoesNotCrossTenants is the issue's
// reproduction, asserting the fix: a custodian enrolled under one tenant's
// context must not resolve a challenge issued under another's.
func TestRoutedAuthFactorEnrollmentDoesNotCrossTenants(t *testing.T) {
	h := newAuthFactorTenantHarness(t, "acme", "globex")

	enrollment, err := h.svc.EnrollFactor(ctxFor("acme"), "stub", "custodian-1", authfactors.EnrollParams{})
	if err != nil {
		t.Fatalf("EnrollFactor under acme: %v", err)
	}

	_, err = h.svc.ChallengeFactor(ctxFor("globex"), enrollment.EnrollmentID)
	if err == nil {
		t.Fatal("globex resolved acme's enrollment")
	}
	var notFound *authfactors.EnrollmentNotFoundError
	if !errors.As(err, &notFound) {
		t.Fatalf("want *authfactors.EnrollmentNotFoundError, got %T", err)
	}

	challenge, err := h.svc.ChallengeFactor(ctxFor("acme"), enrollment.EnrollmentID)
	if err != nil {
		t.Fatalf("acme could not resolve its own enrollment: %v", err)
	}

	// VerifyFactor from the wrong tenant never reaches the per-tenant service:
	// there is no challenge cached there to find.
	if err := h.svc.VerifyFactor(ctxFor("globex"), "stub", challenge.ChallengeID, &authfactors.Response{}); err == nil {
		t.Fatal("globex verified a challenge issued under acme")
	}
	// The enrolling tenant verifies its own challenge.
	if err := h.svc.VerifyFactor(ctxFor("acme"), "stub", challenge.ChallengeID, &authfactors.Response{}); err != nil {
		t.Fatalf("acme could not verify its own challenge: %v", err)
	}
}

// TestRoutedAuthFactorEnrollRefusesMismatchedTenantID asserts EnrollFactor
// refuses when params.TenantID names a tenant other than the context's,
// rather than binding the persisted record to whichever one the caller wrote
// in the request body, or silently correcting it. Without this an
// acme-context caller could enrol a record CLAIMING to be globex's while it
// physically lands in acme's store — every transport hands
// EnrollFactorRequest.TenantID through unvalidated, so this is the only place
// that can catch it.
func TestRoutedAuthFactorEnrollRefusesMismatchedTenantID(t *testing.T) {
	h := newAuthFactorTenantHarness(t, "acme", "globex")

	_, err := h.svc.EnrollFactor(ctxFor("acme"), "stub", "custodian-spoof",
		authfactors.EnrollParams{TenantID: "globex"})
	var mismatch *AuthFactorTenantMismatchError
	if !errors.As(err, &mismatch) {
		t.Fatalf("want *AuthFactorTenantMismatchError, got %T: %v", err, err)
	}
	if mismatch.Requested != "globex" || mismatch.Context != "acme" {
		t.Fatalf("mismatch = %+v, want Requested=globex Context=acme", mismatch)
	}

	// Nothing was enrolled in either tenant.
	for _, tenantID := range []string{"acme", "globex"} {
		got, err := h.svc.ListFactors(ctxFor(tenantID), "")
		if err != nil {
			t.Fatalf("ListFactors(%s): %v", tenantID, err)
		}
		if len(got) != 0 {
			t.Fatalf("ListFactors(%s) = %v, want none after a refused enrolment", tenantID, got)
		}
	}
}

// TestRoutedAuthFactorEnrollAllowsMatchingOrEmptyTenantID asserts the
// mismatch check does not reject the two legitimate cases: a request that
// names its own context tenant explicitly, and one that names none at all —
// the latter is filled in from the context.
func TestRoutedAuthFactorEnrollAllowsMatchingOrEmptyTenantID(t *testing.T) {
	h := newAuthFactorTenantHarness(t, "acme")

	matched, err := h.svc.EnrollFactor(ctxFor("acme"), "stub", "custodian-match",
		authfactors.EnrollParams{TenantID: "acme"})
	if err != nil {
		t.Fatalf("EnrollFactor with a matching TenantID: %v", err)
	}
	if matched.TenantID != "acme" {
		t.Fatalf("TenantID = %q, want acme", matched.TenantID)
	}

	empty, err := h.svc.EnrollFactor(ctxFor("acme"), "stub", "custodian-empty",
		authfactors.EnrollParams{})
	if err != nil {
		t.Fatalf("EnrollFactor with an empty TenantID: %v", err)
	}
	if empty.TenantID != "acme" {
		t.Fatalf("TenantID = %q, want acme filled in from an empty request field", empty.TenantID)
	}

	got, err := h.svc.ListFactors(ctxFor("acme"), "")
	if err != nil {
		t.Fatalf("ListFactors: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("ListFactors = %v, want 2 enrolments", got)
	}
}

// TestRoutedAuthFactorKeysCarryTheTenantNamespace pins the physical layout
// the routing produces: the record lives under the tenant's identity
// namespace, and nothing is left at the flat root.
func TestRoutedAuthFactorKeysCarryTheTenantNamespace(t *testing.T) {
	h := newAuthFactorTenantHarness(t, "acme")
	if _, err := h.svc.EnrollFactor(ctxFor("acme"), "stub", "custodian-ns", authfactors.EnrollParams{}); err != nil {
		t.Fatalf("EnrollFactor: %v", err)
	}

	want := tenant.IdentityNamespace("acme") + sysEnrollmentPrefix + "enroll-custodian-ns"
	if v, err := h.shared.Get(context.Background(), want); err != nil || len(v) == 0 {
		t.Fatalf("no record at %q (err=%v)", want, err)
	}
	flat, err := h.shared.List(context.Background(), sysEnrollmentPrefix)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(flat) != 0 {
		t.Fatalf("routed EnrollFactor wrote %d record(s) at the untenanted root: %v", len(flat), flat)
	}
}

// TestRoutedAuthFactorListIsPerTenant asserts ListFactors("") enumerates only
// the caller's tenant, even though the fake factor derives the same
// enrollment id shape for both.
func TestRoutedAuthFactorListIsPerTenant(t *testing.T) {
	h := newAuthFactorTenantHarness(t, "acme", "globex")

	if _, err := h.svc.EnrollFactor(ctxFor("acme"), "stub", "acme-custodian", authfactors.EnrollParams{}); err != nil {
		t.Fatalf("EnrollFactor acme: %v", err)
	}
	if _, err := h.svc.EnrollFactor(ctxFor("globex"), "stub", "globex-custodian", authfactors.EnrollParams{}); err != nil {
		t.Fatalf("EnrollFactor globex: %v", err)
	}

	for _, tc := range []struct{ tenantID, wantSubject string }{
		{"acme", "acme-custodian"},
		{"globex", "globex-custodian"},
	} {
		got, err := h.svc.ListFactors(ctxFor(tc.tenantID), "")
		if err != nil {
			t.Fatalf("ListFactors(%s): %v", tc.tenantID, err)
		}
		if len(got) != 1 || got[0].UserID != tc.wantSubject {
			t.Fatalf("ListFactors(%s) = %v, want exactly [%s]", tc.tenantID, got, tc.wantSubject)
		}
	}
}

// TestRoutedAuthFactorRevokeStaysInTenant asserts RevokeFactor from another
// tenant finds nothing rather than acting on someone else's enrollment.
func TestRoutedAuthFactorRevokeStaysInTenant(t *testing.T) {
	h := newAuthFactorTenantHarness(t, "acme", "globex")
	enrollment, err := h.svc.EnrollFactor(ctxFor("acme"), "stub", "only-in-acme", authfactors.EnrollParams{})
	if err != nil {
		t.Fatalf("EnrollFactor: %v", err)
	}

	if err := h.svc.RevokeFactor(ctxFor("globex"), enrollment.EnrollmentID); err == nil {
		t.Fatal("RevokeFactor from another tenant succeeded")
	}
	if err := h.svc.RevokeFactor(ctxFor("acme"), enrollment.EnrollmentID); err != nil {
		t.Fatalf("the owning tenant could not revoke its own enrollment: %v", err)
	}
}

// freshWrapper is a comparable struct whose IDENTITY is nonetheless fresh on
// every construction (each is a new heap allocation), embedding the real
// store it delegates to. It exists to simulate a StoreResolver that builds a
// lightweight per-call wrapper rather than returning a cached, stable value —
// a shape StoreResolver's interface contract never ruled out.
//
// CompareAndDelete is forwarded explicitly rather than left to promotion:
// the embedded field's STATIC type is the kvstore.KVStore interface, so Go
// promotes only KVStore's own methods, not whatever extra capability the
// dynamic value underneath happens to have. A real per-call wrapper a
// production StoreResolver returns would face the identical requirement —
// see kvstore/namespaced.go's atomicNamespacedKVStore, which exists for
// exactly this reason — so forwarding it here is the accurate stand-in, not
// a test-only accommodation.
type freshWrapper struct{ kvstore.KVStore }

// CompareAndDelete forwards to the wrapped store's AtomicKVStore capability
// when present, so a freshWrapper accurately represents a resolver whose
// underlying store supports atomic challenge consumption.
func (w *freshWrapper) CompareAndDelete(ctx context.Context, key string, expected []byte) (bool, error) {
	atomicKV, ok := w.KVStore.(kvstore.AtomicKVStore)
	if !ok {
		return false, kvstore.ErrAtomicWriteUnsupported
	}
	return atomicKV.CompareAndDelete(ctx, key, expected)
}

// freshWrapperResolver returns a NEW freshWrapper instance around the SAME
// underlying store on every single call.
type freshWrapperResolver struct{ store kvstore.KVStore }

func (r freshWrapperResolver) Resolve(context.Context, string) (kvstore.KVStore, error) {
	return &freshWrapper{r.store}, nil
}

// TestRoutedAuthFactorServiceWorksAcrossFreshWrapperInstances covers a review
// finding: a per-tenant cache keyed on the resolved store's IDENTITY assumed
// Resolve returns the same Go value across calls, which StoreResolver's
// contract never promised. A resolver that legitimately hands back a new,
// merely-comparable wrapper on every call defeated that cache exactly as a
// non-comparable store did — ChallengeFactor and VerifyFactor would each
// build (and, under the old design, cache keyed on) a DIFFERENT wrapper
// value, so the pending challenge ChallengeFactor held in memory was
// invisible to VerifyFactor moments later, and verification failed as
// not-found on every call. Since authFactorService now persists the pending
// challenge in the store rather than in its own memory (and the router
// builds no cache to keep coherent at all), this passes regardless of
// whether the two calls' service instances — or their resolved wrapper
// values — are the same object.
func TestRoutedAuthFactorServiceWorksAcrossFreshWrapperInstances(t *testing.T) {
	resolver := freshWrapperResolver{store: kvstore.NewMemoryKVStore()}
	svc, err := NewRoutingAuthFactorService(RoutingAuthFactorServiceConfig{
		Factors:  []authfactors.Factor{&stubAuthFactor{name: "stub"}},
		Resolver: resolver,
	})
	if err != nil {
		t.Fatalf("NewRoutingAuthFactorService: %v", err)
	}
	ctx := ctxFor("acme")

	enrollment, err := svc.EnrollFactor(ctx, "stub", "custodian-1", authfactors.EnrollParams{})
	if err != nil {
		t.Fatalf("EnrollFactor: %v", err)
	}
	challenge, err := svc.ChallengeFactor(ctx, enrollment.EnrollmentID)
	if err != nil {
		t.Fatalf("ChallengeFactor: %v", err)
	}
	if err := svc.VerifyFactor(ctx, "stub", challenge.ChallengeID, &authfactors.Response{}); err != nil {
		t.Fatalf("VerifyFactor: %v", err)
	}
}

// transitioningResolver returns a different, comparable store the SECOND
// and later time it is asked for a tenant — simulating an isolation
// transition, where the same tenant ID starts resolving to a genuinely
// different store mid-process (a prefix tenant migrated to a dedicated
// barrier, say).
type transitioningResolver struct {
	first, second kvstore.KVStore
	calls         int
}

func (r *transitioningResolver) Resolve(context.Context, string) (kvstore.KVStore, error) {
	r.calls++
	if r.calls == 1 {
		return r.first, nil
	}
	return r.second, nil
}

// TestRoutedAuthFactorServiceFollowsIsolationTransition asserts the cache is
// keyed on the resolved store's identity, not on the tenant ID: when the
// resolver starts returning a different store for the same tenant, the
// router must follow it rather than keep answering out of the first store it
// ever saw for that tenant. A tenant-ID-keyed cache would pass every other
// test in this file and fail only this one.
func TestRoutedAuthFactorServiceFollowsIsolationTransition(t *testing.T) {
	resolver := &transitioningResolver{
		first:  kvstore.NewMemoryKVStore(),
		second: kvstore.NewMemoryKVStore(),
	}
	svc, err := NewRoutingAuthFactorService(RoutingAuthFactorServiceConfig{
		Factors:  []authfactors.Factor{&stubAuthFactor{name: "stub"}},
		Resolver: resolver,
	})
	if err != nil {
		t.Fatalf("NewRoutingAuthFactorService: %v", err)
	}
	ctx := ctxFor("acme")

	enrollment, err := svc.EnrollFactor(ctx, "stub", "custodian-1", authfactors.EnrollParams{})
	if err != nil {
		t.Fatalf("EnrollFactor (pre-transition): %v", err)
	}

	// The resolver now answers "acme" with a different store. If the cache
	// were keyed on the tenant ID, this would still resolve out of the store
	// enrolled above; keyed on store identity, it must not.
	if _, err := svc.ChallengeFactor(ctx, enrollment.EnrollmentID); err == nil {
		t.Fatal("resolved a challenge against the store from before the isolation transition")
	}

	if _, err := svc.EnrollFactor(ctx, "stub", "custodian-1", authfactors.EnrollParams{}); err != nil {
		t.Fatalf("EnrollFactor (post-transition): %v", err)
	}

	oldKeys, err := resolver.first.List(context.Background(), sysEnrollmentPrefix)
	if err != nil {
		t.Fatalf("List old store: %v", err)
	}
	if len(oldKeys) != 1 {
		t.Fatalf("old store has %d record(s), want exactly the pre-transition one", len(oldKeys))
	}
	newKeys, err := resolver.second.List(context.Background(), sysEnrollmentPrefix)
	if err != nil {
		t.Fatalf("List new store: %v", err)
	}
	if len(newKeys) != 1 {
		t.Fatalf("new store has %d record(s), want exactly the post-transition one", len(newKeys))
	}
}

// TestRoutedAuthFactorServiceFailsClosedOnSealedTenant asserts a resolver
// refusal reaches the caller on every verb rather than being folded into a
// not-found. sealedResolver is defined in user_auth_factor_tenant_scope_test.go.
func TestRoutedAuthFactorServiceFailsClosedOnSealedTenant(t *testing.T) {
	svc, err := NewRoutingAuthFactorService(RoutingAuthFactorServiceConfig{
		Factors:  []authfactors.Factor{&stubAuthFactor{name: "stub"}},
		Resolver: sealedResolver{},
	})
	if err != nil {
		t.Fatalf("NewRoutingAuthFactorService: %v", err)
	}
	ctx := ctxFor("sealed-tenant")

	checks := map[string]func() error{
		"EnrollFactor":    func() error { _, e := svc.EnrollFactor(ctx, "stub", "x", authfactors.EnrollParams{}); return e },
		"ChallengeFactor": func() error { _, e := svc.ChallengeFactor(ctx, "x"); return e },
		"VerifyFactor":    func() error { return svc.VerifyFactor(ctx, "stub", "x", &authfactors.Response{}) },
		"ListFactors":     func() error { _, e := svc.ListFactors(ctx, ""); return e },
		"RevokeFactor":    func() error { return svc.RevokeFactor(ctx, "x") },
	}
	for name, call := range checks {
		if err := call(); !errors.Is(err, tenant.ErrTenantSealed) {
			t.Errorf("%s: want tenant.ErrTenantSealed, got %v", name, err)
		}
	}
}

// sealAfterResolveResolver resolves every request to the SAME real
// barrier-backed tenant store (seal.NewTenantBarrierKVStore over a real
// *seal.StorageBarrier — the store an isolated tenant's home store wraps). Once armed, it seals the real barrier AFTER
// deciding the resolution succeeded and BEFORE returning the store: the
// resolution itself saw an unsealed tenant, so the first store operation the
// request makes is the one that meets the seal.
type sealAfterResolveResolver struct {
	store   kvstore.KVStore
	barrier *seal.StorageBarrier
	armed   atomic.Bool
}

func (r *sealAfterResolveResolver) Resolve(context.Context, string) (kvstore.KVStore, error) {
	if r.armed.CompareAndSwap(true, false) {
		if err := r.barrier.Seal(); err != nil {
			return nil, err
		}
	}
	return r.store, nil
}

// newSealAfterResolveResolver builds the resolver over an initialized,
// unsealed real barrier.
func newSealAfterResolveResolver(t *testing.T) *sealAfterResolveResolver {
	t.Helper()
	barrier, _ := newTestSoftwareBarrier(t)
	creds, err := seal.CredentialsFor(context.Background(), []byte("test-secret-32-bytes-long-enough"))
	if err != nil {
		t.Fatalf("CredentialsFor: %v", err)
	}
	if err := barrier.Initialize(context.Background(), creds); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	seal.DestroyCredentials(creds)
	store, err := seal.NewTenantBarrierKVStore(seal.WrapTenantBarrier("acme", barrier))
	if err != nil {
		t.Fatalf("NewTenantBarrierKVStore: %v", err)
	}
	return &sealAfterResolveResolver{store: store, barrier: barrier}
}

// TestRoutedAuthFactorServiceSurfacesSealAfterResolve covers the race the
// sealed-RESOLVER test above cannot reach: Resolve succeeds, and the tenant's
// real barrier seals before the request's first store operation. That
// operation fails with the barrier's own sentinel, seal.ErrSealed — distinct
// from the tenant.ErrTenantSealed Resolve returns for an ALREADY-sealed
// tenant — and it must reach the caller as itself on every verb, not folded
// into a not-found. The transports classify seal.ErrSealed identically to
// tenant.ErrTenantSealed; their tests assert that mapping.
func TestRoutedAuthFactorServiceSurfacesSealAfterResolve(t *testing.T) {
	calls := map[string]func(svc AuthFactorServicer, ctx context.Context, enrollmentID, challengeID string) error{
		"EnrollFactor": func(svc AuthFactorServicer, ctx context.Context, _, _ string) error {
			_, err := svc.EnrollFactor(ctx, "stub", "custodian-2", authfactors.EnrollParams{})
			return err
		},
		"ChallengeFactor": func(svc AuthFactorServicer, ctx context.Context, enrollmentID, _ string) error {
			_, err := svc.ChallengeFactor(ctx, enrollmentID)
			return err
		},
		"VerifyFactor": func(svc AuthFactorServicer, ctx context.Context, _, challengeID string) error {
			return svc.VerifyFactor(ctx, "stub", challengeID, &authfactors.Response{})
		},
		"ListFactors": func(svc AuthFactorServicer, ctx context.Context, _, _ string) error {
			_, err := svc.ListFactors(ctx, "")
			return err
		},
		"RevokeFactor": func(svc AuthFactorServicer, ctx context.Context, enrollmentID, _ string) error {
			return svc.RevokeFactor(ctx, enrollmentID)
		},
	}
	for name, call := range calls {
		t.Run(name, func(t *testing.T) {
			resolver := newSealAfterResolveResolver(t)
			svc, err := NewRoutingAuthFactorService(RoutingAuthFactorServiceConfig{
				Factors:  []authfactors.Factor{&stubAuthFactor{name: "stub"}},
				Resolver: resolver,
			})
			if err != nil {
				t.Fatalf("NewRoutingAuthFactorService: %v", err)
			}
			ctx := ctxFor("acme")
			enrollment, err := svc.EnrollFactor(ctx, "stub", "custodian-1", authfactors.EnrollParams{})
			if err != nil {
				t.Fatalf("EnrollFactor (unsealed): %v", err)
			}
			challenge, err := svc.ChallengeFactor(ctx, enrollment.EnrollmentID)
			if err != nil {
				t.Fatalf("ChallengeFactor (unsealed): %v", err)
			}

			resolver.armed.Store(true)
			err = call(svc, ctx, enrollment.EnrollmentID, challenge.ChallengeID)
			if !errors.Is(err, seal.ErrSealed) {
				t.Fatalf("%s after a seal between Resolve and the store op = %v, want seal.ErrSealed", name, err)
			}
			var notFound *authfactors.EnrollmentNotFoundError
			if errors.As(err, &notFound) {
				t.Errorf("%s folded the seal into a not-found: %v", name, err)
			}
		})
	}
}

// TestNewRoutingAuthFactorServiceRequiresResolver asserts the constructor
// refuses rather than producing a service with no tenant dimension.
func TestNewRoutingAuthFactorServiceRequiresResolver(t *testing.T) {
	svc, err := NewRoutingAuthFactorService(RoutingAuthFactorServiceConfig{})
	if err == nil {
		t.Fatal("nil resolver was accepted")
	}
	if svc != nil {
		t.Fatal("a service was returned alongside the error")
	}
	var invalid *AuthFactorRoutingConfigError
	if !errors.As(err, &invalid) {
		t.Fatalf("want *AuthFactorRoutingConfigError, got %T", err)
	}
	if got, want := invalid.Error(), "service: auth-factor routing config: store resolver is required"; got != want {
		t.Fatalf("Error() = %q, want %q", got, want)
	}
}

// TestUnroutedAuthFactorServiceAcceptsAKVStore asserts NewAuthFactorService
// builds over a kvstore.KVStore as well as a KVServicer — the narrowing that
// lets the router hand it a resolved store directly.
func TestUnroutedAuthFactorServiceAcceptsAKVStore(t *testing.T) {
	store := kvstore.NewMemoryKVStore()
	svc, err := NewAuthFactorService(AuthFactorServiceConfig{
		Factors: []authfactors.Factor{&stubAuthFactor{name: "stub"}},
		KV:      store,
	})
	if err != nil {
		t.Fatalf("NewAuthFactorService over a KVStore: %v", err)
	}
	enrollment, err := svc.EnrollFactor(context.Background(), "stub", "direct", authfactors.EnrollParams{})
	if err != nil {
		t.Fatalf("EnrollFactor: %v", err)
	}
	if _, err := svc.ChallengeFactor(context.Background(), enrollment.EnrollmentID); err != nil {
		t.Fatalf("ChallengeFactor: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Non-comparable stores
// ---------------------------------------------------------------------------

// nonComparableAuthKV is a valid kvstore.KVStore whose dynamic type is NOT
// comparable — a slice, mirroring pkg/invite's nonComparableKV — delegating
// every call to the real store it wraps. kvstore.KVStore is an interface and
// nothing requires implementations to be comparable, and StoreResolver.Resolve
// returns the interface, so a caller may legitimately hand the router a value
// like this one.
type nonComparableAuthKV []kvstore.KVStore

func (n nonComparableAuthKV) real() kvstore.KVStore { return n[0] }

func (n nonComparableAuthKV) Put(ctx context.Context, key string, value []byte) error {
	return n.real().Put(ctx, key, value)
}

func (n nonComparableAuthKV) Get(ctx context.Context, key string) ([]byte, error) {
	return n.real().Get(ctx, key)
}

func (n nonComparableAuthKV) Delete(ctx context.Context, key string) error {
	return n.real().Delete(ctx, key)
}

func (n nonComparableAuthKV) Scan(ctx context.Context, prefix string, fn func(string, []byte) error) error {
	return n.real().Scan(ctx, prefix, fn)
}

func (n nonComparableAuthKV) List(ctx context.Context, prefix string) ([]string, error) {
	return n.real().List(ctx, prefix)
}

func (n nonComparableAuthKV) Exists(ctx context.Context, key string) (bool, error) {
	return n.real().Exists(ctx, key)
}

func (n nonComparableAuthKV) RegisterEntityIndexes(entityType string, indexes []kvstore.EntityIndex) error {
	return n.real().RegisterEntityIndexes(entityType, indexes)
}

func (n nonComparableAuthKV) QueryIndex(ctx context.Context, indexName string, value []byte) ([][]byte, error) {
	return n.real().QueryIndex(ctx, indexName, value)
}

func (n nonComparableAuthKV) ScanIndex(ctx context.Context, indexName string, start, end []byte) (map[string][][]byte, error) {
	return n.real().ScanIndex(ctx, indexName, start, end)
}

func (n nonComparableAuthKV) QueryCompoundIndex(ctx context.Context, indexName string, values [][]byte) ([][]byte, error) {
	return n.real().QueryCompoundIndex(ctx, indexName, values)
}

func (n nonComparableAuthKV) ScanCompoundIndex(ctx context.Context, indexName string, prefixValues [][]byte, rangeStart, rangeEnd []byte) (map[string][][]byte, error) {
	return n.real().ScanCompoundIndex(ctx, indexName, prefixValues, rangeStart, rangeEnd)
}

// CompareAndDelete forwards to the wrapped store's AtomicKVStore capability,
// exactly as freshWrapper's does and for the same reason: a slice type has
// no promoted methods at all, so every capability the underlying store has
// must be forwarded explicitly for this wrapper to be an accurate stand-in.
func (n nonComparableAuthKV) CompareAndDelete(ctx context.Context, key string, expected []byte) (bool, error) {
	atomicKV, ok := n.real().(kvstore.AtomicKVStore)
	if !ok {
		return false, kvstore.ErrAtomicWriteUnsupported
	}
	return atomicKV.CompareAndDelete(ctx, key, expected)
}

var _ kvstore.KVStore = nonComparableAuthKV(nil)

// nonComparableAuthResolver hands back a fresh non-comparable wrapper value
// around the same underlying store on every call, the way a resolver that
// builds a lightweight view per call legitimately could.
type nonComparableAuthResolver struct{ store kvstore.KVStore }

func (r nonComparableAuthResolver) Resolve(context.Context, string) (kvstore.KVStore, error) {
	return nonComparableAuthKV{r.store}, nil
}

// TestRoutedAuthFactorServiceWorksOverANonComparableStore pins the same
// statelessness property TestRoutedAuthFactorServiceWorksAcrossFreshWrapperInstances
// does, specifically for a store whose dynamic type cannot be a map key at
// all — the case that originally motivated (and, across three review rounds,
// defeated) every identity- or tenant-ID-keyed caching scheme this router
// tried. There is no comparability-dependent code left to exercise; this
// reproduces the exact Enroll → Challenge → Verify sequence over such a store
// as a regression pin.
func TestRoutedAuthFactorServiceWorksOverANonComparableStore(t *testing.T) {
	resolver := nonComparableAuthResolver{store: kvstore.NewMemoryKVStore()}
	svc, err := NewRoutingAuthFactorService(RoutingAuthFactorServiceConfig{
		Factors:  []authfactors.Factor{&stubAuthFactor{name: "stub"}},
		Resolver: resolver,
	})
	if err != nil {
		t.Fatalf("NewRoutingAuthFactorService: %v", err)
	}
	ctx := ctxFor("acme")

	enrollment, err := svc.EnrollFactor(ctx, "stub", "custodian-nc", authfactors.EnrollParams{})
	if err != nil {
		t.Fatalf("EnrollFactor: %v", err)
	}
	challenge, err := svc.ChallengeFactor(ctx, enrollment.EnrollmentID)
	if err != nil {
		t.Fatalf("ChallengeFactor: %v", err)
	}
	if err := svc.VerifyFactor(ctx, "stub", challenge.ChallengeID, &authfactors.Response{}); err != nil {
		t.Fatalf("VerifyFactor over a non-comparable store: %v", err)
	}
}
