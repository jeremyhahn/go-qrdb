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

package node

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/firstboot"
)

// FirstBoot runs the canonical first-boot (firstboot.Initialize) under THIS
// node's auth.mtls_binding_policy, and -- when that policy is "required" --
// verifies against the node's own user store that the bootstrap admin can log
// in by certificate before reporting success (go-qrdb#517).
//
// It is how an embedder bootstraps a node without being able to lock the admin
// out: the policy the node enforces on every login is the policy first-boot
// is held to, whatever deps.MTLSBindingPolicy says. The node's own policy is
// authoritative and always overrides deps.MTLSBindingPolicy, in either
// direction -- not merely raised, since auth.MTLSBindingPolicy's zero value
// is auth.MTLSBindingRequired (not an "unset" sentinel), so a deps literal
// that never sets the field already reads as the strict policy and a
// one-directional raise could never lower it back to what the node actually
// enforces. Call it after the node has started.
//
// Calling firstboot.Initialize directly instead forgoes the node's policy for
// that run; the node still refuses to START on its next boot when an existing
// admin holds no live binding under "required".
func (n *Node) FirstBoot(ctx context.Context, deps firstboot.Dependencies) (*firstboot.Result, error) {
	return firstBootUnderPolicy(ctx, deps, auth.MTLSBindingPolicy(n.bindingPolicy.Load()), n.UserService())
}

// FirstBootWithCeremony is FirstBoot, but first resolves
// deps.AdminCertPEM / deps.AdminCertPool / deps.Binder from the durable
// ceremony-initialize -> first-boot hand-off (go-qrdb#548), via the SAME
// pkg/firstboot wiring cmd/qrdbd's own first boot uses --
// firstboot.ResolveAdminCertForFirstBoot, firstboot.AdminCertPoolFromCABundle
// and firstboot.UserCertBinder -- rather than a copy of it. This is the
// shared production path behind "every composition enrols the bootstrap
// admin's binding" (docs/identity-rbac/architecture.md) for an embedder
// composing through sdk/go/node, exactly as bootstrapUserRoleStores /
// runFirstBoot is for qrdbd.
//
// store is the durable, replicated pending-admin-cert record SOMETHING in
// this cluster wrote via firstboot.WritePendingAdminCert after running the
// ceremony -- possibly this node, possibly another one. PendingAdminCertStore
// builds one over this node's own KV plane. nil is valid: an embedder with
// no cluster-wide record to resolve (one that already set
// deps.AdminCertPEM/.CA/.Binder itself) should call FirstBoot instead.
// caBundlePEM, when non-empty, is the trust anchor the resolved certificate
// must chain to (ordinarily the ceremony CA's own CABundle()).
func (n *Node) FirstBootWithCeremony(ctx context.Context, store firstboot.PendingAdminCertStore, caBundlePEM []byte, timeout time.Duration, deps firstboot.Dependencies) (*firstboot.Result, error) {
	policy := auth.MTLSBindingPolicy(n.bindingPolicy.Load())
	users := n.UserService()

	adminCertPEM, err := firstboot.ResolveAdminCertForFirstBoot(ctx, store, users, policy, timeout)
	if err != nil {
		// *NoPendingAdminCredentialError means the bounded wait ran out with
		// neither a live binding nor a pending record -- under "required",
		// that no longer aborts the boot (go-qrdb#560 review B2, matching
		// the reversal already in effect at wireClientAndServer's own
		// boot-time check, go-qrdb#548 review item 3): first boot proceeds
		// with no certificate to bind, leaving the admin unbound, and the
		// node comes up into "awaiting admin credential" once that later
		// check decides the posture. Every other error here (a genuine
		// store fault) still aborts.
		var noCred *firstboot.NoPendingAdminCredentialError
		if !errors.As(err, &noCred) {
			return nil, err
		}
		slog.Warn(`node: no admin certificate binding and no pending admin credential record; `+
			`continuing without one -- the node will boot into "awaiting admin credential"`,
			"error", err)
	}
	deps.AdminCertPEM = adminCertPEM
	deps.AdminCertPool = firstboot.AdminCertPoolFromCABundle(caBundlePEM)
	deps.Binder = firstboot.UserCertBinder{Users: users}

	result, err := firstBootUnderPolicy(ctx, deps, policy, users)
	if err != nil {
		return nil, err
	}
	// Best-effort: the binding already committed (or the admin already held
	// one), so a failure clearing the now-superfluous record is logged, not
	// fatal, matching cmd/qrdbd/start.go's own treatment of the same step.
	if err := firstboot.ClearPendingAdminCert(ctx, store); err != nil {
		slog.Warn("node: failed to clear the pending admin credential record", "error", err)
	}
	return result, nil
}

// firstBootUnderPolicy is FirstBoot with the node's state passed in, so the
// policy handling is testable without standing up a node. users is the store
// the node's auth middleware authenticates against; the verdict is read from
// it rather than from deps.Users, which may be an adapter over something else.
//
// policy -- the node's own configured auth.mtls_binding_policy -- always wins
// over whatever deps.MTLSBindingPolicy already held, since that field's zero
// value is indistinguishable from an explicit auth.MTLSBindingRequired.
func firstBootUnderPolicy(ctx context.Context, deps firstboot.Dependencies, policy auth.MTLSBindingPolicy, users firstboot.AdminLookup) (*firstboot.Result, error) {
	deps.MTLSBindingPolicy = policy
	result, err := firstboot.Initialize(ctx, deps)
	if err != nil {
		return nil, err
	}
	if policy == auth.MTLSBindingRequired {
		if verifyErr := firstboot.VerifyAdminCertBinding(ctx, users, time.Now().UTC()); verifyErr != nil {
			// A no-live-binding verdict is tolerated when no
			// admin-credential path was configured for this run at all
			// (go-qrdb#560 review B2): there was nothing this run could
			// have bound, so the admin stays unbound and
			// wireClientAndServer's own AwaitingAdminCredentialProbe is
			// the posture that takes over -- not a boot refusal. This is
			// the SAME tolerance firstboot.Initialize's own
			// verifyRequiredOutcome applies internally against deps.Users;
			// re-applying it here, against users (the node's own
			// authentication store, which may differ from deps.Users), is
			// what lets TestFirstBootUnderPolicy_VerdictReadFromTheNodesStore
			// still catch a certificate bound into the wrong store: that
			// case always has a path configured, so HasAdminCertPath keeps
			// refusing it.
			if !firstboot.HasAdminCertPath(deps) {
				var refusal *firstboot.AdminCertBindingRequiredError
				if errors.As(verifyErr, &refusal) &&
					(refusal.Reason == firstboot.AdminBindingNone || refusal.Reason == firstboot.AdminBindingAllExpired) {
					return result, nil
				}
			}
			return nil, verifyErr
		}
	}
	return result, nil
}

// verifyExistingAdminBinding is the node's boot-time check under "required":
// an admin that EXISTS must hold a live binding, or the node refuses to start
// (the owner's ruling: refuse to boot rather than run locked out).
//
// An admin that does not exist yet is not a lockout -- first-boot has not run,
// and Node.FirstBoot enforces the policy when it does -- so it is logged and
// admitted. A lookup that fails for any other reason is retried until timeout
// (a clustered node may not have a leader yet) and then refuses the boot.
func verifyExistingAdminBinding(ctx context.Context, users firstboot.AdminLookup, timeout time.Duration) error {
	err := firstboot.VerifyAdminCertBindingWithin(ctx, users, timeout)
	var refusal *firstboot.AdminCertBindingRequiredError
	if errors.As(err, &refusal) && refusal.Reason == firstboot.AdminBindingMissing {
		slog.Info("node: auth.mtls_binding_policy is \"required\" and the bootstrap admin does not exist yet; Node.FirstBoot enforces the policy when it creates it")
		return nil
	}
	return err
}
