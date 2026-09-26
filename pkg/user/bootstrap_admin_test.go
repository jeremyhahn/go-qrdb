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

package user

import (
	"context"
	"errors"
	"log/slog"
	"math"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/util/metrics"
)

// adminGauge reads the bootstrap-admin deadline gauge from the metrics
// registry -- what a scrape would see, not what the code meant to set.
func adminGauge(t *testing.T) float64 {
	t.Helper()
	families, err := metrics.Registry().Gather()
	if err != nil {
		t.Fatalf("Gather: %v", err)
	}
	for _, f := range families {
		if f.GetName() == "qrdb_auth_admin_cert_binding_not_after_timestamp_seconds" {
			return f.GetMetric()[0].GetGauge().GetValue()
		}
	}
	t.Fatal("the admin binding gauge is not registered")
	return 0
}

// adminFixture is a store built WithBootstrapAdminBindingRequired holding the
// bootstrap admin (with one live binding, adminTp) and one ordinary user.
// plain is a store WITHOUT the option over the same keyspace, for seeding
// states the guarded store would refuse to create.
type adminFixture struct {
	kv      *memKVStore
	s       *kvUserStore
	plain   *kvUserStore
	admin   *User
	other   *User
	adminTp string
	log     *strings.Builder
}

func newAdminFixture(t *testing.T) *adminFixture {
	t.Helper()
	kv := newMemKVStore()
	idgen := newTestIDGen(t)
	var buf strings.Builder
	s := &kvUserStore{store: kv, idgen: idgen}
	WithBootstrapAdminBindingRequired()(s)
	WithLogger(slog.New(slog.NewTextHandler(&buf, nil)))(s)
	f := &adminFixture{kv: kv, s: s, plain: &kvUserStore{store: kv, idgen: idgen}, log: &buf}
	ctx := context.Background()
	var err error
	if f.admin, err = s.CreateUser(ctx, &CreateUserRequest{Username: BootstrapAdminUsername, TenantID: "system", WithoutEmailLoginKey: true}); err != nil {
		t.Fatalf("CreateUser(admin): %v", err)
	}
	if f.other, err = s.CreateUser(ctx, &CreateUserRequest{Username: "bob", TenantID: "system", WithoutEmailLoginKey: true}); err != nil {
		t.Fatalf("CreateUser(bob): %v", err)
	}
	f.adminTp = newThumbprint(t, "admin", 1)
	if f.admin, err = s.AddCertBinding(ctx, f.admin.ID, f.adminTp, 0); err != nil {
		t.Fatalf("AddCertBinding(admin): %v", err)
	}
	return f
}

// wantLockout asserts err is the typed refusal for reason.
func wantLockout(t *testing.T, err error, reason AdminLockoutReason) {
	t.Helper()
	var refused *BootstrapAdminLockoutError
	if !errors.As(err, &refused) {
		t.Fatalf("err = %T(%v), want *BootstrapAdminLockoutError(%s)", err, err, reason)
	}
	if refused.Reason != reason {
		t.Fatalf("reason = %s, want %s", refused.Reason, reason)
	}
	if !strings.Contains(err.Error(), adminLockoutRemedies[reason]) {
		t.Fatalf("error %q does not say what to do instead", err.Error())
	}
}

// assertAdminCanStillLogIn re-reads the admin and asserts the refused change
// left it exactly able to log in: active, mtls permitted, adminTp live and
// still resolving through the index.
func (f *adminFixture) assertAdminCanStillLogIn(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	u, err := f.s.GetUserByUsername(ctx, "system", BootstrapAdminUsername)
	if err != nil {
		t.Fatalf("the admin is gone: %v", err)
	}
	if u.ID != f.admin.ID {
		t.Fatalf("admin id = %d, want %d", u.ID, f.admin.ID)
	}
	if blocker := AdminLoginBlocker(u, time.Now().UTC()); blocker != "" {
		t.Fatalf("the refused change left the admin blocked by %s: %+v", blocker, u)
	}
	if byTp, err := f.s.UserByCertThumbprint(ctx, f.adminTp); err != nil || byTp.ID != f.admin.ID {
		t.Fatalf("the refused change dropped the admin's index entry: %v, %v", byTp, err)
	}
}

// TestBootstrapAdminGuard_RefusesEveryLockout (go-qrdb#517 B1, B2): under
// "required" every change that would leave the admin unable to log in by
// certificate is refused with its reason, whichever verb makes it, and the
// admin is left exactly as it was.
func TestBootstrapAdminGuard_RefusesEveryLockout(t *testing.T) {
	past := time.Now().Add(-time.Hour).UTC().Truncate(time.Second)
	cases := []struct {
		name   string
		change func(f *adminFixture) error
		reason AdminLockoutReason
	}{
		{"RemoveCertBinding of the last live binding", func(f *adminFixture) error {
			_, err := f.s.RemoveCertBinding(context.Background(), f.admin.ID, f.adminTp)
			return err
		}, AdminLockoutNoLiveBinding},
		{"UpdateUser emptying the bindings", func(f *adminFixture) error {
			_, err := f.s.UpdateUser(context.Background(), &UpdateUserRequest{ID: f.admin.ID, CertBindings: []CertBinding{}})
			return err
		}, AdminLockoutNoLiveBinding},
		{"UpdateUser re-binding the last live key with a past expiry", func(f *adminFixture) error {
			_, err := f.s.UpdateUser(context.Background(), &UpdateUserRequest{ID: f.admin.ID, CertBindings: []CertBinding{
				{Thumbprint: f.adminTp, Kind: CertBindingSPKIThumbprint, NotAfter: past},
			}})
			return err
		}, AdminLockoutNoLiveBinding},
		{"DeleteUser", func(f *adminFixture) error {
			return f.s.DeleteUser(context.Background(), f.admin.ID)
		}, AdminLockoutDelete},
		{"DisableUser", func(f *adminFixture) error {
			return f.s.DisableUser(context.Background(), f.admin.ID)
		}, AdminLockoutLifecycle},
		{"UpdateUser suspending", func(f *adminFixture) error {
			_, err := f.s.UpdateUser(context.Background(), &UpdateUserRequest{ID: f.admin.ID, Lifecycle: LifecycleSuspended})
			return err
		}, AdminLockoutLifecycle},
		{"UpdateUser marking deleting", func(f *adminFixture) error {
			_, err := f.s.UpdateUser(context.Background(), &UpdateUserRequest{ID: f.admin.ID, Lifecycle: LifecycleDeleting})
			return err
		}, AdminLockoutLifecycle},
		{"UpdateUser dropping mtls from the methods", func(f *adminFixture) error {
			_, err := f.s.UpdateUser(context.Background(), &UpdateUserRequest{ID: f.admin.ID, AuthMethods: []string{"webauthn"}})
			return err
		}, AdminLockoutMTLSNotPermitted},
		{"UpdateUser renaming", func(f *adminFixture) error {
			_, err := f.s.UpdateUser(context.Background(), &UpdateUserRequest{ID: f.admin.ID, Username: "root"})
			return err
		}, AdminLockoutRename},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newAdminFixture(t)
			wantLockout(t, tc.change(f), tc.reason)
			f.assertAdminCanStillLogIn(t)
		})
	}
}

// TestBootstrapAdminGuard_AllowsWhatKeepsTheAdminLoggedIn: the guard refuses
// the lockout, not the verb. Enrolling the replacement first is the
// documented path, a method policy that still includes mtls is fine, and an
// ordinary edit goes through.
func TestBootstrapAdminGuard_AllowsWhatKeepsTheAdminLoggedIn(t *testing.T) {
	f := newAdminFixture(t)
	ctx := context.Background()
	replacement := newThumbprint(t, "admin", 2)
	if _, err := f.s.AddCertBinding(ctx, f.admin.ID, replacement, time.Now().Add(24*time.Hour).Unix()); err != nil {
		t.Fatalf("enrolling a replacement = %v", err)
	}
	if _, err := f.s.RemoveCertBinding(ctx, f.admin.ID, f.adminTp); err != nil {
		t.Fatalf("withdrawing with a live replacement enrolled = %v, want nil", err)
	}
	// The replacement is now the last live one.
	_, err := f.s.RemoveCertBinding(ctx, f.admin.ID, replacement)
	wantLockout(t, err, AdminLockoutNoLiveBinding)

	if _, err := f.s.UpdateUser(ctx, &UpdateUserRequest{ID: f.admin.ID, AuthMethods: []string{"mtls", "piv"}}); err != nil {
		t.Fatalf("a method policy that keeps mtls = %v, want nil", err)
	}
	if _, err := f.s.UpdateUser(ctx, &UpdateUserRequest{ID: f.admin.ID, Name: "Platform Admin", RoleIDs: []string{"r"}}); err != nil {
		t.Fatalf("an ordinary edit = %v, want nil", err)
	}
	if _, err := f.s.UpdateUser(ctx, &UpdateUserRequest{ID: f.admin.ID, Username: BootstrapAdminUsername}); err != nil {
		t.Fatalf("re-asserting the admin's own name = %v, want nil", err)
	}
}

// TestBootstrapAdminGuard_ExpiredBindingDoesNotCount: an expired binding
// cannot log in, so it does not make the last live one withdrawable; and
// withdrawing the expired one is not a lockout.
func TestBootstrapAdminGuard_ExpiredBindingDoesNotCount(t *testing.T) {
	f := newAdminFixture(t)
	ctx := context.Background()
	expired := newThumbprint(t, "admin", 2)
	// Seeded through UpdateUser: AddCertBinding refuses a past expiry.
	if _, err := f.s.UpdateUser(ctx, &UpdateUserRequest{ID: f.admin.ID, CertBindings: []CertBinding{
		{Thumbprint: f.adminTp, Kind: CertBindingSPKIThumbprint},
		{Thumbprint: expired, Kind: CertBindingSPKIThumbprint, NotAfter: time.Now().Add(-time.Hour).UTC()},
	}}); err != nil {
		t.Fatalf("seeding an expired binding beside the live one = %v", err)
	}
	_, err := f.s.RemoveCertBinding(ctx, f.admin.ID, f.adminTp)
	wantLockout(t, err, AdminLockoutNoLiveBinding)
	if _, err := f.s.RemoveCertBinding(ctx, f.admin.ID, expired); err != nil {
		t.Fatalf("withdrawing an expired binding = %v, want nil", err)
	}
	f.assertAdminCanStillLogIn(t)
}

// TestBootstrapAdminGuard_RepairIsNotRefused: a requirement the admin ALREADY
// fails is not the change's doing, so the guard does not stand in the way of
// fixing it -- or of an unrelated edit to an already-locked-out admin.
func TestBootstrapAdminGuard_RepairIsNotRefused(t *testing.T) {
	f := newAdminFixture(t)
	ctx := context.Background()
	// Break the admin the way an operator under "enrolled" could have.
	if _, err := f.plain.UpdateUser(ctx, &UpdateUserRequest{ID: f.admin.ID, Lifecycle: LifecycleSuspended, AuthMethods: []string{"webauthn"}}); err != nil {
		t.Fatalf("seeding a suspended, webauthn-only admin: %v", err)
	}
	if _, err := f.s.UpdateUser(ctx, &UpdateUserRequest{ID: f.admin.ID, AuthMethods: []string{"piv"}}); err != nil {
		t.Fatalf("an edit that leaves an already-broken requirement broken = %v, want nil", err)
	}
	if _, err := f.s.UpdateUser(ctx, &UpdateUserRequest{ID: f.admin.ID, AuthMethods: []string{}}); err != nil {
		t.Fatalf("restoring the method policy = %v, want nil", err)
	}
	if err := f.s.EnableUser(ctx, f.admin.ID); err != nil {
		t.Fatalf("re-enabling the admin = %v, want nil", err)
	}
	f.assertAdminCanStillLogIn(t)
	// Repaired, it is guarded again.
	wantLockout(t, f.s.DisableUser(ctx, f.admin.ID), AdminLockoutLifecycle)
}

// TestBootstrapAdminGuard_ScopedToTheAdminAndThePolicy: other accounts are
// unaffected, and a store without the option refuses none of it.
func TestBootstrapAdminGuard_ScopedToTheAdminAndThePolicy(t *testing.T) {
	f := newAdminFixture(t)
	ctx := context.Background()
	tp := newThumbprint(t, "bob", 1)
	if _, err := f.s.AddCertBinding(ctx, f.other.ID, tp, 0); err != nil {
		t.Fatalf("AddCertBinding: %v", err)
	}
	if _, err := f.s.RemoveCertBinding(ctx, f.other.ID, tp); err != nil {
		t.Fatalf("withdrawing an ordinary account's last binding = %v, want nil", err)
	}
	if err := f.s.DisableUser(ctx, f.other.ID); err != nil {
		t.Fatalf("disabling an ordinary account = %v, want nil", err)
	}
	if err := f.s.DeleteUser(ctx, f.other.ID); err != nil {
		t.Fatalf("deleting an ordinary account = %v, want nil", err)
	}

	// The "enrolled" posture: the same store without the option.
	if _, err := f.plain.RemoveCertBinding(ctx, f.admin.ID, f.adminTp); err != nil {
		t.Fatalf("withdrawing the admin's last binding under enrolled = %v, want nil", err)
	}
	if _, err := f.plain.UpdateUser(ctx, &UpdateUserRequest{ID: f.admin.ID, Username: "root"}); err != nil {
		t.Fatalf("renaming the admin under enrolled = %v, want nil", err)
	}
	if err := f.plain.DeleteUser(ctx, f.admin.ID); err != nil {
		t.Fatalf("deleting the admin under enrolled = %v, want nil", err)
	}
}

// TestBootstrapAdminGuard_ConcurrentWithdrawalsKeepOneKey: two withdrawals of
// the admin's last two keys, each safe alone, race on two goroutines. The
// guard is judged against the record each compare-and-swap is based on, so
// exactly one may commit and the admin always keeps a live key.
func TestBootstrapAdminGuard_ConcurrentWithdrawalsKeepOneKey(t *testing.T) {
	for round := range 25 {
		f := newAdminFixture(t)
		ctx := context.Background()
		second := newThumbprint(t, "admin", 2)
		if _, err := f.s.AddCertBinding(ctx, f.admin.ID, second, 0); err != nil {
			t.Fatalf("AddCertBinding: %v", err)
		}

		start := make(chan struct{})
		errs := make([]error, 2)
		var wg sync.WaitGroup
		for i, tp := range []string{f.adminTp, second} {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				_, errs[i] = f.s.RemoveCertBinding(ctx, f.admin.ID, tp)
			}()
		}
		close(start)
		wg.Wait()

		var ok, refused int
		for _, err := range errs {
			var lockout *BootstrapAdminLockoutError
			switch {
			case err == nil:
				ok++
			case errors.As(err, &lockout):
				refused++
			default:
				t.Fatalf("round %d: unexpected error %v", round, err)
			}
		}
		if ok != 1 || refused != 1 {
			t.Fatalf("round %d: %d withdrawals committed and %d were refused, want exactly one of each", round, ok, refused)
		}
		u, err := f.s.GetUser(ctx, f.admin.ID)
		if err != nil || !hasLiveBinding(u.CertBindings, time.Now().UTC()) {
			t.Fatalf("round %d: the admin was left with no live binding: %+v, %v", round, u, err)
		}
	}
}

// TestBootstrapAdminGuard_LosingWithdrawalIsJudgedAgainstTheWinner pins the
// same property without depending on the scheduler: a concurrent withdrawal
// of the other key commits in the window between this withdrawal's read and
// its commit. This one must lose its swap, re-read, and be refused.
func TestBootstrapAdminGuard_LosingWithdrawalIsJudgedAgainstTheWinner(t *testing.T) {
	f := newAdminFixture(t)
	ctx := context.Background()
	second := newThumbprint(t, "admin", 2)
	if _, err := f.s.AddCertBinding(ctx, f.admin.ID, second, 0); err != nil {
		t.Fatalf("AddCertBinding: %v", err)
	}
	var winnerErr error
	fired := false
	f.kv.afterGet = func(key string) {
		if fired || key != primaryKey(f.admin.ID) {
			return
		}
		fired = true
		f.kv.afterGet = nil
		_, winnerErr = f.s.RemoveCertBinding(ctx, f.admin.ID, second)
	}
	_, err := f.s.RemoveCertBinding(ctx, f.admin.ID, f.adminTp)
	if !fired || winnerErr != nil {
		t.Fatalf("the interfering withdrawal did not commit: fired=%v err=%v", fired, winnerErr)
	}
	wantLockout(t, err, AdminLockoutNoLiveBinding)
	f.assertAdminCanStillLogIn(t)
}

// TestAddCertBinding_RefusesAPastExpiry (go-qrdb#517 B1): an enrolment whose
// expiry is not in the future is refused for every account -- it could never
// authenticate -- and for a key already bound it would otherwise silently
// withdraw the key.
func TestAddCertBinding_RefusesAPastExpiry(t *testing.T) {
	f := newAdminFixture(t)
	ctx := context.Background()
	past := time.Now().Add(-time.Minute).Unix()
	for _, target := range []*User{f.admin, f.other} {
		tp := f.adminTp
		if target != f.admin {
			tp = newThumbprint(t, "bob", 1)
		}
		_, err := f.s.AddCertBinding(ctx, target.ID, tp, past)
		var expired *CertBindingExpiredError
		if !errors.As(err, &expired) || expired.Thumbprint != tp || expired.NotAfter.Unix() != past {
			t.Fatalf("AddCertBinding(%s, past) = %v, want *CertBindingExpiredError naming it", target.Username, err)
		}
		if !strings.Contains(err.Error(), "not in the future") {
			t.Fatalf("error %q does not say what is wrong", err.Error())
		}
	}
	f.assertAdminCanStillLogIn(t)
	// The guard-free store refuses it too: this is validation, not posture.
	if _, err := f.plain.AddCertBinding(ctx, f.admin.ID, f.adminTp, past); err == nil {
		t.Fatal("a store without the option accepted a past expiry")
	}
}

func TestIsBootstrapAdmin(t *testing.T) {
	cases := map[string]struct {
		u    *User
		want bool
	}{
		"system admin":          {&User{Username: BootstrapAdminUsername, TenantID: "system"}, true},
		"empty tenant = system": {&User{Username: BootstrapAdminUsername, TenantID: ""}, true},
		"tenant admin":          {&User{Username: BootstrapAdminUsername, TenantID: "acme"}, false},
		"other system user":     {&User{Username: "bob", TenantID: "system"}, false},
		"nil":                   {nil, false},
	}
	for name, tc := range cases {
		if got := IsBootstrapAdmin(tc.u); got != tc.want {
			t.Errorf("%s: IsBootstrapAdmin = %v, want %v", name, got, tc.want)
		}
	}
}

// TestAdminLoginBlocker names the first requirement a record fails, in the
// order the authentication path applies them.
func TestAdminLoginBlocker(t *testing.T) {
	now := time.Now().UTC()
	live := []CertBinding{{Thumbprint: "x"}}
	expired := []CertBinding{{Thumbprint: "x", NotAfter: now.Add(-time.Hour)}}
	cases := map[string]struct {
		u    *User
		want AdminLockoutReason
	}{
		"can log in":            {&User{Lifecycle: LifecycleActive, CertBindings: live}, ""},
		"mtls among methods":    {&User{Lifecycle: LifecycleActive, AuthMethods: []string{"piv", "mtls"}, CertBindings: live}, ""},
		"suspended":             {&User{Lifecycle: LifecycleSuspended, AuthMethods: []string{"piv"}}, AdminLockoutLifecycle},
		"methods exclude mtls":  {&User{Lifecycle: LifecycleActive, AuthMethods: []string{"piv"}}, AdminLockoutMTLSNotPermitted},
		"only expired bindings": {&User{Lifecycle: LifecycleActive, CertBindings: expired}, AdminLockoutNoLiveBinding},
		"no bindings":           {&User{Lifecycle: LifecycleActive}, AdminLockoutNoLiveBinding},
	}
	for name, tc := range cases {
		if got := AdminLoginBlocker(tc.u, now); got != tc.want {
			t.Errorf("%s: AdminLoginBlocker = %q, want %q", name, got, tc.want)
		}
	}
}

// TestObserveBootstrapAdminBindings_Gauge (N2): the gauge carries the latest
// live NotAfter as Unix seconds, +Inf for a binding that never expires, and
// the warning fires inside the 30-day window.
func TestObserveBootstrapAdminBindings_Gauge(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	b := func(notAfter time.Time) CertBinding { return CertBinding{Thumbprint: "x", NotAfter: notAfter} }
	cases := []struct {
		name      string
		bindings  []CertBinding
		remaining time.Duration
		gauge     float64
		warn      bool
	}{
		{"expires in a week", []CertBinding{b(now.Add(7 * 24 * time.Hour))}, 7 * 24 * time.Hour, float64(now.Add(7 * 24 * time.Hour).Unix()), true},
		{"latest live counts", []CertBinding{b(now.Add(time.Hour)), b(now.Add(90 * 24 * time.Hour)), b(now.Add(-time.Hour))}, 90 * 24 * time.Hour, float64(now.Add(90 * 24 * time.Hour).Unix()), false},
		{"never expires", []CertBinding{b(now.Add(time.Hour)), {Thumbprint: "y"}}, -1, math.Inf(1), false},
		{"only expired", []CertBinding{b(now.Add(-time.Hour))}, 0, float64(now.Add(-time.Hour).Unix()), false},
		{"no bindings", nil, 0, 0, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var buf strings.Builder
			logger := slog.New(slog.NewTextHandler(&buf, nil))
			got := ObserveBootstrapAdminBindings(logger, &User{Username: BootstrapAdminUsername, TenantID: "system", CertBindings: tc.bindings}, now)
			if got != tc.remaining {
				t.Fatalf("remaining = %v, want %v", got, tc.remaining)
			}
			if g := adminGauge(t); g != tc.gauge {
				t.Fatalf("gauge = %v, want %v", g, tc.gauge)
			}
			if warned := strings.Contains(buf.String(), "expires soon"); warned != tc.warn {
				t.Fatalf("warned = %v, want %v (log %q)", warned, tc.warn, buf.String())
			}
		})
	}
}

// TestAdminChanges_RefreshTheAdminGauge (N2, NB3): every committed change to
// the admin's bindings -- through AddCertBinding, RemoveCertBinding or
// UpdateUser -- updates the deadline at once, deleting the admin clears it,
// and another account's changes leave it alone.
func TestAdminChanges_RefreshTheAdminGauge(t *testing.T) {
	f := newAdminFixture(t)
	ctx := context.Background()
	soon := time.Now().Add(48 * time.Hour).Truncate(time.Second)
	later := time.Now().Add(400 * 24 * time.Hour).Truncate(time.Second)
	soonTp, laterTp := newThumbprint(t, "admin", 2), newThumbprint(t, "admin", 3)
	gaugeIs := func(step string, want float64) {
		t.Helper()
		if g := adminGauge(t); g != want {
			t.Fatalf("%s: gauge = %v, want %v", step, g, want)
		}
	}
	gaugeIs("fixture enrolled a never-expiring key", math.Inf(1))

	// Replace the never-expiring key with a dated one through UpdateUser.
	if _, err := f.s.UpdateUser(ctx, &UpdateUserRequest{ID: f.admin.ID, CertBindings: []CertBinding{
		{Thumbprint: soonTp, Kind: CertBindingSPKIThumbprint, NotAfter: soon},
	}}); err != nil {
		t.Fatalf("UpdateUser(CertBindings): %v", err)
	}
	gaugeIs("after UpdateUser replaced the bindings", float64(soon.Unix()))
	if _, err := f.s.AddCertBinding(ctx, f.admin.ID, laterTp, later.Unix()); err != nil {
		t.Fatalf("AddCertBinding: %v", err)
	}
	gaugeIs("after enrolling a later key", float64(later.Unix()))
	if _, err := f.s.RemoveCertBinding(ctx, f.admin.ID, laterTp); err != nil {
		t.Fatalf("RemoveCertBinding: %v", err)
	}
	gaugeIs("after withdrawing the later key", float64(soon.Unix()))
	if _, err := f.s.AddCertBinding(ctx, f.other.ID, newThumbprint(t, "bob", 9), later.Unix()); err != nil {
		t.Fatalf("AddCertBinding(bob): %v", err)
	}
	gaugeIs("after another account's enrolment", float64(soon.Unix()))

	// Deleting the admin is possible only under "enrolled"; it leaves no
	// deadline to report.
	if err := f.plain.DeleteUser(ctx, f.admin.ID); err != nil {
		t.Fatalf("DeleteUser(admin) under enrolled: %v", err)
	}
	gaugeIs("after deleting the admin", 0)
}

// TestAdminExpiryWarning_IsTheRequiredPosturesAndUsesTheStoresLogger (NB2):
// the near-expiry warning goes to the logger the store was built with, and
// only a store built WithBootstrapAdminBindingRequired emits it.
func TestAdminExpiryWarning_IsTheRequiredPosturesAndUsesTheStoresLogger(t *testing.T) {
	f := newAdminFixture(t)
	ctx := context.Background()
	soon := time.Now().Add(48 * time.Hour).Unix()

	var plainLog strings.Builder
	WithLogger(slog.New(slog.NewTextHandler(&plainLog, nil)))(f.plain)
	if _, err := f.plain.AddCertBinding(ctx, f.admin.ID, f.adminTp, soon); err != nil {
		t.Fatalf("AddCertBinding under enrolled: %v", err)
	}
	if strings.Contains(plainLog.String(), "expires soon") {
		t.Fatalf("a store without the option warned: %q", plainLog.String())
	}

	if _, err := f.s.AddCertBinding(ctx, f.admin.ID, f.adminTp, soon); err != nil {
		t.Fatalf("AddCertBinding under required: %v", err)
	}
	if !strings.Contains(f.log.String(), "expires soon") {
		t.Fatalf("the required store did not warn through its logger: %q", f.log.String())
	}
}
