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

package embedded

import (
	"github.com/jeremyhahn/go-qrdb/pkg/api/sealedwindow"
	authmw "github.com/jeremyhahn/go-qrdb/pkg/auth/middleware"
	"github.com/jeremyhahn/go-qrdb/pkg/clusterseal"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
)

// SealProbe reports whether the node must refuse every request outside the
// sealed-window allowlist. It is the probe the node's regular network
// listener gates on (server.NodeSealProbe): closed while the node barrier is
// sealed, and while a cluster seal holds the node, cuts it off, or is not yet
// confirmed.
type SealProbe interface {
	IsSealed() bool
}

// WithSealProbe gates this transport, and every database-scoped transport
// derived from it, on p: while p reports sealed, every request outside the
// canonical sealed-window allowlist (sealedwindow.EmbeddedAllowlist) is
// refused with *BarrierSealedError, exactly as the network transports refuse
// it. Without a probe the transport gates nothing.
func WithSealProbe(p SealProbe) Option {
	return func(t *Transport) { t.sealProbe = p }
}

// AwaitingAdminCredentialProbe reports whether the node is currently
// "awaiting admin credential" (go-qrdb#548 review item 3): auth.mtls_binding_policy
// is "required" and the bootstrap admin holds no live certificate binding.
type AwaitingAdminCredentialProbe interface {
	AwaitingAdminCredential() bool
}

// WithAwaitingAdminCredentialProbe gates this transport, and every
// database-scoped transport derived from it, on p: while p reports
// awaiting, every request outside awaitingAdminCredentialAllowlist
// (ceremony status and ceremony-initialize) is refused with
// *middleware.AwaitingAdminCredentialError. Without a probe the transport
// gates nothing on this axis.
func WithAwaitingAdminCredentialProbe(p AwaitingAdminCredentialProbe) Option {
	return func(t *Transport) { t.awaitingAdminCredentialProbe = p }
}

// WithSealStreams subscribes this transport's own watches to the node's
// cluster-seal stream registry (clusterseal.Deferred.Streams): a seal or a
// cut-off that reaches the watch's barrier ends it, with the seal's refusal
// as the terminal frame, exactly as it ends a network listener's watch. An
// unscoped watch reads the caller's tenant's default database, and subscribes
// as that. A database-scoped transport's watches resolve through the
// service registry, which guards them itself. Without it a watch the gate
// admitted before a seal outlives the seal.
func WithSealStreams(s *clusterseal.Streams) Option {
	return func(t *Transport) { t.sealStreams = s }
}

// ungatedLifecycleMethods are the request-shaped methods that are not
// requests: connecting and closing the transport -- a network client
// completes its handshake with a sealed node too -- and deriving a
// database-scoped transport whose every request is gated in turn.
var ungatedLifecycleMethods = map[string]struct{}{
	"Connect":      {},
	"Close":        {},
	"WithDatabase": {},
}

// embeddedAllowlist is the sealed-window allowlist for this transport,
// derived once from the canonical table every transport derives from.
var embeddedAllowlist = sealedwindow.EmbeddedAllowlist()

// awaitingAdminCredentialAllowlist is the awaiting-admin-credential
// allowlist for this transport (ceremony status and ceremony-initialize),
// derived from the same canonical table.
var awaitingAdminCredentialAllowlist = sealedwindow.EmbeddedAwaitingAdminCredential()

// gate refuses method while the seal probe is closed, unless the method is
// on the sealed-window allowlist, and separately refuses it while the node
// is awaiting admin credential (go-qrdb#548 review item 3) unless the
// method is one of the two that state still serves. It runs first in every
// request method, so a refused request reads none of its arguments. The
// sealed-window decision is the same one the network listeners make
// (sealedwindow.Decide): the embedded transport is always the regular,
// authenticated side of the gate.
func (t *Transport) gate(method string) error {
	if t.awaitingAdminCredentialProbe != nil && t.awaitingAdminCredentialProbe.AwaitingAdminCredential() {
		if _, ok := awaitingAdminCredentialAllowlist[method]; !ok {
			return &authmw.AwaitingAdminCredentialError{}
		}
	}
	if t.sealProbe == nil {
		return nil
	}
	_, allowlisted := embeddedAllowlist[method]
	sealed, unconfirmed := sealedwindow.Sample(t.sealProbe)
	decision := sealedwindow.Decide(sealedwindow.Request{Sealed: sealed, Unconfirmed: unconfirmed, Allowlisted: allowlisted})
	if decision == sealedwindow.Serve {
		return nil
	}
	if refuse, ok := embeddedRefusals[decision]; ok {
		return refuse()
	}
	return &BarrierSealedError{Err: seal.ErrSealed}
}

// embeddedRefusals names the refusals that are not a sealed barrier. Any
// other refusing decision fails closed as BarrierSealedError.
var embeddedRefusals = map[sealedwindow.Decision]func() error{
	sealedwindow.RefuseUnconfirmed: func() error { return &clusterseal.SealStateUnconfirmedError{} },
}
