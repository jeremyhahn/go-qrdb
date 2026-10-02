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

package auth

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/api/sealedwindow"
)

// TestCallerPrincipal_Classification: an authenticated identity is its
// tenant and subject, a TLS-verified certificate is its leaf's digest, and no
// identity, an identity the sealed window admitted anonymously and an
// unverified certificate are the anonymous caller.
func TestCallerPrincipal_Classification(t *testing.T) {
	id := &Identity{Subject: "officer", TenantID: "system"}
	leafA := &x509.Certificate{Raw: []byte("leaf-a")}
	leafB := &x509.Certificate{Raw: []byte("leaf-b")}
	verified := func(leaf *x509.Certificate) context.Context {
		return WithTLSState(context.Background(), &tls.ConnectionState{VerifiedChains: [][]*x509.Certificate{{leaf}}})
	}
	if got := CallerPrincipal(WithIdentity(context.Background(), id)); got != "id:system/officer" {
		t.Fatalf("identity principal = %q", got)
	}
	a, b := CallerPrincipal(verified(leafA)), CallerPrincipal(verified(leafB))
	if len(a) != len("cert:")+64 || a[:5] != "cert:" || a == b {
		t.Fatalf("certificate principals = %q, %q; want distinct leaf digests", a, b)
	}
	for name, ctx := range map[string]context.Context{
		"no caller":           context.Background(),
		"nil context":         nil,
		"sealed-window anon":  sealedwindow.WithAnonymous(WithIdentity(context.Background(), id)),
		"empty verified":      WithTLSState(context.Background(), &tls.ConnectionState{VerifiedChains: [][]*x509.Certificate{{}}}),
		"unverified TLS cert": WithTLSState(context.Background(), &tls.ConnectionState{PeerCertificates: []*x509.Certificate{leafA}}),
	} {
		if got := CallerPrincipal(ctx); got != "" { //nolint:staticcheck // SA1012: a nil context is one of the cases under test
			t.Errorf("%s: principal = %q, want anonymous", name, got)
		}
	}
}

// TestSOPINBackoff_Caps: the backoff stops doubling at SOPINMaxBackoff and
// does not overflow however long the failures go on.
func TestSOPINBackoff_Caps(t *testing.T) {
	cases := map[int]time.Duration{0: SOPINBaseBackoff, 2: 4 * SOPINBaseBackoff, 20: SOPINMaxBackoff, 1_000_000: SOPINMaxBackoff}
	for n, want := range cases {
		if got := soPINBackoff(n); got != want {
			t.Errorf("soPINBackoff(%d) = %v, want %v", n, got, want)
		}
	}
}

// errWrong is a wrong-PIN answer for the lockout tests.
var errWrong = errors.New("wrong")

// TestSOPINLockout_KeysAndClassesAreSeparate: each key has its own
// counters, and within a key the anonymous caller and each authenticated
// principal count separately; a correct PIN resets only its own counter and
// an untouched key is forgotten.
func TestSOPINLockout_KeysAndClassesAreSeparate(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	l := NewSOPINLockout(func() time.Time { return now })
	wrong := func() error { return errWrong }
	right := func() error { return nil }
	isWrong := func(err error) bool { return errors.Is(err, errWrong) }
	for i := 0; i < SOPINFreeFailures; i++ {
		if err := l.Check("acme", "", wrong, isWrong); !errors.Is(err, errWrong) {
			t.Fatalf("failure %d = %v", i, err)
		}
	}
	var locked *SOPINLockedError
	if err := l.Check("acme", "", right, isWrong); !errors.As(err, &locked) || !errors.Is(err, ErrSOPINLocked) || locked.RetryAfter != SOPINBaseBackoff {
		t.Fatalf("anonymous after the free failures = %v", err)
	}
	if err := l.Check("acme", "id:system/officer", right, isWrong); err != nil {
		t.Fatalf("authenticated on the same key = %v", err)
	}
	if err := l.Check("globex", "", right, isWrong); err != nil {
		t.Fatalf("another key = %v", err)
	}
	if f, until := l.Status("acme", ""); f != SOPINFreeFailures || until.IsZero() {
		t.Fatalf("status = %d, %v", f, until)
	}
	if f, until := l.Status("nobody", "id:system/officer"); f != 0 || !until.IsZero() {
		t.Fatalf("unknown key status = %d, %v", f, until)
	}
	errOther := errors.New("store down")
	if err := l.Check("acme", "id:system/officer", func() error { return errOther }, isWrong); !errors.Is(err, errOther) {
		t.Fatalf("a non-mismatch error = %v", err)
	}
	if (&SOPINLockedError{RetryAfter: time.Second}).Error() == "" || !l.Now().Equal(now) {
		t.Fatal("error message or clock")
	}
}

// lockoutFixture is a lockout on a settable clock with wrong/right verifiers.
type lockoutFixture struct {
	now     time.Time
	l       *SOPINLockout
	wrong   func() error
	right   func() error
	isWrong func(error) bool
}

func newLockoutFixture() *lockoutFixture {
	f := &lockoutFixture{now: time.Unix(1_000_000, 0)}
	f.l = NewSOPINLockout(func() time.Time { return f.now })
	f.wrong = func() error { return errWrong }
	f.right = func() error { return nil }
	f.isWrong = func(err error) bool { return errors.Is(err, errWrong) }
	return f
}

// TestSOPINLockout_TenantUserCannotLockOutTheSO: a tenant user holding a
// certificate the client CA verified, guessing wrong PINs on the node key as
// fast as its own backoff admits for a whole aggregate window, locks out only
// itself: the Security Officer's next check -- another principal -- is
// admitted.
func TestSOPINLockout_TenantUserCannotLockOutTheSO(t *testing.T) {
	f := newLockoutFixture()
	user := CallerPrincipal(WithTLSState(context.Background(), &tls.ConnectionState{
		VerifiedChains: [][]*x509.Certificate{{{Raw: []byte("tenant-user-leaf")}}},
	}))
	so := CallerPrincipal(WithIdentity(context.Background(), &Identity{Subject: "officer", TenantID: "system"}))
	end := f.now.Add(SOPINAggregateWindow)
	attempts := 0
	for f.now.Before(end) {
		err := f.l.Check("node", user, f.wrong, f.isWrong)
		var locked *SOPINLockedError
		if errors.As(err, &locked) {
			f.now = f.now.Add(locked.RetryAfter)
			continue
		}
		attempts++
	}
	// Its own backoff holds one principal well below the aggregate cap.
	if attempts <= SOPINFreeFailures || attempts >= SOPINAggregateFailures {
		t.Fatalf("the user made %d attempts in a window, want between %d and %d", attempts, SOPINFreeFailures, SOPINAggregateFailures)
	}
	if failures, _ := f.l.Status("node", user); failures != attempts {
		t.Fatalf("the user's own counter = %d, want %d", failures, attempts)
	}
	if err := f.l.Check("node", so, f.right, f.isWrong); err != nil {
		t.Fatalf("the SO after %d wrong PINs from a tenant user's certificate = %v, want admitted", attempts, err)
	}
}

// TestSOPINLockout_AggregateCapsManyPrincipals: wrong PINs from many
// authenticated principals together are capped at SOPINAggregateFailures per
// window -- then every authenticated check of the key is refused until the
// window ends, while the anonymous counter and other keys are untouched. This
// is the documented residual: enough principals guessing together can delay
// the SO for one window.
func TestSOPINLockout_AggregateCapsManyPrincipals(t *testing.T) {
	f := newLockoutFixture()
	for i := 0; i < SOPINAggregateFailures; i++ {
		principal := "cert:" + string(rune('a'+i/SOPINFreeFailures))
		if err := f.l.Check("node", principal, f.wrong, f.isWrong); !errors.Is(err, errWrong) {
			t.Fatalf("failure %d by %s = %v", i, principal, err)
		}
	}
	var locked *SOPINLockedError
	if err := f.l.Check("node", "id:system/officer", f.right, f.isWrong); !errors.As(err, &locked) || locked.RetryAfter != SOPINAggregateWindow {
		t.Fatalf("an unrelated principal past the cap = %v, want locked for the window", err)
	}
	if _, until := f.l.Status("node", "id:system/officer"); until.IsZero() {
		t.Fatal("status does not report the aggregate lock")
	}
	if err := f.l.Check("node", "", f.right, f.isWrong); err != nil {
		t.Fatalf("the anonymous counter = %v, want untouched", err)
	}
	if err := f.l.Check("tenant-acme", "id:acme/so", f.right, f.isWrong); err != nil {
		t.Fatalf("another key = %v, want untouched", err)
	}
	f.now = f.now.Add(SOPINAggregateWindow)
	if err := f.l.Check("node", "id:system/officer", f.right, f.isWrong); err != nil {
		t.Fatalf("after the window = %v, want admitted", err)
	}
}

// TestSOPINLockout_PrincipalsBeyondTheCapShareOverflow: past
// SOPINMaxTrackedPrincipals, new principals share one overflow counter, so
// memory stays bounded and they still lock out.
func TestSOPINLockout_PrincipalsBeyondTheCapShareOverflow(t *testing.T) {
	f := newLockoutFixture()
	state := &soPINKeyState{}
	for i := 0; i < SOPINMaxTrackedPrincipals; i++ {
		state.principalCounter("cert:"+string(rune(i+0x100)), true, f.now).fail(f.now)
	}
	f.l.keys["node"] = state
	for i := 0; i < SOPINFreeFailures; i++ {
		if err := f.l.Check("node", "cert:late-"+string(rune('a'+i)), f.wrong, f.isWrong); !errors.Is(err, errWrong) {
			t.Fatalf("overflow failure %d = %v", i, err)
		}
	}
	if len(state.principals) != SOPINMaxTrackedPrincipals+1 {
		t.Fatalf("tracked %d principals, want the cap plus the overflow counter", len(state.principals))
	}
	var locked *SOPINLockedError
	if err := f.l.Check("node", "cert:late-z", f.right, f.isWrong); !errors.As(err, &locked) {
		t.Fatalf("a further overflow principal = %v, want the shared overflow lockout", err)
	}
	if failures, _ := f.l.Status("node", "cert:never-seen"); failures != 0 {
		t.Fatalf("an untracked principal's status = %d failures", failures)
	}
}

// memKnownGood is an in-memory SOPINKnownGoodStore that counts its writes.
type memKnownGood struct {
	sets  map[string][]string
	saves int
}

func (m *memKnownGood) LoadKnownGood(key string) ([]string, error) {
	return append([]string(nil), m.sets[key]...), nil
}

func (m *memKnownGood) SaveKnownGood(key string, principals []string) error {
	if m.sets == nil {
		m.sets = map[string][]string{}
	}
	m.sets[key] = principals
	m.saves++
	return nil
}

// verifiedCertPrincipal is the principal of a TLS-verified leaf with raw bytes.
func verifiedCertPrincipal(raw string) string {
	return CallerPrincipal(WithTLSState(context.Background(), &tls.ConnectionState{
		VerifiedChains: [][]*x509.Certificate{{{Raw: []byte(raw)}}},
	}))
}

// TestSOPINLockout_KnownGoodSOPassesTheHeldCap: five verified certificates
// guessing together hold the aggregate cap, and an SO that has presented the
// correct PIN before still gets through with it -- while a principal never
// seen with the right PIN is refused by the cap, as is every other guesser.
func TestSOPINLockout_KnownGoodSOPassesTheHeldCap(t *testing.T) {
	f := newLockoutFixture()
	so := CallerPrincipal(WithIdentity(context.Background(), &Identity{Subject: "officer", TenantID: "system"}))
	if err := f.l.Check("node", so, f.right, f.isWrong); err != nil {
		t.Fatalf("the SO's first check = %v", err)
	}
	guessers := []string{"leaf-1", "leaf-2", "leaf-3", "leaf-4", "leaf-5"}
	held := false
	for !held {
		progress := false
		for _, g := range guessers {
			err := f.l.Check("node", verifiedCertPrincipal(g), f.wrong, f.isWrong)
			var locked *SOPINLockedError
			if errors.As(err, &locked) {
				continue
			}
			progress = true
		}
		if _, held = f.l.keys["node"].aggregate.locked(f.now); held {
			break
		}
		if !progress {
			f.now = f.now.Add(SOPINBaseBackoff)
		}
	}
	var locked *SOPINLockedError
	if err := f.l.Check("node", verifiedCertPrincipal("newcomer"), f.right, f.isWrong); !errors.As(err, &locked) {
		t.Fatalf("a principal never known good, under the held cap = %v, want refused", err)
	}
	if err := f.l.Check("node", so, f.right, f.isWrong); err != nil {
		t.Fatalf("the known-good SO under the held cap = %v, want admitted", err)
	}
}

// TestSOPINLockout_KnownGoodIsPersistedAndStillBacksOff: the known-good set
// survives a new lockout over the same store, and a known-good principal's
// own wrong PINs still lock it out (and do not count toward the cap).
func TestSOPINLockout_KnownGoodIsPersistedAndStillBacksOff(t *testing.T) {
	store := &memKnownGood{}
	f := newLockoutFixture()
	f.l = NewSOPINLockout(func() time.Time { return f.now }, WithKnownGoodStore(store))
	so := "id:system/officer"
	if err := f.l.Check("node", so, f.right, f.isWrong); err != nil {
		t.Fatal(err)
	}
	if err := f.l.Check("node", so, f.right, f.isWrong); err != nil {
		t.Fatal(err)
	}
	if store.saves != 1 || len(store.sets["node"]) != 1 {
		t.Fatalf("store = %+v after two successes, want one save of one principal", store)
	}

	restarted := NewSOPINLockout(func() time.Time { return f.now }, WithKnownGoodStore(store))
	restarted.keys["node"] = &soPINKeyState{aggregate: soPINAggregate{windowStart: f.now, failures: SOPINAggregateFailures}}
	if err := restarted.Check("node", so, f.right, f.isWrong); err != nil {
		t.Fatalf("after a restart under a held cap, the persisted SO = %v, want admitted", err)
	}
	for i := 0; i < SOPINFreeFailures; i++ {
		_ = restarted.Check("node", so, f.wrong, f.isWrong)
	}
	var locked *SOPINLockedError
	if err := restarted.Check("node", so, f.right, f.isWrong); !errors.As(err, &locked) {
		t.Fatalf("the known-good SO after its own free failures = %v, want its own backoff", err)
	}
	if got := restarted.keys["node"].aggregate.failures; got != 0 {
		t.Fatalf("a known-good principal's failures counted toward the cap: %d", got)
	}
}

// TestSOPINLockout_KnownGoodSetIsBounded keeps the most recent
// SOPINMaxKnownGood principals.
func TestSOPINLockout_KnownGoodSetIsBounded(t *testing.T) {
	k := &soPINKeyState{}
	for i := 0; i < SOPINMaxKnownGood+3; i++ {
		k.addKnownGood("id:t/" + string(rune('a'+i)))
	}
	if len(k.knownGood) != SOPINMaxKnownGood || k.isKnownGood("id:t/a") || !k.isKnownGood("id:t/"+string(rune('a'+SOPINMaxKnownGood+2))) {
		t.Fatalf("known-good = %v", k.knownGood)
	}
	if k.addKnownGood(k.knownGood[len(k.knownGood)-1]) {
		t.Fatal("re-adding the most recent principal changed the set")
	}
	first := k.knownGood[0]
	if !k.addKnownGood(first) || k.knownGood[len(k.knownGood)-1] != first {
		t.Fatalf("re-adding an older principal did not make it most recent: %v", k.knownGood)
	}
}

// TestSOPINLockout_StalePrincipalsAreEvictedBeforeOverflow: once attackers'
// counters at the tracking cap have aged out (unlocked, last failure older
// than SOPINStaleAfter), a new legitimate principal gets its own counter
// rather than the shared overflow one -- while fresh entries are not evicted.
func TestSOPINLockout_StalePrincipalsAreEvictedBeforeOverflow(t *testing.T) {
	f := newLockoutFixture()
	state := &soPINKeyState{}
	for i := 0; i < SOPINMaxTrackedPrincipals; i++ {
		state.principalCounter("cert:attacker-"+string(rune(i+0x100)), true, f.now).fail(f.now)
	}
	f.l.keys["node"] = state

	fresh := state.principalCounter("cert:legit-early", true, f.now)
	if fresh != state.principals[overflowPrincipal] {
		t.Fatal("with every tracked counter fresh, a new principal must share the overflow counter")
	}

	f.now = f.now.Add(SOPINStaleAfter)
	if err := f.l.Check("node", "cert:legit", f.right, f.isWrong); err != nil {
		t.Fatalf("the new principal's check = %v", err)
	}
	for i := 0; i < SOPINFreeFailures; i++ {
		_ = f.l.Check("node", "cert:legit-2", f.wrong, f.isWrong)
	}
	if _, ok := state.principals["cert:legit-2"]; !ok {
		t.Fatal("a new principal was pushed to overflow although the old entries had aged out")
	}
	if len(state.principals) >= SOPINMaxTrackedPrincipals {
		t.Fatalf("stale entries were not evicted: %d tracked", len(state.principals))
	}
}
