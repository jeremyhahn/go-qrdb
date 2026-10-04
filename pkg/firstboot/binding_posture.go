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
	"sync"
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

// DefaultAwaitingAdminCredentialRecheckInterval bounds how often
// AwaitingAdminCredentialProbe re-reads the user store, when its own
// RecheckInterval is left at zero.
const DefaultAwaitingAdminCredentialRecheckInterval = 2 * time.Second

// awaitingAdminCredentialLookupTimeout bounds ONE re-check read so a store
// that cannot answer (no leader yet, a lost quorum) cannot block the
// request that triggered the re-check indefinitely -- every transport gate
// calls AwaitingAdminCredential() on every request, so this is on the hot
// path. A read that times out is treated as "awaiting": fail closed, not
// fail open, exactly like every other read this package cannot complete.
const awaitingAdminCredentialLookupTimeout = 3 * time.Second

// AwaitingAdminCredentialProbe reports whether a node should be in the
// "awaiting admin credential" boot state (go-qrdb#548 review item 3):
// Policy is auth.MTLSBindingRequired and the bootstrap admin holds no live
// certificate binding. Every transport gate (REST, gRPC, native QUIC, MCP,
// the embedded transport, the Unix socket transport) samples this on every
// request, so it caches its verdict for RecheckInterval rather than
// reading the linearizable user store on every single one -- the store is
// read at most once per interval, serialized across concurrent callers,
// and the verdict flips to "not awaiting" within one interval of the
// admin's certificate actually binding, with no restart required.
//
// The zero value is unusable: Users must be set. Policy defaults to the
// zero auth.MTLSBindingPolicy, which is auth.MTLSBindingRequired (not an
// "unset" sentinel, matching every other reader of that type in this
// package) -- a probe built without explicitly setting Policy therefore
// reads as the strict posture, not as "never awaiting".
type AwaitingAdminCredentialProbe struct {
	// Policy is the node's configured auth.mtls_binding_policy. Any value
	// other than auth.MTLSBindingRequired makes AwaitingAdminCredential
	// always report false: this boot state exists ONLY for the posture
	// that refuses to boot an admin-unbound node outright.
	Policy auth.MTLSBindingPolicy
	// Users resolves the bootstrap admin, read through the LINEARIZABLE
	// store -- the same read VerifyAdminCertBinding itself performs.
	Users AdminLookup
	// RecheckInterval bounds how often AwaitingAdminCredential re-reads the
	// store. Zero selects DefaultAwaitingAdminCredentialRecheckInterval. A
	// test may set it near-zero to observe a bind without waiting out the
	// production interval.
	RecheckInterval time.Duration
	// LookupTimeout bounds ONE re-check read. Zero selects the production
	// default (awaitingAdminCredentialLookupTimeout); a test exercising a
	// store that never answers sets it short rather than waiting out the
	// production bound.
	LookupTimeout time.Duration

	mu        sync.Mutex
	checked   bool
	lastCheck time.Time
	awaiting  bool
}

// AwaitingAdminCredential implements the AwaitingAdminCredentialProbe
// interface every transport gate's ServerOption declares (one per
// transport package, each a structurally identical, independently
// declared interface -- see e.g. pkg/api/rest's awaitingAdminCredentialProbe).
func (p *AwaitingAdminCredentialProbe) AwaitingAdminCredential() bool {
	if p.Policy != auth.MTLSBindingRequired {
		return false
	}
	interval := p.RecheckInterval
	if interval <= 0 {
		interval = DefaultAwaitingAdminCredentialRecheckInterval
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.checked && time.Since(p.lastCheck) < interval {
		return p.awaiting
	}
	timeout := p.LookupTimeout
	if timeout <= 0 {
		timeout = awaitingAdminCredentialLookupTimeout
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	p.awaiting = isAwaitingAdminCredential(VerifyAdminCertBinding(ctx, p.Users, time.Now().UTC()))
	p.checked = true
	p.lastCheck = time.Now()
	return p.awaiting
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
