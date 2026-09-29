// Copyright 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package node

import (
	"context"

	qrdbconfig "github.com/jeremyhahn/go-qrdb/pkg/config"
	"github.com/jeremyhahn/go-qrdb/pkg/server"
)

// ServeSealedWindow brings up the pre-boot sealed-window listener for a
// node that was constructed in the sealed state: an API-only
// multi-protocol server exposing ONLY the seal-related endpoints
// (barrier status / initialize / unseal, ceremony, credentials) gated by
// the canonical sealed-window allowlist. It mirrors the qrdbd daemon's
// boot-time unseal listener for SDK embedders.
//
// The function blocks until the barrier is unsealed (operator-driven
// through the listener or any other path) or ctx is canceled. On unseal
// the listener is stopped and nil is returned; the caller then completes
// the boot with n.Unseal(ctx, creds) — the barrier is already unsealed,
// so Unseal proceeds directly to the data-plane build. On cancellation
// *server.SealedWindowCanceledError is returned.
//
// Ceremony enclave state is preserved exactly as the daemon preserves
// it: the listener serves the node's own seal-service instances
// (n.Services().Barrier / .Ceremony / .Credential), and those same
// instances are reused by the post-unseal boot, so officer registrations
// and enclave-held pending shares made during the sealed window survive
// the listener teardown.
//
// extraOpts are appended to the listener's server options; use them to
// supply TLS (server.WithTLS), a ceremony CA (server.WithCeremonyCA), or
// any other composition option. When the node is not sealed (seal
// disabled or already unsealed) the call is a no-op and returns nil.
func ServeSealedWindow(ctx context.Context, n *Node, extraOpts ...server.ServerOption) error {
	if n == nil {
		return &ConfigError{Field: "node", Message: "must not be nil"}
	}
	if !n.IsSealed() {
		return nil
	}
	cfg := n.sealedWindowConfig()
	if cfg == nil {
		return &ConfigError{Field: "node", Message: "sealed node has no pending boot configuration"}
	}

	return server.ServeSealedWindow(ctx, cfg, n.barrier.Load(), n.sealedWindowOptions(extraOpts)...)
}

// sealedWindowConfig returns the *qrdbconfig.Config captured by the
// pending (sealed) boot, or nil when the node has no pending boot.
func (n *Node) sealedWindowConfig() *qrdbconfig.Config {
	if n.pendingStandalone != nil {
		return n.pendingStandalone.cfg
	}
	if n.pendingClustered != nil {
		return n.pendingClustered.cfg
	}
	return nil
}

// sealedWindowOptions assembles the sealed-window listener's options: the
// node's own seal-service instances, its barrier registry and PIN backend —
// what the daemon's sealed window serves — then the caller's extraOpts.
// Without the PIN backend every SO-PIN-gated endpoint on the listener would
// fail closed.
func (n *Node) sealedWindowOptions(extraOpts []server.ServerOption) []server.ServerOption {
	var opts []server.ServerOption
	if svcNode := n.svcNode.Load(); svcNode != nil {
		if svcNode.Barrier != nil {
			opts = append(opts, server.WithBarrierService(svcNode.Barrier))
		}
		if svcNode.Ceremony != nil {
			opts = append(opts, server.WithCeremonyService(svcNode.Ceremony))
		}
		if svcNode.Credential != nil {
			opts = append(opts, server.WithCredentialService(svcNode.Credential))
		}
	}
	if barrierRegistry := n.barrierRegistry.Load(); barrierRegistry != nil {
		opts = append(opts, server.WithBarrierRegistry(barrierRegistry))
	}
	if backend := n.pinBackend(extraOpts); backend != nil {
		opts = append(opts, server.WithPINBackend(backend))
	}
	return append(opts, extraOpts...)
}
