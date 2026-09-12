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

package conformance

import (
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// The cert-binding WITHDRAWAL conformance matrix (go-qrdb#518).
//
// AddCertBinding shipped on all six transports; nothing removed a binding on
// any of them, so a binding written today could not be withdrawn through any
// API. Since the binding set became what decides an account's mTLS admission,
// that made an enrolled key an irrevocable credential rather than a stale
// record.
//
// This suite is what makes a partial repair impossible to ship. A withdrawal
// reachable on four transports and missing on two is a surface that disagrees
// with itself, and the disagreement is invisible from inside any one of them:
// the operator who needs it is the one holding whichever transport was
// forgotten.
//
// Each cell drives the WHOLE cycle over ONE transport — enrol two keys,
// withdraw one, observe that the withdrawn key resolves nothing and the
// retained key still resolves the account — because the assertion that matters
// is the state afterwards, not that a call returned without an error.

// withdrawnThumbprint and retainedThumbprint are two distinct canonical SPKI
// thumbprints (base64url of 32 bytes). They are literals rather than computed
// from a minted certificate because nothing here presents a certificate: the
// axis under test is whether the VERB crosses each transport, and the
// authentication consequence of a withdrawal is asserted against the real
// authenticator in pkg/auth/middleware.
const (
	withdrawnThumbprint = "BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB"
	retainedThumbprint  = "CCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCC"
)

// newBindingTarget creates a user to enrol and withdraw bindings on.
//
// It must NOT be the acting identity. Since PR #516 an account's first binding
// CLOSES it: only a certificate matching one of its bindings authenticates as
// it. Enrolling a synthetic thumbprint on the user the test's own certificate
// authenticates as therefore locks the test out of the server on the very next
// call — which is what the first run of this suite did, on all five remote
// transports at once. The subject of the withdrawal is a bystander account,
// exactly as it is in an operator's use of the verb.
func newBindingTarget(t *testing.T, c userClients, name string) uint64 {
	t.Helper()
	u, err := c.users.CreateUser(boundedCtx(t), &transport.CreateUserRequest{
		Username:             name,
		TenantID:             tenant.SystemTenantID,
		WithoutEmailLoginKey: true,
	})
	if err != nil {
		t.Fatalf("CreateUser(%s): %v", name, err)
	}
	return u.ID
}

// TestCertBindingWithdrawal_ReachableOnEveryTransport walks the same transport
// axis the not-found matrix walks and asserts the withdrawal verb works on
// every one of them.
func TestCertBindingWithdrawal_ReachableOnEveryTransport(t *testing.T) {
	for _, tk := range userNotFoundTransports {
		t.Run(string(tk), func(t *testing.T) {
			c := bootUserCell(t, tk)
			ctx := boundedCtx(t)
			target := newBindingTarget(t, c, "reachable")

			for _, tp := range []string{withdrawnThumbprint, retainedThumbprint} {
				if _, err := c.users.AddCertBinding(ctx, target, tp, 0); err != nil {
					t.Fatalf("%s: AddCertBinding(%s): %v", tk, tp, err)
				}
			}

			// Both resolve before the withdrawal, so the assertion afterwards
			// is about the withdrawal and not about enrolment never having
			// worked on this transport.
			for _, tp := range []string{withdrawnThumbprint, retainedThumbprint} {
				if _, err := c.users.GetUserByCertThumbprint(ctx, tp); err != nil {
					t.Fatalf("%s: %s did not resolve before the withdrawal: %v", tk, tp, err)
				}
			}

			got, err := c.users.RemoveCertBinding(ctx, target, withdrawnThumbprint)
			if err != nil {
				t.Fatalf("%s: RemoveCertBinding: %v", tk, err)
			}
			if got == nil {
				t.Fatalf("%s: RemoveCertBinding returned no user", tk)
			}
			if got.ID != target {
				t.Fatalf("%s: returned user %d, want %d", tk, got.ID, target)
			}

			// The withdrawn key resolves nothing.
			if _, err := c.users.GetUserByCertThumbprint(ctx, withdrawnThumbprint); err == nil {
				t.Fatalf("%s: the withdrawn thumbprint still resolves a user", tk)
			}

			// The retained key is untouched — a withdrawal is not a lockout.
			retained, err := c.users.GetUserByCertThumbprint(ctx, retainedThumbprint)
			if err != nil {
				t.Fatalf("%s: the retained binding stopped resolving: %v", tk, err)
			}
			if retained.ID != target {
				t.Fatalf("%s: the retained binding resolves user %d, want %d", tk, retained.ID, target)
			}
		})
	}
}

// TestCertBindingWithdrawal_UnheldBindingIsNotFoundOnEveryTransport pins the
// second half of the contract: a withdrawal naming a binding the account does
// NOT hold reports a not-found rather than succeeding vacuously.
//
// That refusal is the security-relevant one. An operator withdrawing a key
// they believe is compromised, who mistypes the thumbprint, must not be told
// the withdrawal succeeded while the real binding stays live — and they must
// be told so on whichever transport they happen to be holding.
func TestCertBindingWithdrawal_UnheldBindingIsNotFoundOnEveryTransport(t *testing.T) {
	for _, tk := range userNotFoundTransports {
		t.Run(string(tk), func(t *testing.T) {
			c := bootUserCell(t, tk)
			ctx := boundedCtx(t)
			target := newBindingTarget(t, c, "unheld")

			if _, err := c.users.AddCertBinding(ctx, target, retainedThumbprint, 0); err != nil {
				t.Fatalf("%s: AddCertBinding: %v", tk, err)
			}

			_, err := c.users.RemoveCertBinding(ctx, target, withdrawnThumbprint)
			if err == nil {
				t.Fatalf("%s: withdrawing a binding the account does not hold reported success", tk)
			}

			// The account's real binding must survive the refused withdrawal.
			if _, gerr := c.users.GetUserByCertThumbprint(ctx, retainedThumbprint); gerr != nil {
				t.Fatalf("%s: a refused withdrawal disturbed the held binding: %v", tk, gerr)
			}
		})
	}
}

// TestCertBindingWithdrawal_LastBindingEmptiesTheSet asserts the posture
// change deliberately, on the transport surface, because it is the one
// consequence of this verb an operator can be surprised by.
//
// An account with NO bindings states no restriction: it is admitted on the
// certificate's CN and OU alone. So withdrawing the LAST binding does not lock
// the account down, it reopens it.
//
// Emptiness is observed through the verb's own refusal rather than by reading
// the set, because transport.User does not carry CertBindings on any transport
// — a second withdrawal of the same thumbprint must report not-found, which it
// can only do if the first one removed it. What the AUTHENTICATOR then does
// with an empty set is asserted against the real authenticator and the real
// store in pkg/auth/middleware.
func TestCertBindingWithdrawal_LastBindingEmptiesTheSet(t *testing.T) {
	for _, tk := range userNotFoundTransports {
		t.Run(string(tk), func(t *testing.T) {
			c := bootUserCell(t, tk)
			ctx := boundedCtx(t)
			target := newBindingTarget(t, c, "lastbinding")

			if _, err := c.users.AddCertBinding(ctx, target, withdrawnThumbprint, 0); err != nil {
				t.Fatalf("%s: AddCertBinding: %v", tk, err)
			}
			if _, err := c.users.RemoveCertBinding(ctx, target, withdrawnThumbprint); err != nil {
				t.Fatalf("%s: RemoveCertBinding: %v", tk, err)
			}

			// The account is once again unresolvable by thumbprint, which is
			// the shape of "no restriction" on this surface.
			_, err := c.users.GetUserByCertThumbprint(ctx, withdrawnThumbprint)
			var unf *transport.UserNotFoundError
			if err == nil || !errors.As(err, &unf) {
				t.Fatalf("%s: after the last withdrawal the thumbprint lookup = %v, want a typed not-found", tk, err)
			}

			// And the set really is empty: withdrawing the same thumbprint
			// again finds nothing to withdraw.
			if _, err := c.users.RemoveCertBinding(ctx, target, withdrawnThumbprint); err == nil {
				t.Fatalf("%s: a second withdrawal of the same thumbprint reported success", tk)
			}
		})
	}
}
