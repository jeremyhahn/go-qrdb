// Copyright (c) 2026 Jeremy Hahn
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

package firstboot

import (
	"context"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/dao"
	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/user"
)

// These tests pin go-qrdb#517's first-boot half: under
// auth.mtls_binding_policy "required", a configuration that would leave the
// bootstrap admin unable to log in by certificate is refused, and refused
// BEFORE anything is written. They run against the REAL kvstore-backed user
// store, because the check reads it: fakeUsers answers every lookup with
// (nil, nil), which no real store ever returns, and a posture check proven
// against it would be proven against a store that does not exist.

func newRealUsers(t *testing.T) user.UserServicer {
	t.Helper()
	gen, err := dao.NewSnowflakeGenerator(1)
	if err != nil {
		t.Fatalf("NewSnowflakeGenerator: %v", err)
	}
	svc, err := user.NewStore(kvstore.NewMemoryKVStore(), gen)
	if err != nil {
		t.Fatalf("user.NewStore: %v", err)
	}
	return svc
}

// storeBinder is the CertBinder a composer writes over qrdb's user store:
// BindAdminCert enrols the thumbprint through the canonical verb.
type storeBinder struct{ users user.UserServicer }

func (b storeBinder) BindAdminCert(ctx context.Context, _ string, userID uint64, thumbprint string) error {
	_, err := b.users.AddCertBinding(ctx, userID, thumbprint, 0)
	return err
}

// creatorOnly satisfies UserCreator and nothing else, as an adapter over a
// foreign IAM stack might. The store is a named field, not embedded, so no
// lookup method is promoted onto it.
type creatorOnly struct{ users user.UserServicer }

func (c creatorOnly) CreateUser(ctx context.Context, req *user.CreateUserRequest) (*user.User, error) {
	return c.users.CreateUser(ctx, req)
}

// failingLookup answers every lookup with a transient error.
type failingLookup struct{ err error }

func (f failingLookup) GetUserByUsername(context.Context, string, string) (*user.User, error) {
	return nil, f.err
}

func requireRefusal(t *testing.T, err error, want AdminCertBindingReason) {
	t.Helper()
	var refusal *AdminCertBindingRequiredError
	if !errors.As(err, &refusal) {
		t.Fatalf("err = %T(%v), want *AdminCertBindingRequiredError(%s)", err, err, want)
	}
	if refusal.Reason != want || refusal.Username != adminUsername || refusal.TenantID != tenant.SystemTenantID {
		t.Fatalf("refusal = %+v, want reason %q for system/admin", refusal, want)
	}
	for _, s := range []string{`"required"`, `"enrolled"`, "users:add_cert_binding", string(want)} {
		if !strings.Contains(err.Error(), s) {
			t.Fatalf("error %q does not name %q", err.Error(), s)
		}
	}
}

func createAdmin(t *testing.T, users user.UserServicer) *user.User {
	t.Helper()
	u, err := users.CreateUser(context.Background(), &user.CreateUserRequest{
		Username: adminUsername, TenantID: tenant.SystemTenantID, WithoutEmailLoginKey: true,
	})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	return u
}

func TestVerifyAdminCertBinding(t *testing.T) {
	ctx := context.Background()
	now := time.Now().UTC()

	t.Run("nil lookup", func(t *testing.T) {
		requireRefusal(t, VerifyAdminCertBinding(ctx, nil, now), AdminBindingUnverifiable)
	})
	t.Run("admin missing", func(t *testing.T) {
		requireRefusal(t, VerifyAdminCertBinding(ctx, newRealUsers(t), now), AdminBindingMissing)
	})
	t.Run("admin unbound", func(t *testing.T) {
		users := newRealUsers(t)
		createAdmin(t, users)
		requireRefusal(t, VerifyAdminCertBinding(ctx, users, now), AdminBindingNone)
	})
	t.Run("admin bound", func(t *testing.T) {
		users := newRealUsers(t)
		admin := createAdmin(t, users)
		if _, err := users.AddCertBinding(ctx, admin.ID, user.ComputeSPKIThumbprint(parseTestCert(t, realCertPEM(t))), 0); err != nil {
			t.Fatalf("AddCertBinding: %v", err)
		}
		if err := VerifyAdminCertBinding(ctx, users, now); err != nil {
			t.Fatalf("bound admin refused: %v", err)
		}
	})
	t.Run("every binding expired", func(t *testing.T) {
		users := newRealUsers(t)
		admin := createAdmin(t, users)
		notAfter := now.Add(time.Hour)
		if _, err := users.AddCertBinding(ctx, admin.ID, user.ComputeSPKIThumbprint(parseTestCert(t, realCertPEM(t))), notAfter.Unix()); err != nil {
			t.Fatalf("AddCertBinding: %v", err)
		}
		if err := VerifyAdminCertBinding(ctx, users, now); err != nil {
			t.Fatalf("live binding refused: %v", err)
		}
		requireRefusal(t, VerifyAdminCertBinding(ctx, users, notAfter.Add(time.Second)), AdminBindingAllExpired)
	})
	t.Run("lookup fails", func(t *testing.T) {
		cause := errors.New("raft: read index not ready")
		err := VerifyAdminCertBinding(ctx, failingLookup{err: cause}, now)
		var le *AdminLookupError
		if !errors.As(err, &le) || !errors.Is(err, cause) {
			t.Fatalf("err = %v, want *AdminLookupError wrapping the cause", err)
		}
		if !strings.Contains(err.Error(), cause.Error()) {
			t.Fatalf("error %q hides its cause", err.Error())
		}
	})
}

// TestInitialize_Required_RefusesBeforeWriting covers the fresh-cluster cases:
// the admin does not exist yet, and this run could not bind it. Nothing may be
// written -- no tenant, no role, no admin.
func TestInitialize_Required_RefusesBeforeWriting(t *testing.T) {
	cases := []struct {
		name   string
		deps   func(Dependencies) Dependencies
		reason AdminCertBindingReason
	}{
		{"no binder", func(d Dependencies) Dependencies {
			d.CA = &fakeCA{cert: realCertPEM(t), key: []byte("KEY")}
			return d
		}, AdminBindingNoBinder},
		{"binder without a certificate path", func(d Dependencies) Dependencies {
			d.Binder = &fakeBinder{}
			return d
		}, AdminBindingNoCertPath},
		{"binder with a CSR but no signer", func(d Dependencies) Dependencies {
			d.Binder = &fakeBinder{}
			d.AdminCSRPEM = []byte("csr")
			return d
		}, AdminBindingNoCertPath},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			users := newRealUsers(t)
			tenants, roles := &fakeTenants{}, &fakeRoles{}
			_, err := Initialize(context.Background(), tc.deps(Dependencies{
				Tenants: tenants, Users: users, Roles: roles, Logger: discardLogger(),
				MTLSBindingPolicy: auth.MTLSBindingRequired,
			}))
			requireRefusal(t, err, tc.reason)
			if len(tenants.createCalls) != 0 || len(roles.createCalls) != 0 {
				t.Fatalf("refused first boot wrote: tenants=%d roles=%d", len(tenants.createCalls), len(roles.createCalls))
			}
			if _, lerr := users.GetUserByUsername(context.Background(), tenant.SystemTenantID, adminUsername); lerr == nil {
				t.Fatal("refused first boot created the admin")
			}
		})
	}
}

// TestInitialize_Required_BindsAndBoots is the configuration that satisfies
// the posture: a CA and a Binder. The admin is created, bound, and the boot
// check a composer runs afterwards passes.
func TestInitialize_Required_BindsAndBoots(t *testing.T) {
	users := newRealUsers(t)
	result, err := Initialize(context.Background(), Dependencies{
		Tenants: &fakeTenants{}, Users: users, Roles: &fakeRoles{}, Logger: discardLogger(),
		CA:                &fakeCA{cert: realCertPEM(t), key: []byte("KEY")},
		Binder:            storeBinder{users: users},
		MTLSBindingPolicy: auth.MTLSBindingRequired,
	})
	if err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	if !result.AdminCreated || result.AdminCertThumbprint == "" {
		t.Fatalf("result = %+v, want a created and bound admin", result)
	}
	if err := VerifyAdminCertBinding(context.Background(), users, time.Now().UTC()); err != nil {
		t.Fatalf("admin bound by first boot fails the boot check: %v", err)
	}
	// A re-run finds the bound admin and passes too.
	if _, err := Initialize(context.Background(), Dependencies{
		Tenants: &fakeTenants{}, Users: users, Roles: &fakeRoles{}, Logger: discardLogger(),
		MTLSBindingPolicy: auth.MTLSBindingRequired,
	}); err != nil {
		t.Fatalf("re-run over a bound admin: %v", err)
	}
}

// TestInitialize_Required_ExistingUnboundAdminRefused: a re-run issues no
// certificate, so an existing admin with no binding is the lockout, whatever
// is wired.
func TestInitialize_Required_ExistingUnboundAdminRefused(t *testing.T) {
	users := newRealUsers(t)
	createAdmin(t, users)
	_, err := Initialize(context.Background(), Dependencies{
		Tenants: &fakeTenants{}, Users: users, Roles: &fakeRoles{}, Logger: discardLogger(),
		CA:                &fakeCA{cert: realCertPEM(t), key: []byte("KEY")},
		Binder:            storeBinder{users: users},
		MTLSBindingPolicy: auth.MTLSBindingRequired,
	})
	requireRefusal(t, err, AdminBindingNone)
}

// TestInitialize_Required_UnverifiableUsersRefused: an adapter that can only
// create users cannot show the posture holds, so it refuses rather than
// assuming.
func TestInitialize_Required_UnverifiableUsersRefused(t *testing.T) {
	users := newRealUsers(t)
	_, err := Initialize(context.Background(), Dependencies{
		Tenants: &fakeTenants{}, Users: creatorOnly{users: users}, Roles: &fakeRoles{}, Logger: discardLogger(),
		CA:                &fakeCA{cert: realCertPEM(t), key: []byte("KEY")},
		Binder:            storeBinder{users: users},
		MTLSBindingPolicy: auth.MTLSBindingRequired,
	})
	requireRefusal(t, err, AdminBindingUnverifiable)
}

// TestInitialize_Required_LookupFailureRefused: a transient read failure is not
// a verdict either way, and "required" does not boot on no verdict.
func TestInitialize_Required_LookupFailureRefused(t *testing.T) {
	err := checkBindingPosture(context.Background(), Dependencies{
		Users:             lookupUsers{failingLookup: failingLookup{err: errors.New("transient")}},
		MTLSBindingPolicy: auth.MTLSBindingRequired,
	})
	var le *AdminLookupError
	if !errors.As(err, &le) {
		t.Fatalf("err = %v, want *AdminLookupError", err)
	}
}

// lookupUsers is a UserCreator whose lookup fails.
type lookupUsers struct{ failingLookup }

func (lookupUsers) CreateUser(context.Context, *user.CreateUserRequest) (*user.User, error) {
	return nil, errors.New("not reached")
}

// TestInitialize_Enrolled_UnchangedWithoutBinder is the control: the default
// posture boots with no Binder exactly as before.
func TestInitialize_Enrolled_UnchangedWithoutBinder(t *testing.T) {
	result, err := Initialize(context.Background(), Dependencies{
		Tenants: &fakeTenants{}, Users: newRealUsers(t), Roles: &fakeRoles{}, Logger: discardLogger(),
	})
	if err != nil || !result.AdminCreated {
		t.Fatalf("Initialize = (%+v, %v), want a created admin", result, err)
	}
}

func parseTestCert(t *testing.T, certPEM []byte) *x509.Certificate {
	t.Helper()
	block, _ := pem.Decode(certPEM)
	if block == nil {
		t.Fatal("certificate PEM did not decode")
	}
	c, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatalf("ParseCertificate: %v", err)
	}
	return c
}

// forgetfulUsers creates nothing and reports success with no user -- a user
// surface that "created" the admin without returning it.
type forgetfulUsers struct{ users user.UserServicer }

func (f forgetfulUsers) CreateUser(context.Context, *user.CreateUserRequest) (*user.User, error) {
	return nil, nil
}

func (f forgetfulUsers) GetUserByUsername(ctx context.Context, tenantID, username string) (*user.User, error) {
	return f.users.GetUserByUsername(ctx, tenantID, username)
}

// TestInitialize_Required_VerifiesTheOutcome: the pre-flight checks the inputs;
// these are the ways the inputs can look right and the outcome still lock the
// admin out. Each must fail first boot rather than report success.
func TestInitialize_Required_VerifiesTheOutcome(t *testing.T) {
	t.Run("CA returned an empty certificate", func(t *testing.T) {
		users := newRealUsers(t)
		_, err := Initialize(context.Background(), Dependencies{
			Tenants: &fakeTenants{}, Users: users, Roles: &fakeRoles{}, Logger: discardLogger(),
			CA:                &fakeCA{cert: nil, key: nil},
			Binder:            storeBinder{users: users},
			MTLSBindingPolicy: auth.MTLSBindingRequired,
		})
		requireRefusal(t, err, AdminBindingNone)
	})
	t.Run("binder reported success without binding", func(t *testing.T) {
		users := newRealUsers(t)
		_, err := Initialize(context.Background(), Dependencies{
			Tenants: &fakeTenants{}, Users: users, Roles: &fakeRoles{}, Logger: discardLogger(),
			CA:                &fakeCA{cert: realCertPEM(t), key: []byte("KEY")},
			Binder:            &fakeBinder{},
			MTLSBindingPolicy: auth.MTLSBindingRequired,
		})
		requireRefusal(t, err, AdminBindingNone)
	})
	t.Run("user surface returned no admin", func(t *testing.T) {
		users := newRealUsers(t)
		_, err := Initialize(context.Background(), Dependencies{
			Tenants: &fakeTenants{}, Users: forgetfulUsers{users: users}, Roles: &fakeRoles{}, Logger: discardLogger(),
			CA:                &fakeCA{cert: realCertPEM(t), key: []byte("KEY")},
			Binder:            storeBinder{users: users},
			MTLSBindingPolicy: auth.MTLSBindingRequired,
		})
		requireRefusal(t, err, AdminBindingMissing)
	})
}

// TestVerifyAdminBindings_LoginGatesBeyondTheBinding (go-qrdb#517 review N4):
// a live binding is not a login. An admin whose lifecycle forbids login, or
// whose permitted methods exclude mtls, is locked out all the same.
func TestVerifyAdminBindings_LoginGatesBeyondTheBinding(t *testing.T) {
	now := time.Now().UTC()
	live := []user.CertBinding{{Thumbprint: "t", NotAfter: now.Add(time.Hour)}}
	admin := func(mut func(u *user.User)) *user.User {
		u := &user.User{Username: adminUsername, TenantID: tenant.SystemTenantID, Lifecycle: user.LifecycleActive, CertBindings: live}
		mut(u)
		return u
	}
	cases := []struct {
		name string
		u    *user.User
		want AdminCertBindingReason
	}{
		{"suspended", admin(func(u *user.User) { u.Lifecycle = user.LifecycleSuspended }), AdminBindingCannotAuthenticate},
		{"disabled", admin(func(u *user.User) { u.Lifecycle = user.LifecycleDisabled }), AdminBindingCannotAuthenticate},
		{"no lifecycle", admin(func(u *user.User) { u.Lifecycle = "" }), AdminBindingCannotAuthenticate},
		{"mtls not permitted", admin(func(u *user.User) { u.AuthMethods = []string{"webauthn"} }), AdminBindingMTLSNotPermitted},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			requireRefusal(t, verifyAdminBindings(tc.u, now), tc.want)
		})
	}
	for name, u := range map[string]*user.User{
		"active, no method policy":     admin(func(*user.User) {}),
		"active, mtls among permitted": admin(func(u *user.User) { u.AuthMethods = []string{"webauthn", "mtls"} }),
	} {
		if err := verifyAdminBindings(u, now); err != nil {
			t.Errorf("%s: err = %v, want nil", name, err)
		}
	}
}

// blockingLookup never answers until its context is done.
type blockingLookup struct{ calls int }

func (b *blockingLookup) GetUserByUsername(ctx context.Context, _, _ string) (*user.User, error) {
	b.calls++
	<-ctx.Done()
	return nil, ctx.Err()
}

// TestVerifyAdminCertBindingWithin_DeadlineCutsOffABlockedLookup (N3): a
// lookup that hangs instead of failing is bounded by the same deadline as the
// retries.
func TestVerifyAdminCertBindingWithin_DeadlineCutsOffABlockedLookup(t *testing.T) {
	lookup := &blockingLookup{}
	start := time.Now()
	err := VerifyAdminCertBindingWithin(context.Background(), lookup, 150*time.Millisecond)
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("a blocked lookup held the check for %v past its bound", elapsed)
	}
	var le *AdminLookupError
	if !errors.As(err, &le) || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want *AdminLookupError wrapping context.DeadlineExceeded", err)
	}
}

// countingLookup fails the first n lookups, then answers from users.
type countingLookup struct {
	users user.UserServicer
	fail  int
	calls int
}

func (c *countingLookup) GetUserByUsername(ctx context.Context, tenantID, username string) (*user.User, error) {
	c.calls++
	if c.calls <= c.fail {
		return nil, errors.New("read index not ready")
	}
	return c.users.GetUserByUsername(ctx, tenantID, username)
}

// TestVerifyAdminCertBindingWithin: only "could not read" is waited out; a
// verdict is returned at once, and a store that never answers refuses at the
// bound.
func TestVerifyAdminCertBindingWithin(t *testing.T) {
	ctx := context.Background()
	t.Run("transient failure then bound", func(t *testing.T) {
		users := newRealUsers(t)
		admin := createAdmin(t, users)
		if _, err := users.AddCertBinding(ctx, admin.ID, user.ComputeSPKIThumbprint(parseTestCert(t, realCertPEM(t))), 0); err != nil {
			t.Fatalf("AddCertBinding: %v", err)
		}
		lookup := &countingLookup{users: users, fail: 2}
		if err := VerifyAdminCertBindingWithin(ctx, lookup, 5*time.Second); err != nil {
			t.Fatalf("err = %v, want the bound admin admitted after the retries", err)
		}
		if lookup.calls != 3 {
			t.Fatalf("lookups = %d, want 3", lookup.calls)
		}
	})
	t.Run("a verdict is not retried", func(t *testing.T) {
		users := newRealUsers(t)
		createAdmin(t, users)
		lookup := &countingLookup{users: users}
		requireRefusal(t, VerifyAdminCertBindingWithin(ctx, lookup, 5*time.Second), AdminBindingNone)
		if lookup.calls != 1 {
			t.Fatalf("lookups = %d, want 1: an unbound admin is a verdict, not a read failure", lookup.calls)
		}
	})
	t.Run("never readable refuses at the bound", func(t *testing.T) {
		lookup := &countingLookup{users: newRealUsers(t), fail: 1 << 20}
		err := VerifyAdminCertBindingWithin(ctx, lookup, 100*time.Millisecond)
		var le *AdminLookupError
		if !errors.As(err, &le) {
			t.Fatalf("err = %v, want *AdminLookupError", err)
		}
	})
	t.Run("cancelled context stops the wait", func(t *testing.T) {
		cctx, cancel := context.WithCancel(ctx)
		cancel()
		lookup := &countingLookup{users: newRealUsers(t), fail: 1 << 20}
		err := VerifyAdminCertBindingWithin(cctx, lookup, time.Minute)
		var le *AdminLookupError
		if !errors.As(err, &le) || lookup.calls != 1 {
			t.Fatalf("err = %v after %d lookups, want one lookup and *AdminLookupError", err, lookup.calls)
		}
	})
}
