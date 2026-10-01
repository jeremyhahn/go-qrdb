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
// is held to, whatever deps says. deps.MTLSBindingPolicy is raised to the
// node's, never lowered. Call it after the node has started.
//
// Calling firstboot.Initialize directly instead forgoes the node's policy for
// that run; the node still refuses to START on its next boot when an existing
// admin holds no live binding under "required".
func (n *Node) FirstBoot(ctx context.Context, deps firstboot.Dependencies) (*firstboot.Result, error) {
	return firstBootUnderPolicy(ctx, deps, auth.MTLSBindingPolicy(n.bindingPolicy.Load()), n.UserService())
}

// firstBootUnderPolicy is FirstBoot with the node's state passed in, so the
// policy handling is testable without standing up a node. users is the store
// the node's auth middleware authenticates against; the verdict is read from
// it rather than from deps.Users, which may be an adapter over something else.
func firstBootUnderPolicy(ctx context.Context, deps firstboot.Dependencies, policy auth.MTLSBindingPolicy, users firstboot.AdminLookup) (*firstboot.Result, error) {
	if policy == auth.MTLSBindingRequired {
		deps.MTLSBindingPolicy = auth.MTLSBindingRequired
	}
	result, err := firstboot.Initialize(ctx, deps)
	if err != nil {
		return nil, err
	}
	if deps.MTLSBindingPolicy == auth.MTLSBindingRequired {
		if err := firstboot.VerifyAdminCertBinding(ctx, users, time.Now().UTC()); err != nil {
			return nil, err
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
