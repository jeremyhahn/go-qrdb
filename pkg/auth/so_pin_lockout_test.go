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
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/api/sealedwindow"
	"github.com/jeremyhahn/go-qrdb/pkg/holderprofile"
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

// TestCallerPrincipal_AddrKeysByHostOnly is the go-qrdb#560 re-review item
// 2 regression test: an "addr:" principal must be the SAME principal
// across different ephemeral source ports on the same host (a rotating
// client port, or a new connection per request behind a pooling reverse
// proxy), and the same IPv4/IPv6 host written in different but equivalent
// forms (with/without a port, bracketed IPv6, a zone id, leading zeros in
// a group) must collapse to the one principal too -- a different host
// must still be a different principal.
func TestCallerPrincipal_AddrKeysByHostOnly(t *testing.T) {
	principalFor := func(addr string) string {
		return CallerPrincipal(WithRemoteAddr(context.Background(), addr))
	}
	rotatingPorts := []string{"203.0.113.9:1234", "203.0.113.9:5555", "203.0.113.9:65000"}
	first := principalFor(rotatingPorts[0])
	if first == "" || first[:5] != "addr:" {
		t.Fatalf("principal = %q, want an addr: principal", first)
	}
	for _, addr := range rotatingPorts[1:] {
		if got := principalFor(addr); got != first {
			t.Fatalf("principal for %q = %q, want %q (same host, different port)", addr, got, first)
		}
	}
	equivalentIPv6 := []string{
		"[2001:db8::1]:443",
		"[2001:0db8:0000:0000:0000:0000:0000:0001]:9",
		"2001:db8::1",
	}
	v6first := principalFor(equivalentIPv6[0])
	for _, addr := range equivalentIPv6[1:] {
		if got := principalFor(addr); got != v6first {
			t.Fatalf("principal for %q = %q, want %q (same IPv6 host, different form)", addr, got, v6first)
		}
	}
	if v6first == first {
		t.Fatalf("the IPv4 and IPv6 principals collided: %q", v6first)
	}
	zoned := principalFor("[fe80::1%eth0]:443")
	if zoned == "" || zoned == first || zoned == v6first {
		t.Fatalf("zoned IPv6 principal = %q, want its own distinct, non-empty principal", zoned)
	}
	if got := principalFor("203.0.113.10:1234"); got == first {
		t.Fatalf("a genuinely different host got the same principal as %q: %q", first, got)
	}
	// A hostname a reverse proxy might log instead of an IP is stripped of
	// its port and otherwise passed through unchanged -- stripping only
	// ever removes a port, never invents a host.
	if got := principalFor("proxy.example.com:8080"); got != "addr:proxy.example.com" {
		t.Fatalf("hostname principal = %q, want %q", got, "addr:proxy.example.com")
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
		if err := l.Check(context.Background(), "acme", "", wrong, isWrong); !errors.Is(err, errWrong) {
			t.Fatalf("failure %d = %v", i, err)
		}
	}
	var locked *SOPINLockedError
	if err := l.Check(context.Background(), "acme", "", right, isWrong); !errors.As(err, &locked) || !errors.Is(err, ErrSOPINLocked) || locked.RetryAfter != SOPINBaseBackoff {
		t.Fatalf("anonymous after the free failures = %v", err)
	}
	if err := l.Check(context.Background(), "acme", "id:system/officer", right, isWrong); err != nil {
		t.Fatalf("authenticated on the same key = %v", err)
	}
	if err := l.Check(context.Background(), "globex", "", right, isWrong); err != nil {
		t.Fatalf("another key = %v", err)
	}
	if f, until := l.Status("acme", ""); f != SOPINFreeFailures || until.IsZero() {
		t.Fatalf("status = %d, %v", f, until)
	}
	if f, until := l.Status("nobody", "id:system/officer"); f != 0 || !until.IsZero() {
		t.Fatalf("unknown key status = %d, %v", f, until)
	}
	errOther := errors.New("store down")
	if err := l.Check(context.Background(), "acme", "id:system/officer", func() error { return errOther }, isWrong); !errors.Is(err, errOther) {
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
		err := f.l.Check(context.Background(), "node", user, f.wrong, f.isWrong)
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
	if err := f.l.Check(context.Background(), "node", so, f.right, f.isWrong); err != nil {
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
		if err := f.l.Check(context.Background(), "node", principal, f.wrong, f.isWrong); !errors.Is(err, errWrong) {
			t.Fatalf("failure %d by %s = %v", i, principal, err)
		}
	}
	var locked *SOPINLockedError
	if err := f.l.Check(context.Background(), "node", "id:system/officer", f.right, f.isWrong); !errors.As(err, &locked) || locked.RetryAfter != SOPINAggregateWindow {
		t.Fatalf("an unrelated principal past the cap = %v, want locked for the window", err)
	}
	if _, until := f.l.Status("node", "id:system/officer"); until.IsZero() {
		t.Fatal("status does not report the aggregate lock")
	}
	if err := f.l.Check(context.Background(), "node", "", f.right, f.isWrong); err != nil {
		t.Fatalf("the anonymous counter = %v, want untouched", err)
	}
	if err := f.l.Check(context.Background(), "tenant-acme", "id:acme/so", f.right, f.isWrong); err != nil {
		t.Fatalf("another key = %v, want untouched", err)
	}
	f.now = f.now.Add(SOPINAggregateWindow)
	if err := f.l.Check(context.Background(), "node", "id:system/officer", f.right, f.isWrong); err != nil {
		t.Fatalf("after the window = %v, want admitted", err)
	}
}

// TestSOPINLockout_AddrPrincipalsExcludedFromAggregateCap is the
// go-qrdb#560 re-review item 2 regression test: unlike an authenticated
// principal, an "addr:" principal's failures never engage the
// AUTHENTICATED aggregate cap, so enough distinct unauthenticated hosts
// guessing together can neither lock out a DIFFERENT host nor an
// authenticated officer -- "one host locked out a different host AND an
// authenticated officer" is exactly the re-review's finding this closes.
// Each host still locks itself out on its own backoff; that part is
// unchanged. Stays well under SOPINAddrAggregateFailures (go-qrdb#591
// re-review item D's own, separate cap) so it tests ONLY the authenticated
// cap's exclusion, not that cap's own threshold --
// TestSOPINLockout_AddrAggregateCapsManyAddrPrincipals below covers that.
func TestSOPINLockout_AddrPrincipalsExcludedFromAggregateCap(t *testing.T) {
	f := newLockoutFixture()
	for i := 0; i < SOPINAggregateFailures/2; i++ {
		host := CallerPrincipal(WithRemoteAddr(context.Background(), addrForTest(i)))
		if err := f.l.Check(context.Background(), "node", host, f.wrong, f.isWrong); !errors.Is(err, errWrong) {
			t.Fatalf("failure %d by %s = %v, want the plain wrong-PIN error (not yet locked)", i, host, err)
		}
	}
	// A host NOT among the ones above, guessing for the first time, must
	// be admitted to the comparison (a wrong PIN, not a lockout) --
	// proving the aggregate cap never engaged across all those addr:
	// failures.
	freshHost := CallerPrincipal(WithRemoteAddr(context.Background(), "198.51.100.1:1"))
	if err := f.l.Check(context.Background(), "node", freshHost, f.wrong, f.isWrong); !errors.Is(err, errWrong) {
		t.Fatalf("a fresh, unrelated host = %v, want the plain wrong-PIN error, not an aggregate lockout", err)
	}
	// An authenticated officer, who never guessed at all, must be
	// admitted too.
	so := CallerPrincipal(WithIdentity(context.Background(), &Identity{Subject: "officer", TenantID: "system"}))
	if err := f.l.Check(context.Background(), "node", so, f.right, f.isWrong); err != nil {
		t.Fatalf("an authenticated officer after only addr: failures = %v, want admitted", err)
	}
	if _, until := f.l.Status("node", so); !until.IsZero() {
		t.Fatalf("status reports the officer held by the aggregate cap after only addr: failures")
	}
}

// addrForTest returns the i-th of a distinct run of IPv4 host:port strings
// for TestSOPINLockout_AddrPrincipalsExcludedFromAggregateCap, each a
// different host (so each gets its own counter) within one private test
// range (TEST-NET-3, RFC 5737).
func addrForTest(i int) string {
	return fmt.Sprintf("203.0.113.%d:443", 1+i%254)
}

// addrV6SubnetForTest returns the i-th of a distinct run of IPv6 host:port
// strings, each in its OWN /64 (varying the 4th group, which /64 masking
// keeps), within the documentation/example range (RFC 3849, 2001:db8::/32).
func addrV6SubnetForTest(i int) string {
	return fmt.Sprintf("[2001:db8:0:%x::1]:443", i)
}

// TestRemoteHostOnly_IPv6MaskedToSlash64 is the go-qrdb#591 re-review item
// D regression test for the masking half of the fix: two addresses within
// the SAME /64 (differing only in the bits below it) collapse to the
// identical "addr:" principal, exactly as two different ephemeral ports on
// the same host already did -- the attack this closes is a single /64
// minting effectively unlimited distinct /128 addresses, each otherwise a
// fresh, un-rate-limited principal. A different /64 still gets a different
// principal: masking narrows what counts as "the same source," it does
// not erase host isolation entirely.
func TestRemoteHostOnly_IPv6MaskedToSlash64(t *testing.T) {
	principalFor := func(addr string) string {
		return CallerPrincipal(WithRemoteAddr(context.Background(), addr))
	}
	base := principalFor("[2001:db8:1234:5678::1]:443")
	sameSubnet := []string{
		"[2001:db8:1234:5678::2]:443",
		"[2001:db8:1234:5678:ffff:ffff:ffff:ffff]:9",
		"2001:db8:1234:5678::1",
	}
	for _, addr := range sameSubnet {
		if got := principalFor(addr); got != base {
			t.Fatalf("principal for %q = %q, want %q (same /64, only the host part differs)", addr, got, base)
		}
	}
	differentSubnet := principalFor("[2001:db8:1234:5679::1]:443")
	if differentSubnet == base {
		t.Fatalf("a genuinely different /64 got the same principal as %q: %q", base, differentSubnet)
	}
	// An IPv4-mapped IPv6 address is unmapped and keyed as the IPv4 host it
	// actually is, never treated as a /64 of its own.
	v4mapped := principalFor("[::ffff:203.0.113.9]:443")
	v4native := principalFor("203.0.113.9:443")
	if v4mapped != v4native {
		t.Fatalf("IPv4-mapped principal = %q, want the same as native IPv4 %q", v4mapped, v4native)
	}
}

// TestSOPINLockout_AddrAggregateCapsManyAddrPrincipals is the go-qrdb#591
// re-review item D regression test for the aggregate-cap half of the fix:
// mirrors TestSOPINLockout_AggregateCapsManyPrincipals, but for "addr:"
// principals in distinct /64s instead of authenticated identities. Enough
// of them guessing together engage a SEPARATE cap (SOPINAddrAggregateFailures),
// never the authenticated one -- a fresh addr principal is refused, but an
// authenticated officer and a different key both stay untouched, and the
// cap releases after its own window.
func TestSOPINLockout_AddrAggregateCapsManyAddrPrincipals(t *testing.T) {
	f := newLockoutFixture()
	for i := 0; i < SOPINAddrAggregateFailures; i++ {
		host := CallerPrincipal(WithRemoteAddr(context.Background(), addrV6SubnetForTest(i)))
		if err := f.l.Check(context.Background(), "node", host, f.wrong, f.isWrong); !errors.Is(err, errWrong) {
			t.Fatalf("failure %d by %s = %v, want the plain wrong-PIN error (not yet locked)", i, host, err)
		}
	}
	freshHost := CallerPrincipal(WithRemoteAddr(context.Background(), "[2001:db8:ffff:ffff::1]:443"))
	var locked *SOPINLockedError
	if err := f.l.Check(context.Background(), "node", freshHost, f.wrong, f.isWrong); !errors.As(err, &locked) || locked.RetryAfter != SOPINAddrAggregateWindow {
		t.Fatalf("a fresh /64 past the addr: aggregate cap = %v, want locked for the window", err)
	}
	if _, until := f.l.Status("node", freshHost); until.IsZero() {
		t.Fatal("status does not report the addr: aggregate lock")
	}
	// The AUTHENTICATED aggregate cap is a separate budget: an officer who
	// never guessed at all must still be admitted after only addr:
	// failures, exactly as the exclusion test above already proves in the
	// other direction.
	so := CallerPrincipal(WithIdentity(context.Background(), &Identity{Subject: "officer", TenantID: "system"}))
	if err := f.l.Check(context.Background(), "node", so, f.right, f.isWrong); err != nil {
		t.Fatalf("an authenticated officer after only addr: failures = %v, want admitted", err)
	}
	if err := f.l.Check(context.Background(), "tenant-acme", "addr:198.51.100.1", f.right, f.isWrong); err != nil {
		t.Fatalf("another key = %v, want untouched", err)
	}
	f.now = f.now.Add(SOPINAddrAggregateWindow)
	if err := f.l.Check(context.Background(), "node", freshHost, f.wrong, f.isWrong); !errors.Is(err, errWrong) {
		t.Fatalf("after the window = %v, want the plain wrong-PIN error, not still locked", err)
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
		if err := f.l.Check(context.Background(), "node", "cert:late-"+string(rune('a'+i)), f.wrong, f.isWrong); !errors.Is(err, errWrong) {
			t.Fatalf("overflow failure %d = %v", i, err)
		}
	}
	if len(state.principals) != SOPINMaxTrackedPrincipals+1 {
		t.Fatalf("tracked %d principals, want the cap plus the overflow counter", len(state.principals))
	}
	var locked *SOPINLockedError
	if err := f.l.Check(context.Background(), "node", "cert:late-z", f.right, f.isWrong); !errors.As(err, &locked) {
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

func (m *memKnownGood) LoadKnownGood(_ context.Context, key string) ([]string, error) {
	return append([]string(nil), m.sets[key]...), nil
}

func (m *memKnownGood) SaveKnownGood(_ context.Context, key string, principals []string) error {
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
	if err := f.l.Check(context.Background(), "node", so, f.right, f.isWrong); err != nil {
		t.Fatalf("the SO's first check = %v", err)
	}
	guessers := []string{"leaf-1", "leaf-2", "leaf-3", "leaf-4", "leaf-5"}
	held := false
	for !held {
		progress := false
		for _, g := range guessers {
			err := f.l.Check(context.Background(), "node", verifiedCertPrincipal(g), f.wrong, f.isWrong)
			var locked *SOPINLockedError
			if errors.As(err, &locked) {
				continue
			}
			progress = true
		}
		if _, held = f.l.keys["node"].aggregate.locked(f.now, SOPINAggregateFailures, SOPINAggregateWindow); held {
			break
		}
		if !progress {
			f.now = f.now.Add(SOPINBaseBackoff)
		}
	}
	var locked *SOPINLockedError
	if err := f.l.Check(context.Background(), "node", verifiedCertPrincipal("newcomer"), f.right, f.isWrong); !errors.As(err, &locked) {
		t.Fatalf("a principal never known good, under the held cap = %v, want refused", err)
	}
	if err := f.l.Check(context.Background(), "node", so, f.right, f.isWrong); err != nil {
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
	if err := f.l.Check(context.Background(), "node", so, f.right, f.isWrong); err != nil {
		t.Fatal(err)
	}
	if err := f.l.Check(context.Background(), "node", so, f.right, f.isWrong); err != nil {
		t.Fatal(err)
	}
	if store.saves != 1 || len(store.sets["node"]) != 1 {
		t.Fatalf("store = %+v after two successes, want one save of one principal", store)
	}

	restarted := NewSOPINLockout(func() time.Time { return f.now }, WithKnownGoodStore(store))
	restarted.keys["node"] = &soPINKeyState{aggregate: soPINAggregate{windowStart: f.now, failures: SOPINAggregateFailures}}
	if err := restarted.Check(context.Background(), "node", so, f.right, f.isWrong); err != nil {
		t.Fatalf("after a restart under a held cap, the persisted SO = %v, want admitted", err)
	}
	for i := 0; i < SOPINFreeFailures; i++ {
		_ = restarted.Check(context.Background(), "node", so, f.wrong, f.isWrong)
	}
	var locked *SOPINLockedError
	if err := restarted.Check(context.Background(), "node", so, f.right, f.isWrong); !errors.As(err, &locked) {
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
	if err := f.l.Check(context.Background(), "node", "cert:legit", f.right, f.isWrong); err != nil {
		t.Fatalf("the new principal's check = %v", err)
	}
	for i := 0; i < SOPINFreeFailures; i++ {
		_ = f.l.Check(context.Background(), "node", "cert:legit-2", f.wrong, f.isWrong)
	}
	if _, ok := state.principals["cert:legit-2"]; !ok {
		t.Fatal("a new principal was pushed to overflow although the old entries had aged out")
	}
	if len(state.principals) >= SOPINMaxTrackedPrincipals {
		t.Fatalf("stale entries were not evicted: %d tracked", len(state.principals))
	}
}

// TestCallerPrincipal_HolderIsNotTheUserOfTheSameName: a holder certificate
// whose CN is "alice" and the user alice of the same tenant are different
// principals. The holder's own wrong SO PINs lock out only the holder, and the
// holder having presented the right PIN does not make the user known-good
// under a held aggregate cap.
func TestCallerPrincipal_HolderIsNotTheUserOfTheSameName(t *testing.T) {
	user := CallerPrincipal(WithIdentity(context.Background(), &Identity{Subject: "alice", TenantID: "acme"}))
	holder := CallerPrincipal(WithIdentity(context.Background(), &Identity{
		Subject:  "alice",
		TenantID: "acme",
		Metadata: map[string]string{HolderProfileMetadataKey: holderprofile.Custodian},
	}))
	if holder != "holder:acme/alice" || user != "id:acme/alice" {
		t.Fatalf("principals = holder %q, user %q", holder, user)
	}

	f := newLockoutFixture()
	for i := 0; i < SOPINFreeFailures; i++ {
		_ = f.l.Check(context.Background(), "acme", holder, f.wrong, f.isWrong)
	}
	var locked *SOPINLockedError
	if err := f.l.Check(context.Background(), "acme", holder, f.right, f.isWrong); !errors.As(err, &locked) {
		t.Fatalf("the holder after its own free failures = %v, want locked", err)
	}
	if err := f.l.Check(context.Background(), "acme", user, f.right, f.isWrong); err != nil {
		t.Fatalf("the user alice after the holder's failures = %v, want admitted", err)
	}

	f = newLockoutFixture()
	if err := f.l.Check(context.Background(), "acme", holder, f.right, f.isWrong); err != nil {
		t.Fatal(err)
	}
	f.l.keys["acme"].aggregate = soPINAggregate{windowStart: f.now, failures: SOPINAggregateFailures}
	if err := f.l.Check(context.Background(), "acme", user, f.right, f.isWrong); !errors.As(err, &locked) {
		t.Fatalf("the user alice under the held cap, after the holder was known good = %v, want refused", err)
	}
	if err := f.l.Check(context.Background(), "acme", holder, f.right, f.isWrong); err != nil {
		t.Fatalf("the known-good holder under the held cap = %v, want admitted", err)
	}
}

// TestSOPINLockout_StoreHoldsDigestsNotPrincipals: what the known-good store
// is handed is a domain-separated digest of key and principal -- never the
// principal id -- and the digest differs per key, so one key's record says
// nothing about another's.
func TestSOPINLockout_StoreHoldsDigestsNotPrincipals(t *testing.T) {
	store := &memKnownGood{}
	l := NewSOPINLockout(nil, WithKnownGoodStore(store))
	so := "id:system/officer"
	right := func() error { return nil }
	never := func(error) bool { return false }
	for _, key := range []string{"node", "acme"} {
		if err := l.Check(context.Background(), key, so, right, never); err != nil {
			t.Fatal(err)
		}
	}
	node, acme := store.sets["node"], store.sets["acme"]
	if len(node) != 1 || len(acme) != 1 || node[0] == acme[0] {
		t.Fatalf("stored = %v / %v, want one distinct digest per key", node, acme)
	}
	for key, got := range map[string]string{"node": node[0], "acme": acme[0]} {
		if got != knownGoodDigest(key, so) || len(got) != 64 || strings.Contains(got, "officer") {
			t.Fatalf("%s stored %q, want the 64-hex digest of the principal", key, got)
		}
	}
}

// TestSOPINLockout_SlowVerifyHoldsUpOnlyItsOwnKey: a check whose
// verification is slow -- the SO PIN's KDF -- holds its own key's lock, not
// the lockout's: another key's check completes meanwhile, and the slow key's
// next check waits for the first, so its failure is counted before the next
// verification starts.
func TestSOPINLockout_SlowVerifyHoldsUpOnlyItsOwnKey(t *testing.T) {
	l := NewSOPINLockout(nil)
	entered, release := make(chan struct{}, 2), make(chan struct{})
	slowWrong := func() error {
		entered <- struct{}{}
		<-release
		return errWrong
	}
	isWrong := func(err error) bool { return errors.Is(err, errWrong) }
	first := make(chan error, 1)
	go func() { first <- l.Check(context.Background(), "acme", "id:acme/so", slowWrong, isWrong) }()
	<-entered

	other := make(chan error, 1)
	go func() {
		other <- l.Check(context.Background(), "globex", "id:globex/so", func() error { return nil }, isWrong)
	}()
	select {
	case err := <-other:
		if err != nil {
			t.Fatalf("globex check = %v", err)
		}
	case <-time.After(5 * time.Second):
		close(release)
		t.Fatal("another key's check waited on acme's slow verification")
	}

	// acme's second check may not verify until the first has counted.
	second := make(chan int, 1)
	go func() {
		_ = l.Check(context.Background(), "acme", "id:acme/so", func() error {
			failures, _ := l.statusLockedForTest("acme", "id:acme/so")
			second <- failures
			return nil
		}, isWrong)
	}()
	close(release)
	if err := <-first; !errors.Is(err, errWrong) {
		t.Fatalf("first acme check = %v", err)
	}
	if failures := <-second; failures != 1 {
		t.Fatalf("acme's second verification saw %d counted failures, want 1", failures)
	}
}

// TestSOPINLockout_BackoffStartsWhenVerifyReturns: a failure is counted when
// its verification ends, so a KDF slower than the backoff -- PBKDF2 under
// fips140=only -- still leaves the full backoff in force after the free
// failures, and the next check is refused.
func TestSOPINLockout_BackoffStartsWhenVerifyReturns(t *testing.T) {
	f := newLockoutFixture()
	slowWrong := func() error {
		f.now = f.now.Add(2 * SOPINBaseBackoff)
		return errWrong
	}
	for i := 0; i < SOPINFreeFailures; i++ {
		if err := f.l.Check(context.Background(), "node", "", slowWrong, f.isWrong); !errors.Is(err, errWrong) {
			t.Fatalf("failure %d = %v", i, err)
		}
	}
	if _, until := f.l.Status("node", ""); !until.Equal(f.now.Add(SOPINBaseBackoff)) {
		t.Fatalf("locked until %v, want the base backoff from the last verification's end (%v)", until, f.now.Add(SOPINBaseBackoff))
	}
	var locked *SOPINLockedError
	if err := f.l.Check(context.Background(), "node", "", f.right, f.isWrong); !errors.As(err, &locked) || locked.RetryAfter != SOPINBaseBackoff {
		t.Fatalf("the check after the free failures = %v, want locked for the base backoff", err)
	}
}

// statusLockedForTest reads principal's failures for key from inside a verify
// callback, where the key's lock is already held by the calling check.
func (l *SOPINLockout) statusLockedForTest(key, principal string) (int, bool) {
	l.mu.Lock()
	state, ok := l.keys[key]
	l.mu.Unlock()
	if !ok {
		return 0, false
	}
	c, ok := state.principals[principal]
	if !ok {
		return 0, true
	}
	return c.failures, true
}

// TestSOPINLockout_SaveMergesOtherNodesEntries: a save folds in what the
// store holds now -- an entry another node sharing the store wrote after this
// lockout loaded -- instead of overwriting it with this lockout's own set.
func TestSOPINLockout_SaveMergesOtherNodesEntries(t *testing.T) {
	store := &memKnownGood{}
	l := NewSOPINLockout(nil, WithKnownGoodStore(store))
	right := func() error { return nil }
	never := func(error) bool { return false }
	if err := l.Check(context.Background(), "node", "id:system/alice", right, never); err != nil {
		t.Fatal(err)
	}
	otherNode := knownGoodDigest("node", "id:system/bob")
	store.sets["node"] = append(store.sets["node"], otherNode)

	if err := l.Check(context.Background(), "node", "id:system/carol", right, never); err != nil {
		t.Fatal(err)
	}
	got := store.sets["node"]
	for _, want := range []string{knownGoodDigest("node", "id:system/alice"), otherNode, knownGoodDigest("node", "id:system/carol")} {
		if !slices.Contains(got, want) {
			t.Fatalf("stored set %v lost %s", got, want)
		}
	}
	if !l.keys["node"].isKnownGood(otherNode) {
		t.Fatal("the merged entry from another node is not known-good in memory")
	}
}

// TestSOPINLockout_AnUnusedKeyLeavesNoState: a key whose only check was an
// anonymous success records nothing, and once no check holds it its state is
// gone from the map.
func TestSOPINLockout_AnUnusedKeyLeavesNoState(t *testing.T) {
	l := NewSOPINLockout(nil)
	if err := l.Check(context.Background(), "acme", "", func() error { return nil }, func(error) bool { return false }); err != nil {
		t.Fatal(err)
	}
	if _, ok := l.keys["acme"]; ok {
		t.Fatal("an empty key's state outlived its check")
	}
	if failures, until := l.Status("acme", ""); failures != 0 || !until.IsZero() {
		t.Fatalf("Status of an absent key = %d, %v", failures, until)
	}
}

// TestSOPINLockout_MergeUnderCapPressureKeepsOwnEntries pins the documented
// cap-pressure behaviour: when the stored set and this lockout's own together
// exceed SOPINMaxKnownGood, the merge keeps every one of this lockout's own
// entries and drops stored ones, oldest first.
func TestSOPINLockout_MergeUnderCapPressureKeepsOwnEntries(t *testing.T) {
	k := &soPINKeyState{}
	own := []string{"own-1", "own-2", "own-3"}
	for _, d := range own {
		k.addKnownGood(d)
	}
	var stored []string
	for i := 0; i < SOPINMaxKnownGood; i++ {
		stored = append(stored, "stored-"+string(rune('a'+i)))
	}
	k.mergeKnownGood(stored)
	if len(k.knownGood) != SOPINMaxKnownGood {
		t.Fatalf("merged set has %d entries, want the cap %d", len(k.knownGood), SOPINMaxKnownGood)
	}
	for _, d := range own {
		if !k.isKnownGood(d) {
			t.Fatalf("own entry %s dropped under cap pressure: %v", d, k.knownGood)
		}
	}
	dropped := stored[:len(own)]
	for _, d := range dropped {
		if k.isKnownGood(d) {
			t.Fatalf("the oldest stored entry %s survived while the set was over the cap: %v", d, k.knownGood)
		}
	}
}
