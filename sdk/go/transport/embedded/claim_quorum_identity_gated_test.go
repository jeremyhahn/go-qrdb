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
	"context"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/authtest"
	authmw "github.com/jeremyhahn/go-qrdb/pkg/auth/middleware"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/rbac"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// TestShareClaimAndQuorum_RequireAnAuthenticatedCaller is the owner security
// review's item 6: the share-claim and quorum handshakes are IdentityGated
// on the embedded transport exactly as on the network -- an authenticated
// caller is required -- even though neither carries an RBAC PERMISSION of
// its own (the holder's binding authorization is the certificate or factor
// the handshake itself verifies, not a role). With a real AuthMiddleware
// installed, a context carrying no identity at all is refused; one carrying
// even a minimal identity (no RBAC role, since IdentityGated needs none) is
// admitted and reaches the service layer. Mutation-checked below by
// deleting the requireIdentity call and confirming red.
func TestShareClaimAndQuorum_RequireAnAuthenticatedCaller(t *testing.T) {
	barrier := &recordingClaimBarrier{}
	roles := authtest.BuiltInRoles(t, tenantid.System)
	mw := authmw.New(rbac.New(roles))
	tr := newBarrierTransport(t, barrier)
	tr.authMW = mw

	noIdentity := context.Background()
	holder := auth.WithIdentity(context.Background(), &auth.Identity{Subject: "cust-carol"})

	// ShareClaimBegin/Complete are checked end to end: recordingClaimBarrier
	// implements both, so a minimal identity must reach the service (no
	// error at all). BarrierQuorumBegin/Submit are checked only for the
	// refusal, which runs before the (unimplemented-on-this-mock) service
	// call: TestShareHolders_ConformAcrossEverySDKTransport
	// (sdk/go/conformance) already proves the admitted case end to end
	// against the real barrier service.
	t.Run("ShareClaimBegin", func(t *testing.T) {
		if _, err := tr.ShareClaimBegin(noIdentity, &transport.ShareClaimBeginRequest{Username: "cust-carol"}); !errors.Is(err, auth.ErrUnauthenticated) {
			t.Fatalf("no identity: err = %v, want auth.ErrUnauthenticated", err)
		}
		if _, err := tr.ShareClaimBegin(holder, &transport.ShareClaimBeginRequest{Username: "cust-carol"}); err != nil {
			t.Fatalf("a minimal authenticated identity (no RBAC role): %v, want admission", err)
		}
	})
	t.Run("ShareClaimComplete", func(t *testing.T) {
		if _, err := tr.ShareClaimComplete(noIdentity, &transport.ShareClaimCompleteRequest{Username: "cust-carol"}); !errors.Is(err, auth.ErrUnauthenticated) {
			t.Fatalf("no identity: err = %v, want auth.ErrUnauthenticated", err)
		}
		if _, err := tr.ShareClaimComplete(holder, &transport.ShareClaimCompleteRequest{Username: "cust-carol"}); err != nil {
			t.Fatalf("a minimal authenticated identity (no RBAC role): %v, want admission", err)
		}
	})
	t.Run("BarrierQuorumBegin", func(t *testing.T) {
		if _, err := tr.BarrierQuorumBegin(noIdentity, &transport.QuorumBeginRequest{Username: "cust-carol"}); !errors.Is(err, auth.ErrUnauthenticated) {
			t.Fatalf("no identity: err = %v, want auth.ErrUnauthenticated", err)
		}
	})
	t.Run("BarrierQuorumSubmit", func(t *testing.T) {
		if _, err := tr.BarrierQuorumSubmit(noIdentity, &transport.QuorumSubmitRequest{Username: "cust-carol"}); !errors.Is(err, auth.ErrUnauthenticated) {
			t.Fatalf("no identity: err = %v, want auth.ErrUnauthenticated", err)
		}
	})
}
