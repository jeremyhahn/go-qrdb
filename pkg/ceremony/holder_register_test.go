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

package ceremony

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/auth/spiffe"
	"github.com/jeremyhahn/go-qrdb/pkg/authfactors"
	"github.com/jeremyhahn/go-qrdb/pkg/authfactors/mtls"
	"github.com/jeremyhahn/go-qrdb/pkg/keydelivery"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
)

// TestRegisterOfficer_CAAssignsTheIdentity pins the §6 fix: the CSR asks to
// be "admin" in the system security-officer profile, and the certificate the
// CA issues names the holder it was registered as, in the holder SPIFFE
// namespace, and nothing the CSR asked for.
func TestRegisterOfficer_CAAssignsTheIdentity(t *testing.T) {
	e := newHolderEnv(t, 2, 2)
	h := newHolder(t, "carol", seal.RoleCustodian)
	res, err := e.svc.RegisterOfficer(context.Background(), seal.DomainUnseal, h.request("admin"))
	require.NoError(t, err)

	cert, err := mtls.ParseCertificatePEM(res.CertPEM)
	require.NoError(t, err)
	assert.Equal(t, "carol", cert.Subject.CommonName)
	assert.Equal(t, []string{tenantid.System, "custodian"}, cert.Subject.OrganizationalUnit)
	require.Len(t, cert.URIs, 1)
	id, ok, err := spiffe.ParseID(cert.URIs[0])
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, spiffe.ID{TrustDomain: testTrustDomain, TenantID: tenantid.System, Kind: spiffe.KindHolder, Name: "carol"}, id)
	assert.Equal(t, seal.RoleCustodian, res.Role)
	assert.Equal(t, mtls.Name, res.Designated)
	assert.Equal(t, keydelivery.KEMP256, res.EncryptionKEM)
	assert.Equal(t, []string{mtls.Name}, res.Factors)
}

// TestRegisterOfficer_DefaultsToSecurityOfficer pins the default role.
func TestRegisterOfficer_DefaultsToSecurityOfficer(t *testing.T) {
	e := newHolderEnv(t, 2, 2)
	h := newHolder(t, "alice", "")
	res, err := e.svc.RegisterOfficer(context.Background(), seal.DomainUnseal, h.request("admin"))
	require.NoError(t, err)
	assert.Equal(t, seal.RoleSecurityOfficer, res.Role)
	cert, err := mtls.ParseCertificatePEM(res.CertPEM)
	require.NoError(t, err)
	assert.Equal(t, []string{tenantid.System, "security-officer"}, cert.Subject.OrganizationalUnit)
}

// TestRegisterOfficer_Refusals covers every validation refusal.
func TestRegisterOfficer_Refusals(t *testing.T) {
	ctx := context.Background()
	e := newHolderEnv(t, 2, 2)
	alice := newHolder(t, "alice", seal.RoleSecurityOfficer)
	e.register(seal.DomainUnseal, alice)

	cases := map[string]struct {
		req  RegisterOfficerRequest
		want func(error) bool
	}{
		"self enrolment": {
			req:  newHolder(t, "bob", seal.RoleSecurityOfficer).request("bob"),
			want: func(err error) bool { return assert.ErrorIs(t, err, ErrSelfEnrolment) },
		},
		"invalid role": {
			req: func() RegisterOfficerRequest {
				r := newHolder(t, "bob", "").request("admin")
				r.Role = "root"
				return r
			}(),
			want: func(err error) bool { return assert.ErrorIs(t, err, ErrInvalidHolderRole) },
		},
		"missing username": {
			req:  newHolder(t, "", seal.RoleSecurityOfficer).request("admin"),
			want: func(err error) bool { return assert.ErrorIs(t, err, ErrMissingUsername) },
		},
		"invalid username": {
			req:  newHolder(t, "bob/../root", seal.RoleSecurityOfficer).request("admin"),
			want: func(err error) bool { return assert.True(t, errorAs[*InvalidHolderNameError](err), err) },
		},
		"missing CSR": {
			req:  RegisterOfficerRequest{Username: "bob", Enroller: "admin"},
			want: func(err error) bool { return assert.ErrorIs(t, err, ErrMissingCSRPEM) },
		},
		"malformed CSR": {
			req:  RegisterOfficerRequest{Username: "bob", CSRPEM: []byte("not a csr"), Enroller: "admin"},
			want: func(err error) bool { return assert.True(t, errorAs[*CSRParseError](err), err) },
		},
		"duplicate username": {
			req:  newHolder(t, "alice", seal.RoleCustodian).request("admin"),
			want: func(err error) bool { return assert.ErrorIs(t, err, ErrDuplicateUsername) },
		},
		"same key, second identity": {
			req:  newHolderWithKey(t, "alice2", seal.RoleCustodian, alice.certKey).request("admin"),
			want: func(err error) bool { return assert.ErrorIs(t, err, ErrDuplicateHolderKey) },
		},
		"Ed25519 key without an envelope key": {
			req:  newEd25519Holder(t, "ed", seal.RoleSecurityOfficer).request("admin"),
			want: func(err error) bool { return assert.True(t, errorAs[*EncryptionKeyRequiredError](err), err) },
		},
		"unknown KEM": {
			req: func() RegisterOfficerRequest {
				r := newHolder(t, "bob", "").request("admin")
				r.EncryptionKey = &EncryptionKeyEnrolment{KEM: "rsa-oaep", PublicKey: []byte{1}}
				return r
			}(),
			want: func(err error) bool { return assert.True(t, errorAs[*keydelivery.UnsupportedKEMError](err), err) },
		},
		"malformed envelope key": {
			req: func() RegisterOfficerRequest {
				r := newHolder(t, "bob", "").request("admin")
				r.EncryptionKey = &EncryptionKeyEnrolment{KEM: keydelivery.KEMX25519, PublicKey: []byte{1, 2, 3}}
				return r
			}(),
			want: func(err error) bool { return assert.Error(t, err) },
		},
		"unknown factor": {
			req: func() RegisterOfficerRequest {
				r := newHolder(t, "bob", "").request("admin")
				r.Factors = []FactorEnrolment{{Name: "piv"}}
				return r
			}(),
			want: func(err error) bool { return assert.True(t, errorAs[*authfactors.FactorNotFoundError](err), err) },
		},
		"duplicate factor": {
			req: func() RegisterOfficerRequest {
				r := newHolder(t, "bob", "").request("admin")
				r.Factors = []FactorEnrolment{{Name: mtls.Name}}
				return r
			}(),
			want: func(err error) bool { return assert.True(t, errorAs[*DuplicateFactorError](err), err) },
		},
		"factor enrolment fails": {
			req: func() RegisterOfficerRequest {
				r := newHolder(t, "bob", "").request("admin")
				r.Factors = []FactorEnrolment{{Name: testFactorName, Params: map[string]any{"public_key": "short"}}}
				return r
			}(),
			want: func(err error) bool { return assert.True(t, errorAs[*HolderFactorEnrollmentError](err), err) },
		},
		"designated factor not enrolled": {
			req: func() RegisterOfficerRequest {
				r := newHolder(t, "bob", "").request("admin")
				r.Designated = testFactorName
				return r
			}(),
			want: func(err error) bool { return assert.True(t, errorAs[*FactorNotEnrolledError](err), err) },
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := e.svc.RegisterOfficer(ctx, seal.DomainUnseal, tc.req)
			require.Error(t, err)
			tc.want(err)
		})
	}
}

// TestRegisterOfficer_OnePersonOneRolePerScope pins that an envelope key
// already enrolled in the scope cannot be enrolled again under a new name.
func TestRegisterOfficer_OnePersonOneRolePerScope(t *testing.T) {
	e := newHolderEnv(t, 2, 2)
	a := newHolder(t, "alice", seal.RoleSecurityOfficer).withEncryptionKey(t, keydelivery.KEMX25519)
	e.register(seal.DomainUnseal, a)

	b := newHolder(t, "bob", seal.RoleCustodian)
	b.encKEM, b.encPub = a.encKEM, a.encPub
	_, err := e.svc.RegisterOfficer(context.Background(), seal.DomainUnseal, b.request("admin"))
	assert.ErrorIs(t, err, ErrDuplicateHolderKey)
}

// TestRegisterOfficer_RequiresCAAndTrustDomain pins the fail-closed
// dependencies.
func TestRegisterOfficer_RequiresCAAndTrustDomain(t *testing.T) {
	e := newHolderEnv(t, 2, 2)
	noCA, err := NewCeremonyService(&CeremonyConfig{DataDir: t.TempDir()}, shamirBarrier(t, seal.NewMemoryBackend(), 2, 2), testLogger())
	require.NoError(t, err)
	_, err = noCA.RegisterOfficer(context.Background(), seal.DomainUnseal, newHolder(t, "a", "").request("admin"))
	assert.ErrorIs(t, err, ErrCANotInitialized)

	noCA.SetCA(e.ca)
	_, err = noCA.RegisterOfficer(context.Background(), seal.DomainUnseal, newHolder(t, "a", "").request("admin"))
	assert.ErrorIs(t, err, ErrTrustDomainNotConfigured)
}

// TestRegisterOfficer_EnvelopeKEMs pins the KEM each key type enrols with.
func TestRegisterOfficer_EnvelopeKEMs(t *testing.T) {
	e := newHolderEnv(t, 2, 4)
	cases := []struct {
		holder *testHolder
		kem    string
	}{
		{newHolder(t, "p256", ""), keydelivery.KEMP256},
		{newP384Holder(t, "p384", ""), keydelivery.KEMP384},
		{newEd25519Holder(t, "x25519", "").withEncryptionKey(t, keydelivery.KEMX25519), keydelivery.KEMX25519},
		{newHolder(t, "hybrid", "").withEncryptionKey(t, keydelivery.KEMMLKEM768X25519), keydelivery.KEMMLKEM768X25519},
	}
	for _, tc := range cases {
		res, err := e.svc.RegisterOfficer(context.Background(), seal.DomainUnseal, tc.holder.request("admin"))
		require.NoError(t, err, tc.holder.username)
		assert.Equal(t, tc.kem, res.EncryptionKEM, tc.holder.username)
	}
}

// TestRegisterOfficer_AdditionalFactor enrols the assertion factor and
// designates it.
func TestRegisterOfficer_AdditionalFactor(t *testing.T) {
	e := newHolderEnv(t, 2, 2)
	h := newHolder(t, "dave", seal.RoleCustodian).withAuthenticator(t)
	req := h.request("admin")
	req.Designated = testFactorName
	res, err := e.svc.RegisterOfficer(context.Background(), seal.DomainUnseal, req)
	require.NoError(t, err)
	assert.Equal(t, testFactorName, res.Designated)
	assert.Equal(t, []string{mtls.Name, testFactorName}, res.Factors)

	list, err := e.svc.ListOfficers(context.Background(), seal.DomainUnseal)
	require.NoError(t, err)
	require.Len(t, list, 1)
	assert.Equal(t, "dave", list[0].Username)
	assert.Equal(t, testFactorName, list[0].Designated)
	assert.False(t, list[0].SharePending)
}

// TestRegisterOfficer_UnknownDomain refuses a domain the service has no
// scope for.
func TestRegisterOfficer_UnknownDomain(t *testing.T) {
	e := newHolderEnv(t, 2, 2)
	_, err := e.svc.RegisterOfficer(context.Background(), "bogus", newHolder(t, "a", "").request("admin"))
	assert.True(t, errorAs[*seal.QuorumOpInvalidError](err), err)
	_, err = e.svc.ListOfficers(context.Background(), "bogus")
	assert.Error(t, err)
}
