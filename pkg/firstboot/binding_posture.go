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
	"errors"
	"log/slog"
	"sync/atomic"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
	"github.com/jeremyhahn/go-qrdb/pkg/user"
)

// AdminLookup is the narrow read surface the binding-posture check needs to
// find the bootstrap admin. qrdb's user.UserServicer satisfies it; an adapter
// over another IAM stack implements it to let Initialize verify an existing
// admin under the "required" posture.
type AdminLookup interface {
	GetUserByUsername(ctx context.Context, tenantID, username string) (*user.User, error)
}

// VerifyAdminCertBinding reports whether the bootstrap admin (system tenant,
// "admin") could log in by certificate under auth.mtls_binding_policy
// "required" (go-qrdb#517): it must exist and hold at least one certificate
// binding that has not expired at now.
//
// A composer that selects "required" calls this at boot, after first-boot has
// run, and REFUSES TO BOOT on a non-nil error. That is the owner's ruling on
// the lockout the posture creates: a node that started would admit nobody who
// could repair it, because the way back in needs a permission that needs a
// login. A boot failure names the cause while the operator can still change the
// configuration.
//
// Every refusal is an *AdminCertBindingRequiredError naming the account and
// the reason; a lookup that fails for any reason other than not-found is an
// *AdminLookupError, which also refuses — the posture cannot be shown to hold.
func VerifyAdminCertBinding(ctx context.Context, users AdminLookup, now time.Time) error {
	if users == nil {
		return adminBindingRefusal(AdminBindingUnverifiable)
	}
	u, err := users.GetUserByUsername(ctx, tenantid.System, adminUsername)
	if err != nil {
		var unf *user.UserNotFoundError
		if errors.As(err, &unf) {
			return adminBindingRefusal(AdminBindingMissing)
		}
		return &AdminLookupError{Cause: err}
	}
	if err := verifyAdminBindings(u, now); err != nil {
		return err
	}
	user.ObserveBootstrapAdminBindings(slog.Default(), u, now)
	return nil
}

// VerifyAdminCertBindingWithin is VerifyAdminCertBinding with a bounded wait
// for a lookup that FAILS (*AdminLookupError): it retries until timeout, then
// returns the last error. A verdict -- the admin is missing, unbound, or
// bound -- is returned at once; only "could not read" is worth waiting out.
//
// Composers call it at boot with a LINEARIZABLE user store, so on a follower
// the read waits for the admin's bindings to replicate rather than judging a
// record that has not caught up with the leader (a binding enrolled on the
// leader after the admin was created would otherwise read as "no binding"
// and refuse a boot that should succeed). The retries absorb the window
// before a freshly started node can serve such a read.
//
// The whole wait runs under context.WithDeadline, so a lookup that blocks
// instead of failing is cut off at the bound too, not only the sleeps between
// attempts.
func VerifyAdminCertBindingWithin(ctx context.Context, users AdminLookup, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	dctx, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()
	backoff := 50 * time.Millisecond
	for {
		err := VerifyAdminCertBinding(dctx, users, time.Now().UTC())
		var lookup *AdminLookupError
		if !errors.As(err, &lookup) || !time.Now().Before(deadline) {
			return err
		}
		select {
		case <-dctx.Done():
			return err
		case <-time.After(backoff):
		}
		if backoff *= 2; backoff > time.Second {
			backoff = time.Second
		}
	}
}

// AdminBindingLookupTimeout is the bounded wait composers give
// VerifyAdminCertBindingWithin at boot. It matches the daemon's
// admin-replication wait.
const AdminBindingLookupTimeout = 30 * time.Second

// AdminBoundNotifier is told once the bootstrap admin's certificate
// successfully binds while a node is already serving (go-qrdb#560 review
// B1): the only way a running node leaves "awaiting admin credential"
// without a restart. *AwaitingAdminCredentialProbe implements it;
// CeremonyServiceWithAdminRebind (pkg/service) calls it after a reissued
// certificate binds. Defined as an interface, matching this package's own
// AdminLookup/CertBinder, so a test can substitute a deterministic fake.
type AdminBoundNotifier interface {
	AdminBound()
}

// AwaitingAdminCredentialProbe reports whether a node should be in the
// "awaiting admin credential" boot state (go-qrdb#548 review item 3):
// Policy is auth.MTLSBindingRequired and the bootstrap admin held no live
// certificate binding the last time this was decided. Every transport gate
// (REST, gRPC, native QUIC, MCP, the embedded transport, the Unix socket
// transport) samples AwaitingAdminCredential on every request; it does no
// I/O and takes no lock, reading only an atomic flag.
//
// The state is decided ONCE, at boot, by Initialize, from the SAME bounded
// VerifyAdminCertBindingWithin lookup a "required"-posture composer already
// performs before constructing the probe (go-qrdb#560 review B1, replacing
// a live per-request re-check that held a mutex across a user-store lookup
// of up to 3s on the hot path of every request). A running node leaves that
// state only through AdminBound, called by the one path that can bind the
// admin certificate while serving (CeremonyServiceWithAdminRebind's
// reissue) -- never by re-reading the store -- so it never RE-ENTERS
// awaiting merely because that certificate later expires. An admin whose
// binding expires after this process already left "awaiting" is recovered
// by restarting into awaiting mode, then a fresh SO-PIN-gated reissue.
//
// The zero value is immediately usable and reports not-awaiting: a probe an
// embedder constructs with NewAwaitingAdminCredentialProbe but has not yet
// called Initialize on (because it is wired as an AdminBoundNotifier into
// ceremony composition before the boot-time lookup that decides its state
// has run) behaves exactly like a nil probe -- a pure passthrough -- until
// Initialize runs.
type AwaitingAdminCredentialProbe struct {
	// Policy is the node's configured auth.mtls_binding_policy, set by
	// Initialize. Exposed for a caller that wants to log or report it;
	// AwaitingAdminCredential itself never reads it directly.
	Policy auth.MTLSBindingPolicy

	awaiting atomic.Bool
}

// NewAwaitingAdminCredentialProbe returns a probe that reports not-awaiting
// until Initialize decides its boot state. Its pointer identity is stable
// from construction, so a composer can wire the SAME probe as both
// CeremonyServiceWithAdminRebind's AdminBoundNotifier -- needed while
// composing the ceremony service, before the boot-time binding lookup has
// even run -- and the transport gates' probe, needed after, with no
// indirection between the two.
func NewAwaitingAdminCredentialProbe() *AwaitingAdminCredentialProbe {
	return &AwaitingAdminCredentialProbe{}
}

// Initialize decides this probe's awaiting state ONCE, at boot, from
// bootLookupErr -- the result of the SAME bounded VerifyAdminCertBindingWithin
// call a "required"-posture composer already performs. It is not safe to
// call concurrently with itself or with AdminBound; a composer calls it
// exactly once, synchronously, during boot, before any transport gate can
// observe this probe.
//
// policy other than auth.MTLSBindingRequired always leaves the probe
// reporting not-awaiting, regardless of bootLookupErr: this boot state
// exists only for the posture that refuses to boot an admin-unbound node
// outright.
//
// A failed lookup -- bootLookupErr is an *AdminLookupError, meaning the
// check never reached a verdict -- is NOT "awaiting": it is returned here
// as a typed error so the caller can log or report it, while the probe
// itself is left reporting not-awaiting, so an infrastructure fault
// distinct from the admin's own binding state does not additionally lock
// the node down to the two ceremony verbs this state serves.
func (p *AwaitingAdminCredentialProbe) Initialize(policy auth.MTLSBindingPolicy, bootLookupErr error) error {
	p.Policy = policy
	if policy != auth.MTLSBindingRequired {
		return nil
	}
	var lookup *AdminLookupError
	if errors.As(bootLookupErr, &lookup) {
		return lookup
	}
	p.awaiting.Store(isAwaitingAdminCredential(bootLookupErr))
	return nil
}

// AwaitingAdminCredential implements the AwaitingAdminCredentialProbe
// interface every transport gate's ServerOption declares (one per
// transport package, each a structurally identical, independently
// declared interface -- see e.g. pkg/api/rest's awaitingAdminCredentialProbe).
// It does no I/O: Initialize decided this node's state once, at boot, and
// AdminBound is the only way it ever changes afterward.
func (p *AwaitingAdminCredentialProbe) AwaitingAdminCredential() bool {
	return p.awaiting.Load()
}

// AdminBound implements AdminBoundNotifier: it tells the probe the
// bootstrap admin's certificate just bound while this node was serving,
// the only way a running node leaves "awaiting admin credential" without a
// restart. Idempotent, and permanent for the life of this probe instance --
// a later certificate expiry never re-enters that state.
func (p *AwaitingAdminCredentialProbe) AdminBound() {
	p.awaiting.Store(false)
}

// isAwaitingAdminCredential reports whether verifyErr -- VerifyAdminCertBinding's
// result -- means the node should be in the "awaiting admin credential" boot
// state. An admin that does not exist YET is not a lockout (matching
// verifyExistingAdminBinding's identical carve-out, sdk/go/node's own
// boot-time check): whatever eventually creates it -- Node.FirstBoot, or
// pkg/firstboot.Initialize in cmd/qrdbd's boot sequence, both of which hold
// the SAME policy against the account they create -- enforces the posture
// at that point, not this probe before the account exists at all. Every
// other refusal reason (no binding, all expired, a lifecycle or
// permitted-method block, or the lookup itself failing) means an admin that
// DOES exist and cannot log in, which is exactly the state this boot state
// exists for.
func isAwaitingAdminCredential(verifyErr error) bool {
	if verifyErr == nil {
		return false
	}
	var refusal *AdminCertBindingRequiredError
	if errors.As(verifyErr, &refusal) && refusal.Reason == AdminBindingMissing {
		return false
	}
	return true
}

// verifyAdminBindings is the predicate over a resolved admin record: could
// this account log in by certificate under the "required" posture at now?
//
// Holding a live binding is not enough on its own. The authentication path
// also refuses an account whose lifecycle state does not permit a login
// (User.CanAuthenticate) and one whose permitted-method policy excludes mTLS
// (User.AuthMethods), so the boot check refuses them too -- an admin
// suspended, or restricted to another method, is exactly as locked out as one
// with no key. The checks are user.AdminLoginBlocker's, the same predicates
// the user store's guard applies, run in the order the middleware applies them.
func verifyAdminBindings(u *user.User, now time.Time) error {
	if u == nil {
		return adminBindingRefusal(AdminBindingMissing)
	}
	blocker := user.AdminLoginBlocker(u, now)
	if blocker == "" {
		return nil
	}
	if blocker == user.AdminLockoutNoLiveBinding && len(u.CertBindings) > 0 {
		return adminBindingRefusal(AdminBindingAllExpired)
	}
	return adminBindingRefusal(adminBindingReasons[blocker])
}

// adminBindingReasons maps the shared login predicate onto this package's
// refusal reasons. No live binding splits by whether any binding exists at all
// (AdminBindingNone versus AdminBindingAllExpired), which verifyAdminBindings
// decides.
var adminBindingReasons = map[user.AdminLockoutReason]AdminCertBindingReason{
	user.AdminLockoutLifecycle:        AdminBindingCannotAuthenticate,
	user.AdminLockoutMTLSNotPermitted: AdminBindingMTLSNotPermitted,
	user.AdminLockoutNoLiveBinding:    AdminBindingNone,
}

// checkBindingPosture is Initialize's pre-flight under the "required" posture.
// It runs BEFORE any write, so a configuration that would lock the admin out
// fails without having created anything:
//
//   - The admin already exists and already holds a live binding
//     (verifyAdminBindings): nothing to do, proceed.
//   - The admin already exists with NO live binding: this is the
//     create-but-never-bound gap a crash between createAdminUser and
//     deps.Binder.BindAdminCert leaves (go-qrdb#548 review: B6) -- a
//     CertBinder AND an admin certificate path must be wired so Initialize
//     can repair it (binding an EXISTING unbound admin, not creating a
//     second one), or this refuses exactly as the "does not exist yet" case
//     below does, naming the same missing piece.
//   - It does not exist yet: this run creates it, so this run must be able
//     to bind it — a CertBinder AND an admin certificate path must be wired.
//
// Under the default posture it does nothing.
func checkBindingPosture(ctx context.Context, deps Dependencies) error {
	if deps.MTLSBindingPolicy != auth.MTLSBindingRequired {
		return nil
	}
	lookup, ok := deps.Users.(AdminLookup)
	if !ok {
		return adminBindingRefusal(AdminBindingUnverifiable)
	}
	u, err := lookup.GetUserByUsername(ctx, tenantid.System, adminUsername)
	if err == nil {
		verifyErr := verifyAdminBindings(u, time.Now().UTC())
		if verifyErr == nil {
			return nil
		}
		// Only a MISSING or fully-EXPIRED binding is repairable by issuing
		// a fresh certificate -- an account the lifecycle or
		// permitted-method policy itself blocks (AdminBindingCannotAuthenticate,
		// AdminBindingMTLSNotPermitted) is not fixed by a new binding, and
		// must still refuse with that specific reason, not be papered over
		// by a generic "no credential path" check.
		var refusal *AdminCertBindingRequiredError
		if errors.As(verifyErr, &refusal) && (refusal.Reason == AdminBindingNone || refusal.Reason == AdminBindingAllExpired) {
			return checkAdminCertPathAvailable(deps)
		}
		return verifyErr
	}
	var unf *user.UserNotFoundError
	if !errors.As(err, &unf) {
		return &AdminLookupError{Cause: err}
	}
	return checkAdminCertPathAvailable(deps)
}

// checkAdminCertPathAvailable refuses when Initialize has no way to issue or
// bind the bootstrap admin's certificate: no CertBinder wired, or no
// admin-credential path wired. Shared by checkBindingPosture's two "this run
// must be able to bind it" cases -- a brand-new admin and an existing,
// still-unbound one are refused with the identical reasons for the identical
// cause.
func checkAdminCertPathAvailable(deps Dependencies) error {
	if deps.Binder == nil {
		return adminBindingRefusal(AdminBindingNoBinder)
	}
	if !hasAdminCertPath(deps) {
		return adminBindingRefusal(AdminBindingNoCertPath)
	}
	return nil
}

// verifyRequiredOutcome re-reads the admin after first-boot and applies
// verifyAdminBindings to what the store now holds. It does nothing under the
// default posture.
func verifyRequiredOutcome(ctx context.Context, deps Dependencies) error {
	if deps.MTLSBindingPolicy != auth.MTLSBindingRequired {
		return nil
	}
	lookup, ok := deps.Users.(AdminLookup)
	if !ok {
		return adminBindingRefusal(AdminBindingUnverifiable)
	}
	return VerifyAdminCertBinding(ctx, lookup, time.Now().UTC())
}

// hasAdminCertPath reports whether issueAdminCert would produce a certificate:
// Path C (an adopted certificate), Path B (a CSR and a Signer), or Path A (a
// CA). It mirrors issueAdminCert's own switch, which is the definition.
func hasAdminCertPath(deps Dependencies) bool {
	return len(deps.AdminCertPEM) > 0 ||
		(len(deps.AdminCSRPEM) > 0 && deps.Signer != nil) ||
		deps.CA != nil
}

func adminBindingRefusal(reason AdminCertBindingReason) error {
	return &AdminCertBindingRequiredError{
		TenantID: tenantid.System,
		Username: adminUsername,
		Reason:   reason,
	}
}
