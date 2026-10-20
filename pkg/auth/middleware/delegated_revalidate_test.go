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

package middleware

import (
	"context"
	"crypto/x509"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/authtest"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// dlgRevocable answers whatever its switch says, so a test can revoke a
// delegation after the middleware admitted it.
type dlgRevocable struct{ revoked *atomic.Bool }

func (r dlgRevocable) IsCertRevoked(context.Context, *x509.Certificate) (bool, error) {
	return r.revoked.Load(), nil
}

// TestRevalidateDelegation_RevokedAfterAdmissionIsRefusedAtEveryDecision: a
// delegated identity admitted once is verified again at each decision, so a
// revocation landing after admission refuses the next one with
// auth.ErrCertificateRevoked -- through authorize, AuthorizeTenantTarget and
// FilterTenantList alike, and audited where the decision is audited.
func TestRevalidateDelegation_RevokedAfterAdmissionIsRefusedAtEveryDecision(t *testing.T) {
	d := authtest.NewDelegation(t, dlgGrant)
	for name, decide := range map[string]func(*AuthMiddleware, context.Context, *auth.Identity) error{
		"authorize": func(m *AuthMiddleware, ctx context.Context, id *auth.Identity) error {
			return m.authorize(ctx, id, permission.KVGet)
		},
		"AuthorizeTenantTarget": func(m *AuthMiddleware, ctx context.Context, _ *auth.Identity) error {
			return m.AuthorizeTenantTarget(ctx, "acme", permission.KVGet)
		},
		"FilterTenantList": func(m *AuthMiddleware, ctx context.Context, _ *auth.Identity) error {
			_, err := m.FilterTenantList(ctx, []tenant.TenantInfo{{ID: "acme"}})
			return err
		},
	} {
		t.Run(name, func(t *testing.T) {
			var revoked atomic.Bool
			sink := &mockAuditLogger{}
			m := dlgMiddleware(t, &mockAuthenticator{name: "mtls", err: auth.ErrNoPeerCertificate},
				WithAuditLogger(sink), WithDelegationVerifier(d.NewAuthenticator(auth.WithRevocationChecker(dlgRevocable{&revoked}))))
			ctx, id, err := m.AuthenticateDelegation(context.Background(), []*x509.Certificate{d.Certificate})
			if err != nil {
				t.Fatalf("AuthenticateDelegation: %v", err)
			}
			if err := decide(m, ctx, id); err != nil {
				t.Fatalf("before the revocation = %v; want allowed", err)
			}
			revoked.Store(true)
			before := len(sink.entries)
			if err := decide(m, ctx, id); !errors.Is(err, auth.ErrCertificateRevoked) {
				t.Fatalf("after the revocation = %v; want auth.ErrCertificateRevoked", err)
			}
			if name != "FilterTenantList" && len(sink.entries) != before+1 {
				t.Fatalf("%d audit records for the refusal; want one", len(sink.entries)-before)
			}
		})
	}
}

// TestRevalidateDelegation_NoVerifierRefusesADelegatedIdentity: a node that
// accepts no delegations cannot vouch for one bound to the request by
// anything else, so the decision is refused rather than served unverified;
// an identity that carries no delegation is decided as before.
func TestRevalidateDelegation_NoVerifierRefusesADelegatedIdentity(t *testing.T) {
	d := authtest.NewDelegation(t, dlgGrant)
	m := dlgMiddleware(t, &mockAuthenticator{name: "mtls", err: auth.ErrNoPeerCertificate}, WithAuditLogger(&mockAuditLogger{}))

	err := m.revalidateDelegation(context.Background(), d.Identity(t))
	var refused *auth.DelegationRefusedError
	if !errors.As(err, &refused) || !errors.Is(err, auth.ErrForeignDelegationPrincipal) {
		t.Fatalf("revalidateDelegation without a verifier = %v; want the typed refusal", err)
	}
	if err := m.revalidateDelegation(context.Background(), &auth.Identity{Subject: "alice", TenantID: "acme"}); err != nil {
		t.Fatalf("revalidateDelegation of an undelegated identity = %v; want nil", err)
	}
}
