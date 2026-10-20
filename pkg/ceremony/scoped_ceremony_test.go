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
	"encoding/hex"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/authfactors/mtls"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
)

// TestTenantCeremony_HolderLifecycle drives a tenant barrier's holders
// through enrolment, distribution, claim and a quorum seal and unseal.
func TestTenantCeremony_HolderLifecycle(t *testing.T) {
	ctx := context.Background()
	backend := seal.NewMemoryBackend()
	barrier := shamirBarrier(t, backend, 2, 2)
	svc, err := NewTenantCeremonyService("acme", backend, HolderDeps{TrustDomain: testTrustDomain, CA: testCA(t), Factors: testFactors(t)}, testLogger())
	require.NoError(t, err)
	barrier.SetCeremonyDistributor(svc)
	barrier.SetHolderVerifier(svc)

	a := newHolder(t, "alice", "")
	b := newHolder(t, "bob", seal.RoleCustodian).withAuthenticator(t)
	for _, h := range []*testHolder{a, b} {
		res, err := svc.RegisterOfficer(ctx, h.request("tenant-admin"))
		require.NoError(t, err)
		h.recordCert(t, res.CertPEM)
		cert, err := mtls.ParseCertificatePEM(res.CertPEM)
		require.NoError(t, err)
		assert.Equal(t, "acme", cert.Subject.OrganizationalUnit[0], "a tenant holder's identity is in its tenant")
		_, isHolder := auth.HolderProfile(cert)
		assert.True(t, isHolder, "a tenant holder's certificate is a holder certificate")
		assert.Equal(t, []string{"spiffe://acme/user/" + h.username}, uriStrings(cert),
			"a holder certificate's one SAN is its agreeing tenant SPIFFE ID")
	}
	_, err = barrier.InitializeShamir(ctx, seal.FactorPolicyAnyEnrolled)
	require.NoError(t, err)

	list, err := svc.ListOfficers(ctx)
	require.NoError(t, err)
	require.Len(t, list, 2)

	for _, h := range []*testHolder{a, b} {
		ch, err := svc.BeginClaimShare(ctx, h.username, h.thumb)
		require.NoError(t, err)
		sig, err := SignShareClaim(h.certKey, ScopeTenant("acme").ClaimLabel(), h.username, ch.Nonce)
		require.NoError(t, err)
		res, err := svc.CompleteClaimShare(ctx, h.username, hex.EncodeToString(ch.Nonce), sig, h.thumb)
		require.NoError(t, err)
		h.envelope = res.Envelope
	}

	submit := func(op seal.QuorumOp, h *testHolder, factor string) (*seal.QuorumOutcome, error) {
		ch, err := svc.BeginQuorum(ctx, barrier, op, h.username, factor)
		if err != nil {
			return nil, err
		}
		return barrier.SubmitHolderShare(ctx, &seal.HolderSubmission{
			Domain: seal.DomainUnseal, Op: op, Holder: h.username,
			Proof: seal.HolderProof{ChallengeID: ch.ChallengeID, Factor: ch.Factor, Response: h.answer(t, ch)},
			Share: h.openShare(t),
		})
	}
	_, err = submit(seal.OpSeal, a, "")
	require.NoError(t, err)
	out, err := submit(seal.OpSeal, b, testFactorName)
	require.NoError(t, err)
	require.True(t, out.Complete)
	require.True(t, barrier.IsSealed())

	_, err = submit(seal.OpUnseal, b, "")
	require.NoError(t, err)
	out, err = submit(seal.OpUnseal, a, "")
	require.NoError(t, err)
	assert.True(t, out.Complete)
	assert.False(t, barrier.IsSealed())

	svc.Cleanup()
}

// TestTenantCeremony_ConstructorRefusals covers the constructor checks.
func TestTenantCeremony_ConstructorRefusals(t *testing.T) {
	deps := HolderDeps{TrustDomain: testTrustDomain, CA: testCA(t)}
	backend := seal.NewMemoryBackend()
	_, err := NewTenantCeremonyService("", backend, deps, testLogger())
	assert.ErrorIs(t, err, ErrEmptyTenantID)
	_, err = NewTenantCeremonyService("acme", nil, deps, testLogger())
	assert.ErrorIs(t, err, ErrNilBackend)
	// A nil CA is accepted: only enrolment and sign-csr need it (CA m829).
	_, err = NewTenantCeremonyService("acme", backend, HolderDeps{}, testLogger())
	assert.NoError(t, err)
	_, err = NewTenantCeremonyService("acme", backend, deps, nil)
	assert.ErrorIs(t, err, ErrNilLogger)
}

// TestTenantCeremony_NilCAServesVerifyOnlyOperations proves a scoped holder
// authority with no ceremony CA -- a BYO-TLS node, or the sealed window
// before Key A is readable -- serves the operations that only verify (the
// roster read and a holder quorum to seal and unseal), and refuses
// enrolment, which signs, with ErrCANotInitialized (CA m829).
func TestTenantCeremony_NilCAServesVerifyOnlyOperations(t *testing.T) {
	ctx := context.Background()
	backend := seal.NewMemoryBackend()
	barrier := shamirBarrier(t, backend, 2, 2)
	withCA := HolderDeps{TrustDomain: testTrustDomain, CA: testCA(t), Factors: testFactors(t)}
	enroller, err := NewTenantCeremonyService("acme", backend, withCA, testLogger())
	require.NoError(t, err)
	barrier.SetCeremonyDistributor(enroller)

	a := newHolder(t, "alice", "")
	b := newHolder(t, "bob", seal.RoleCustodian).withAuthenticator(t)
	for _, h := range []*testHolder{a, b} {
		res, err := enroller.RegisterOfficer(ctx, h.request("tenant-admin"))
		require.NoError(t, err)
		h.recordCert(t, res.CertPEM)
	}
	_, err = barrier.InitializeShamir(ctx, seal.FactorPolicyAnyEnrolled)
	require.NoError(t, err)
	// Pending shares live only in the distributing authority's memory, so
	// the holders claim them there.
	for _, h := range []*testHolder{a, b} {
		ch, err := enroller.BeginClaimShare(ctx, h.username, h.thumb)
		require.NoError(t, err)
		sig, err := SignShareClaim(h.certKey, ScopeTenant("acme").ClaimLabel(), h.username, ch.Nonce)
		require.NoError(t, err)
		res, err := enroller.CompleteClaimShare(ctx, h.username, hex.EncodeToString(ch.Nonce), sig, h.thumb)
		require.NoError(t, err)
		h.envelope = res.Envelope
	}
	enroller.Cleanup()

	noCA := withCA
	noCA.CA = nil
	svc, err := NewTenantCeremonyService("acme", backend, noCA, testLogger())
	require.NoError(t, err)
	barrier.SetHolderVerifier(svc)

	_, err = svc.RegisterOfficer(ctx, newHolder(t, "carol", "").request("tenant-admin"))
	require.ErrorIs(t, err, ErrCANotInitialized, "enrolment signs, so it needs the CA")

	list, err := svc.ListOfficers(ctx)
	require.NoError(t, err)
	require.Len(t, list, 2)

	submit := func(op seal.QuorumOp, h *testHolder, factor string) (*seal.QuorumOutcome, error) {
		ch, err := svc.BeginQuorum(ctx, barrier, op, h.username, factor)
		if err != nil {
			return nil, err
		}
		return barrier.SubmitHolderShare(ctx, &seal.HolderSubmission{
			Domain: seal.DomainUnseal, Op: op, Holder: h.username,
			Proof: seal.HolderProof{ChallengeID: ch.ChallengeID, Factor: ch.Factor, Response: h.answer(t, ch)},
			Share: h.openShare(t),
		})
	}
	_, err = submit(seal.OpSeal, a, "")
	require.NoError(t, err)
	out, err := submit(seal.OpSeal, b, testFactorName)
	require.NoError(t, err)
	require.True(t, out.Complete)
	require.True(t, barrier.IsSealed())

	_, err = submit(seal.OpUnseal, b, "")
	require.NoError(t, err)
	out, err = submit(seal.OpUnseal, a, "")
	require.NoError(t, err)
	assert.True(t, out.Complete)
	assert.False(t, barrier.IsSealed())

	svc.Cleanup()
}

// TestTenantCeremony_SeesCAWiredAfterConstruction proves a scoped holder
// authority built from CeremonyService.HolderDeps before any CA is wired
// signs with the CA ReplaceCA installs later: a joiner or a restarted
// founder builds its holder authorities sealed and gets Key A after unseal.
func TestTenantCeremony_SeesCAWiredAfterConstruction(t *testing.T) {
	ctx := context.Background()
	core := newBareTestService(t)
	deps := core.HolderDeps()
	require.Nil(t, deps.CA)
	deps.TrustDomain = testTrustDomain
	svc, err := NewTenantCeremonyService("acme", seal.NewMemoryBackend(), deps, testLogger())
	require.NoError(t, err)
	t.Cleanup(svc.Cleanup)

	_, err = svc.RegisterOfficer(ctx, newHolder(t, "alice", "").request("tenant-admin"))
	require.ErrorIs(t, err, ErrCANotInitialized)

	keyA := testCA(t)
	core.ReplaceCA(keyA)
	res, err := svc.RegisterOfficer(ctx, newHolder(t, "alice", "").request("tenant-admin"))
	require.NoError(t, err)
	cert, err := mtls.ParseCertificatePEM(res.CertPEM)
	require.NoError(t, err)
	caCert, err := keyA.CACertificate(ctx)
	require.NoError(t, err)
	assert.NoError(t, cert.CheckSignatureFrom(caCert), "the holder certificate chains to the CA wired after construction")
}

// TestScopedCeremonyRegistry covers the per-tenant service registry.
func TestScopedCeremonyRegistry(t *testing.T) {
	reg := NewScopedCeremonyRegistry()
	_, ok := reg.Get("acme")
	assert.False(t, ok)
	svc, err := NewTenantCeremonyService("acme", seal.NewMemoryBackend(), HolderDeps{TrustDomain: testTrustDomain, CA: testCA(t)}, testLogger())
	require.NoError(t, err)
	reg.Set("acme", svc)
	got, ok := reg.Get("acme")
	require.True(t, ok)
	assert.Same(t, svc, got)
	reg.Delete("acme")
	_, ok = reg.Get("acme")
	assert.False(t, ok)
	reg.Delete("absent")
}

// TestCeremonyErrors_MessagesAndUnwrap pins every error type's message and
// cause chain.
func TestCeremonyErrors_MessagesAndUnwrap(t *testing.T) {
	cause := errors.New("cause")
	wrapped := []interface {
		error
		Unwrap() error
	}{
		&OfficerRegistryError{Op: "save", Err: cause},
		&ShamirSplitError{Err: cause},
		&CSRParseError{Err: cause},
		&CertEncodeError{Err: cause},
		&CACertParseError{Err: cause},
		&CAKeyParseError{Err: cause},
		&ShareStoreError{Err: cause},
		&CABundleError{Err: cause},
		&ServerTLSError{Err: cause},
		&SPKIPinError{Err: cause},
		&FactorEnrollmentError{Factor: FactorMTLS, Err: cause},
		&EncryptionKeyRequiredError{Err: cause},
		&HolderFactorEnrollmentError{Factor: "fido2", Err: cause},
		&ShareEnvelopeError{Holder: "alice", Err: cause},
		&ShareEnvelopeError{Err: cause},
	}
	for _, e := range wrapped {
		assert.NotEmpty(t, e.Error())
		assert.ErrorIs(t, e, cause, "%T", e)
	}
	plain := []error{
		&UnsupportedFactorError{Factor: "x"},
		&FactorUnavailableError{Factor: FactorFIDO2, Reason: "none"},
		&InvalidEnrollmentSpecError{Arg: "a", Reason: "b"},
		&DuplicateEnrollmentUsernameError{Username: "u"},
		&InvalidHolderNameError{Name: "a/b"},
		&DuplicateFactorError{Factor: "mtls"},
		&FactorNotEnrolledError{Factor: "piv"},
	}
	for _, e := range plain {
		assert.NotEmpty(t, e.Error(), "%T", e)
	}
}

// TestParseShareEnvelope_Refusals covers malformed envelopes.
func TestParseShareEnvelope_Refusals(t *testing.T) {
	_, err := ParseShareEnvelope([]byte("not json"))
	assert.True(t, errorAs[*ShareEnvelopeError](err), err)
	_, err = ParseShareEnvelope([]byte(`{"version":1}`))
	assert.ErrorIs(t, err, ErrShareEnvelopeMalformed)
	_, err = ParseShareEnvelope([]byte(`{"version":99,"ciphertext":"AA=="}`))
	assert.ErrorIs(t, err, ErrShareEnvelopeMalformed)

	env := &ShareEnvelope{Version: shareEnvelopeVersion, KEM: "bogus", Ciphertext: []byte{1}}
	_, err = env.Open(nil)
	assert.Error(t, err)
	_, err = env.OpenWithKey(newHolder(t, "a", "").certKey)
	assert.Error(t, err)
}
