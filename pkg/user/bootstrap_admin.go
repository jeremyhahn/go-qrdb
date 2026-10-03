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
	"log/slog"
	"math"
	"slices"
	"strconv"
	"sync"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
	"github.com/jeremyhahn/go-qrdb/pkg/util/metrics"
)

// BootstrapAdminUsername is the username first-boot creates the platform
// administrator under, in the system tenant. Under auth.mtls_binding_policy
// "required" it is the account the node refuses to boot without (go-qrdb#517),
// so the user store and first-boot must agree on it: this is the one spelling.
const BootstrapAdminUsername = "admin"

// IsBootstrapAdmin reports whether u is the platform administrator first-boot
// creates: BootstrapAdminUsername in the system tenant.
//
// It is the identity the boot check resolves, so it is the identity the store
// guards. The store evaluates it on the record AS COMMITTED, before a change is
// applied, and refuses the rename that would move the admin off it. Under
// "required" the admin therefore cannot stop being the admin, and the username
// index keeps any other account from taking the name while the admin holds it.
// (The admin is not a System user: first boot creates it with System == false,
// so that flag cannot identify it.)
func IsBootstrapAdmin(u *User) bool {
	return u != nil && u.Username == BootstrapAdminUsername && tenantid.IsSystem(u.TenantID)
}

// WithBootstrapAdminBindingRequired makes the store refuse every change that
// would leave the bootstrap admin unable to log in by certificate
// (go-qrdb#517). Composers set it when auth.mtls_binding_policy is "required":
// under that posture a node whose admin cannot log in refuses to boot, so the
// change that would cause it is refused instead, with
// *BootstrapAdminLockoutError, on every transport.
//
// Refused: deleting the admin, renaming it, moving it to a lifecycle state
// that cannot authenticate, removing mtls from its permitted methods, and
// leaving it with no live certificate binding -- whichever verb does it
// (RemoveCertBinding, AddCertBinding, UpdateUser, DisableUser, DeleteUser).
// Each is refused only when it would BREAK a requirement the admin currently
// meets, so a change that repairs, or leaves alone, a requirement that is
// already broken still goes through. Other accounts are unaffected.
//
// The recovery path for an admin that is locked out anyway -- its last key
// expired -- is operational, not an API call: set auth.mtls_binding_policy to
// "enrolled" and restart, enrol a key, then return to "required".
func WithBootstrapAdminBindingRequired() Option {
	return func(s *kvUserStore) {
		s.requireAdminBinding = true
	}
}

// AdminLockoutReason names the requirement a refused change would have broken.
type AdminLockoutReason string

// The reasons a change to the bootstrap admin is refused under "required".
const (
	// AdminLockoutDelete: the change deletes the admin.
	AdminLockoutDelete AdminLockoutReason = "delete"
	// AdminLockoutRename: the change renames the admin, so the boot check
	// would no longer find it.
	AdminLockoutRename AdminLockoutReason = "rename"
	// AdminLockoutLifecycle: the change moves the admin to a lifecycle state
	// that cannot authenticate (suspended, locked, disabled, deleting).
	AdminLockoutLifecycle AdminLockoutReason = "lifecycle"
	// AdminLockoutMTLSNotPermitted: the change removes mtls from the admin's
	// permitted authentication methods.
	AdminLockoutMTLSNotPermitted AdminLockoutReason = "mtls_not_permitted"
	// AdminLockoutNoLiveBinding: the change leaves the admin with no
	// unexpired certificate binding.
	AdminLockoutNoLiveBinding AdminLockoutReason = "no_live_binding"
)

// adminLockoutRemedies is what the operator does instead, per reason.
var adminLockoutRemedies = map[AdminLockoutReason]string{
	AdminLockoutDelete:           "the admin cannot be deleted under this policy",
	AdminLockoutRename:           "the admin cannot be renamed under this policy",
	AdminLockoutLifecycle:        "the admin must stay in a state that can log in",
	AdminLockoutMTLSNotPermitted: "the admin's permitted methods must include mtls",
	AdminLockoutNoLiveBinding:    "enrol a replacement key first",
}

// BootstrapAdminLockoutError is returned when the store was built
// WithBootstrapAdminBindingRequired and a change would leave the bootstrap
// admin unable to log in by certificate -- the state in which the node
// refuses to boot. Transports report it as a conflict with the account's
// current state: 409 on REST, FAILED_PRECONDITION on gRPC and QUIC.
type BootstrapAdminLockoutError struct {
	UserID uint64
	Reason AdminLockoutReason
}

// Error names the refused change and what to do instead.
func (e *BootstrapAdminLockoutError) Error() string {
	return "user: refusing change to the bootstrap admin (user " + strconv.FormatUint(e.UserID, 10) +
		", reason " + string(e.Reason) + `): auth.mtls_binding_policy is "required", so the admin could no longer ` +
		"log in and the node would refuse to boot; " + adminLockoutRemedies[e.Reason]
}

// hasLiveBinding reports whether any binding in bs is unexpired at now.
func hasLiveBinding(bs []CertBinding, now time.Time) bool {
	for _, b := range bs {
		if !b.IsExpired(now) {
			return true
		}
	}
	return false
}

// mtlsPermitted reports whether u's method policy admits a certificate login.
// An empty policy states no restriction.
func mtlsPermitted(u *User) bool {
	return len(u.AuthMethods) == 0 || slices.Contains(u.AuthMethods, auth.MethodMTLS)
}

// AdminLoginBlocker names the first requirement u fails for a certificate
// login under "required" at now, in the order the authentication path applies
// them -- lifecycle, method policy, live binding -- or "" when u could log in.
// It does not judge identity: the caller has already resolved u as the admin.
// First-boot's boot check and the store's guard share these predicates, so the
// two cannot disagree about what "the admin can log in" means.
func AdminLoginBlocker(u *User, now time.Time) AdminLockoutReason {
	switch {
	case !u.CanAuthenticate():
		return AdminLockoutLifecycle
	case !mtlsPermitted(u):
		return AdminLockoutMTLSNotPermitted
	case !hasLiveBinding(u.CertBindings, now):
		return AdminLockoutNoLiveBinding
	}
	return ""
}

// adminLockout reports which requirement the change from before to after
// BREAKS for the bootstrap admin, or "" when it breaks none. after is nil for
// a deletion. A requirement before already fails is not reported: the change
// did not cause it, and refusing it would refuse the repair as well.
func adminLockout(before, after *User, now time.Time) AdminLockoutReason {
	switch {
	case !IsBootstrapAdmin(before):
		return ""
	case after == nil:
		return AdminLockoutDelete
	case !IsBootstrapAdmin(after):
		return AdminLockoutRename
	case before.CanAuthenticate() && !after.CanAuthenticate():
		return AdminLockoutLifecycle
	case mtlsPermitted(before) && !mtlsPermitted(after):
		return AdminLockoutMTLSNotPermitted
	case hasLiveBinding(before.CertBindings, now) && !hasLiveBinding(after.CertBindings, now):
		return AdminLockoutNoLiveBinding
	}
	return ""
}

// guardBootstrapAdmin refuses the change from before -- the record as
// committed, which the caller's conditional write is based on -- to after (nil
// for a deletion) when the store is built WithBootstrapAdminBindingRequired
// and the change would lock the bootstrap admin out.
//
// Every write path calls it before its first index mutation, so a refusal is
// a guaranteed no-op. And because before is the base of the caller's
// compare-and-swap, two concurrent changes that are each safe alone cannot
// both commit: the loser re-reads and is judged against the winner's record.
func (s *kvUserStore) guardBootstrapAdmin(before, after *User) error {
	if !s.requireAdminBinding {
		return nil
	}
	if reason := adminLockout(before, after, time.Now().UTC()); reason != "" {
		return &BootstrapAdminLockoutError{UserID: before.ID, Reason: reason}
	}
	return nil
}

// AdminBindingExpiryWarning is how close to its NotAfter the bootstrap admin's
// LAST live certificate binding may come before ObserveBootstrapAdminBindings
// warns. Under "required" the moment it passes is the moment the node stops
// being able to boot, so the warning has to arrive while an operator can still
// enrol a replacement key.
const AdminBindingExpiryWarning = 30 * 24 * time.Hour

// adminBindingNotAfter exports the admin's deadline as a metric: the Unix time
// at which its last live binding expires, +Inf when one never expires. It is a
// TIMESTAMP rather than a countdown so that it stays true between refreshes;
// it is refreshed by every boot check and by every committed change to the
// admin. Alert on it against time():
//
//	qrdb_auth_admin_cert_binding_not_after_timestamp_seconds - time() < 30 * 86400
var adminBindingNotAfter = sync.OnceValue(func() *metrics.Gauge {
	return metrics.NewGauge(metrics.GaugeOpts{
		Namespace: "qrdb",
		Subsystem: "auth",
		Name:      "admin_cert_binding_not_after_timestamp_seconds",
		Help: "Unix time at which the bootstrap admin's last unexpired certificate binding expires " +
			"(+Inf when a binding never expires). Under auth.mtls_binding_policy \"required\" " +
			"the node refuses to boot after it.",
	})
})

// recordAdminBindingDeadline sets the metric above from u's bindings and
// returns the time remaining: -1 when a live binding never expires, 0 when
// none is live. With no live binding the gauge carries the latest expiry
// seen, or 0 when u is nil or holds no bindings.
func recordAdminBindingDeadline(u *User, now time.Time) time.Duration {
	var bindings []CertBinding
	if u != nil {
		bindings = u.CertBindings
	}
	var latest time.Time
	live := false
	for _, b := range bindings {
		if b.IsExpired(now) {
			if b.NotAfter.After(latest) {
				latest = b.NotAfter
			}
			continue
		}
		if b.NotAfter.IsZero() {
			adminBindingNotAfter().Set(math.Inf(1))
			return -1
		}
		if !live || b.NotAfter.After(latest) {
			latest = b.NotAfter
		}
		live = true
	}
	if latest.IsZero() {
		adminBindingNotAfter().Set(0)
	} else {
		adminBindingNotAfter().Set(float64(latest.Unix()))
	}
	if !live {
		return 0
	}
	return latest.Sub(now)
}

// ObserveBootstrapAdminBindings records the bootstrap admin's certificate
// deadline on the metric above and warns when it is under
// AdminBindingExpiryWarning away. It returns the time remaining, -1 when a
// live binding never expires, and 0 when no binding is live.
//
// The warning belongs to the "required" posture: the boot check that calls
// this runs only under it, and a user store calls it only when built
// WithBootstrapAdminBindingRequired. Any other store refreshes the metric
// alone.
func ObserveBootstrapAdminBindings(logger *slog.Logger, u *User, now time.Time) time.Duration {
	remaining := recordAdminBindingDeadline(u, now)
	if remaining > 0 && remaining < AdminBindingExpiryWarning {
		logger.Warn("user: the bootstrap admin's last live certificate binding expires soon; "+
			"under auth.mtls_binding_policy \"required\" the node refuses to boot once it has expired -- enrol a replacement key now",
			slog.String("tenant", u.TenantID),
			slog.String("username", u.Username),
			slog.Time("not_after", now.Add(remaining)),
			slog.Duration("remaining", remaining),
		)
	}
	return remaining
}

// observeBootstrapAdmin refreshes the admin's deadline after a committed
// change to it. u is the admin as committed, or nil when the change removed
// the admin (a deletion, or a rename, both possible only under "enrolled").
func (s *kvUserStore) observeBootstrapAdmin(u *User) {
	now := time.Now().UTC()
	if s.requireAdminBinding && u != nil {
		ObserveBootstrapAdminBindings(s.log(), u, now)
		return
	}
	recordAdminBindingDeadline(u, now)
}
