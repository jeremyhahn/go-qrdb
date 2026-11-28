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
	"errors"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
	"github.com/jeremyhahn/go-qrdb/pkg/user"
)

// TestOperatorIdentityAccess_TenantQualifiedPermissionIsAttestationGated
// proves a peer-attestation policy that gates only the tenant-qualified
// permission is applied to a cross-tenant identity write: naming another
// tenant must not pass the gate authorize applies to every permission.
func TestOperatorIdentityAccess_TenantQualifiedPermissionIsAttestationGated(t *testing.T) {
	for _, perm := range []permission.Permission{permission.UsersWriteTenant, permission.RolesWriteTenant} {
		for _, attested := range []bool{false, true} {
			svc := newRealUserStore(t)
			u := createUser(t, svc, tenantid.System, "op", []string{"operator"})
			cert := mintCert(t, "op", tenantid.System, nil, "")
			enroll(t, svc, u.ID, cert)

			now := time.Now().UTC()
			v := &cacheVerifier{evidence: map[string]cachedEvidence{}}
			gate, err := NewPeerAttestationGate(v, PeerAttestationPolicy{
				Permissions:    []permission.Permission{perm},
				MaxEvidenceAge: 5 * time.Minute,
			})
			if err != nil {
				t.Fatalf("NewPeerAttestationGate: %v", err)
			}
			gate.now = func() time.Time { return now }

			sink := &mockAuditLogger{}
			m := newTestMiddleware(&mockAuthorizer{},
				WithAuthenticator(auth.NewMTLSAuthenticator()),
				WithUserService(svc),
				withOperatorTrails(sink),
				WithPeerAttestationGate(gate),
				WithIsolationPolicyReader(policyFixture(tenant.ClusterIsolationPolicyOperatorAccess, tenant.TenantIsolationPolicyInherit)),
			)
			ctx, identity, err := m.authenticate(mtlsContext(cert))
			if err != nil {
				t.Fatalf("authenticate: %v", err)
			}
			if attested {
				v.evidence[identityKey(identity.TenantID, identity.Subject)] = cachedEvidence{
					peerThumbprint: user.ComputeSPKIThumbprint(cert.cert),
					collectedAt:    now.Add(-time.Minute),
					passed:         true,
				}
			}

			_, err = m.AuthorizeOperatorIdentityAccess(ctx, "acme", perm)
			if attested {
				if err != nil {
					t.Fatalf("%s: attested operator refused: %v", perm, err)
				}
			} else {
				var refusal *PeerAttestationRefusedError
				if !errors.As(err, &refusal) || refusal.Reason != PeerAttestationNoEvidence || refusal.Permission != perm ||
					refusal.Subject != identity.Subject || refusal.TenantID != identity.TenantID {
					t.Fatalf("%s: unattested operator = %v, want a %s peer-attestation refusal", perm, err, PeerAttestationNoEvidence)
				}
			}
			if v.callCount() != 1 {
				t.Fatalf("%s attested=%v: verifier calls = %d, want 1", perm, attested, v.callCount())
			}
		}
	}
}
