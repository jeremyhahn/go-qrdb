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

package service

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/authfactors/mtls"
	"github.com/jeremyhahn/go-qrdb/pkg/ceremony"
	"github.com/jeremyhahn/go-qrdb/pkg/keydelivery"
	"github.com/jeremyhahn/go-qrdb/pkg/pin"
	"github.com/jeremyhahn/go-qrdb/pkg/pin/pintest"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
)

// svcHolder is one share holder's client side.
type svcHolder struct {
	name     string
	key      *ecdsa.PrivateKey
	csr      []byte
	thumb    string
	envelope []byte
}

func newSvcHolder(t *testing.T, name string) *svcHolder {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	der, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{Subject: pkix.Name{CommonName: name}}, key)
	require.NoError(t, err)
	return &svcHolder{name: name, key: key, csr: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: der})}
}

// holderSvcEnv is a Shamir barrier behind the service layer with a real
// ceremony service wired as a composer wires it.
type holderSvcEnv struct {
	t       *testing.T
	backend seal.StorageBackend
	barrier *seal.StorageBarrier
	core    *ceremony.CeremonyService
	svc     BarrierServicer
	cer     CeremonyServicer
	ca      *testCeremonyCA
	pins    pin.PINBackend
}

func shamirTestBarrier(t *testing.T, backend seal.StorageBackend, m, n int) *seal.StorageBarrier {
	t.Helper()
	shamirStrategy, err := seal.NewShamirStrategy(m, n)
	require.NoError(t, err)
	b, err := seal.NewStorageBarrier(testLogger(), backend, seal.BarrierConfig{
		RootKeyPath: "root-key",
		Shamir:      &seal.ShamirConfig{Threshold: m, TotalShares: n},
	}, shamirStrategy)
	require.NoError(t, err)
	return b
}

func newHolderSvcEnv(t *testing.T, m, n int) *holderSvcEnv {
	t.Helper()
	e := &holderSvcEnv{t: t, backend: seal.NewMemoryBackend(), ca: newTestCeremonyCA(t), pins: pintest.NewBackend(t)}
	e.barrier = shamirTestBarrier(t, e.backend, m, n)
	e.wire(e.barrier)
	return e
}

// wire builds the ceremony and service layer over barrier.
func (e *holderSvcEnv) wire(barrier *seal.StorageBarrier) {
	e.t.Helper()
	core, err := ceremony.NewCeremonyService(&ceremony.CeremonyConfig{DataDir: e.t.TempDir()}, barrier, testLogger())
	require.NoError(e.t, err)
	core.SetCA(e.ca)
	core.SetTrustDomain("db.example.com")
	core.SetPINBackend(e.pins)
	e.barrier, e.core = barrier, core
	e.svc = NewBarrierServiceWithCeremony(barrier, core)
	e.cer = NewCeremonyService(core)
}

// adminCtx is a context authenticated as the enrolling admin.
func adminCtx() context.Context {
	return auth.WithIdentity(context.Background(), &auth.Identity{Subject: "admin"})
}

func (e *holderSvcEnv) register(domain seal.QuorumDomain, hs ...*svcHolder) {
	e.t.Helper()
	for _, h := range hs {
		res, err := e.svc.BarrierOfficerRegister(adminCtx(), domain, &BarrierOfficerRegisterRequest{Username: h.name, CSRPEM: h.csr})
		require.NoError(e.t, err)
		cert, err := mtls.ParseCertificatePEM(res.CertPEM)
		require.NoError(e.t, err)
		h.thumb = mtls.Thumbprint(cert.Raw)
	}
}

func (e *holderSvcEnv) claim(domain seal.QuorumDomain, h *svcHolder) {
	e.t.Helper()
	ctx := context.Background()
	ch, err := e.svc.BarrierShareClaimBegin(ctx, domain, h.name, h.thumb)
	require.NoError(e.t, err)
	nonce, err := hex.DecodeString(ch.NonceHex)
	require.NoError(e.t, err)
	label := ceremony.SystemClaimLabel
	if domain == seal.DomainRecovery {
		label = ceremony.RecoveryClaimLabel
	}
	sig, err := ceremony.SignShareClaim(h.key, label, h.name, nonce)
	require.NoError(e.t, err)
	res, err := e.svc.BarrierShareClaimComplete(ctx, domain, h.name, ch.NonceHex, sig, h.thumb)
	require.NoError(e.t, err)
	h.envelope = res.Envelope
}

func (e *holderSvcEnv) submit(domain seal.QuorumDomain, op seal.QuorumOp, h *svcHolder) (*QuorumOutcomeInfo, error) {
	e.t.Helper()
	ctx := context.Background()
	ch, err := e.svc.BarrierQuorumBegin(ctx, &QuorumBeginRequest{Domain: domain, Op: op, Username: h.name})
	if err != nil {
		return nil, err
	}
	sig, err := mtls.SignNonce(h.key, ch.Nonce)
	require.NoError(e.t, err)
	env, err := ceremony.ParseShareEnvelope(h.envelope)
	require.NoError(e.t, err)
	share, err := env.OpenWithKey(h.key)
	require.NoError(e.t, err)
	return e.svc.BarrierQuorumSubmit(ctx, &QuorumSubmitRequest{
		Domain: domain, Op: op, Username: h.name, ChallengeID: ch.ChallengeID, Factor: ch.Factor, Response: sig, Share: share,
	})
}

// TestBarrierService_HolderCustodyEndToEnd drives the whole service surface:
// enrolment, initialization, claim, restart, quorum unseal, root token,
// rekey and revocation.
func TestBarrierService_HolderCustodyEndToEnd(t *testing.T) {
	ctx := context.Background()
	e := newHolderSvcEnv(t, 2, 3)
	a, b, c := newSvcHolder(t, "alice"), newSvcHolder(t, "bob"), newSvcHolder(t, "carol")
	e.register(seal.DomainUnseal, a, b, c)

	info, err := e.svc.BarrierInitializeShamir(ctx, 2, 3, seal.FactorPolicyDesignated)
	require.NoError(t, err)
	assert.Equal(t, 3, info.ShareCount)
	status, err := e.svc.BarrierStatus(ctx)
	require.NoError(t, err)
	assert.Equal(t, 2, status.ShamirThreshold)
	assert.Equal(t, 3, status.ShamirTotal)

	list, err := e.svc.BarrierOfficerList(ctx, seal.DomainUnseal)
	require.NoError(t, err)
	assert.Len(t, list, 3)
	for _, h := range []*svcHolder{a, b, c} {
		e.claim(seal.DomainUnseal, h)
	}

	_, err = e.submit(seal.DomainUnseal, seal.OpRootToken, a)
	require.NoError(t, err)
	out, err := e.submit(seal.DomainUnseal, seal.OpRootToken, b)
	require.NoError(t, err)
	require.True(t, out.Complete)
	require.NotNil(t, out.RootToken)
	assert.NotEmpty(t, out.RootToken.Token)

	// Restart sealed and unseal with a quorum.
	e.wire(shamirTestBarrier(t, e.backend, 2, 3))
	require.True(t, e.barrier.IsSealed())
	_, err = e.submit(seal.DomainUnseal, seal.OpUnseal, c)
	require.NoError(t, err)
	out, err = e.submit(seal.DomainUnseal, seal.OpUnseal, a)
	require.NoError(t, err)
	assert.True(t, out.Complete)
	assert.False(t, e.barrier.IsSealed())

	// Revoke carol: a rekey of the remaining two.
	round, err := e.svc.BarrierOfficerRevoke(ctx, seal.DomainUnseal, "carol", 0)
	require.NoError(t, err)
	assert.Equal(t, []string{"carol"}, round.Remove)
	_, err = e.submit(seal.DomainUnseal, seal.OpRekey, a)
	require.NoError(t, err)
	out, err = e.submit(seal.DomainUnseal, seal.OpRekey, b)
	require.NoError(t, err)
	require.True(t, out.Complete)
	require.NotNil(t, out.Rekey)
	assert.Equal(t, 2, out.Rekey.TotalShares)

	// An explicit rekey round opens under the service too.
	round, err = e.svc.BarrierRekeyStart(ctx, seal.DomainUnseal, 2, seal.FactorPolicyAnyEnrolled)
	require.NoError(t, err)
	assert.Equal(t, seal.OpRekey, round.Op)
}

// TestBarrierService_RecoveryKeys drives recovery-key generation.
func TestBarrierService_RecoveryKeys(t *testing.T) {
	ctx := context.Background()
	e := newHolderSvcEnv(t, 2, 2)
	a, b := newSvcHolder(t, "alice"), newSvcHolder(t, "bob")
	e.register(seal.DomainUnseal, a, b)
	_, err := e.svc.BarrierInitializeShamir(ctx, 2, 2, "")
	require.NoError(t, err)

	has, err := e.svc.BarrierHasRecoveryKeys(ctx)
	require.NoError(t, err)
	assert.False(t, has)

	r1, r2 := newSvcHolder(t, "rec-1"), newSvcHolder(t, "rec-2")
	e.register(seal.DomainRecovery, r1, r2)
	info, err := e.svc.BarrierGenerateRecoveryKeys(ctx, 2, "")
	require.NoError(t, err)
	assert.Equal(t, 2, info.ShareCount)
	has, err = e.svc.BarrierHasRecoveryKeys(ctx)
	require.NoError(t, err)
	assert.True(t, has)
	e.claim(seal.DomainRecovery, r1)

	require.NoError(t, e.svc.BarrierDeleteRecoveryKeys(ctx))
	has, err = e.svc.BarrierHasRecoveryKeys(ctx)
	require.NoError(t, err)
	assert.False(t, has)
}

// TestBarrierService_EnrollerComesFromTheAuthenticatedIdentity pins that a
// caller cannot enrol itself: the enroller is the authenticated identity,
// never a request field.
func TestBarrierService_EnrollerComesFromTheAuthenticatedIdentity(t *testing.T) {
	e := newHolderSvcEnv(t, 2, 2)
	h := newSvcHolder(t, "mallory")
	ctx := auth.WithIdentity(context.Background(), &auth.Identity{Subject: "mallory"})
	_, err := e.svc.BarrierOfficerRegister(ctx, seal.DomainUnseal, &BarrierOfficerRegisterRequest{Username: "mallory", CSRPEM: h.csr})
	assert.ErrorIs(t, err, ceremony.ErrSelfEnrolment)

	reg := HolderRegistration(adminCtx(), &BarrierOfficerRegisterRequest{
		Username: "x", Role: seal.RoleCustodian, EncryptionKEM: keydelivery.KEMX25519, EncryptionKey: []byte{1},
		Factors: []ceremony.FactorEnrolment{{Name: "fido2"}}, Designated: "fido2",
	})
	assert.Equal(t, "admin", reg.Enroller)
	require.NotNil(t, reg.EncryptionKey)
	assert.Equal(t, keydelivery.KEMX25519, reg.EncryptionKey.KEM)
	assert.Equal(t, seal.RoleCustodian, reg.Role)
	assert.Nil(t, HolderRegistration(context.Background(), &BarrierOfficerRegisterRequest{Username: "y"}).EncryptionKey)
}

// TestBarrierService_HolderOpsNeedACeremony pins the not-configured and
// nil-request refusals.
func TestBarrierService_HolderOpsNeedACeremony(t *testing.T) {
	barrier, _ := newTestSoftwareBarrier(t)
	svc := NewBarrierService(barrier)
	ctx := context.Background()
	calls := map[string]func() error{
		"register": func() error {
			_, err := svc.BarrierOfficerRegister(ctx, seal.DomainUnseal, &BarrierOfficerRegisterRequest{})
			return err
		},
		"list":        func() error { _, err := svc.BarrierOfficerList(ctx, seal.DomainUnseal); return err },
		"claim begin": func() error { _, err := svc.BarrierShareClaimBegin(ctx, seal.DomainUnseal, "a", "t"); return err },
		"claim complete": func() error {
			_, err := svc.BarrierShareClaimComplete(ctx, seal.DomainUnseal, "a", "n", nil, "t")
			return err
		},
		"quorum begin": func() error { _, err := svc.BarrierQuorumBegin(ctx, &QuorumBeginRequest{}); return err },
		"revoke":       func() error { _, err := svc.BarrierOfficerRevoke(ctx, seal.DomainUnseal, "a", 0); return err },
	}
	for name, call := range calls {
		assert.ErrorIs(t, call(), ErrCeremonyNotConfigured, name)
	}

	e := newHolderSvcEnv(t, 2, 2)
	_, err := e.svc.BarrierOfficerRegister(ctx, seal.DomainUnseal, nil)
	assert.ErrorIs(t, err, ErrNilRequest)
	_, err = e.svc.BarrierQuorumBegin(ctx, nil)
	assert.ErrorIs(t, err, ErrNilRequest)
	_, err = e.svc.BarrierQuorumSubmit(ctx, nil)
	assert.ErrorIs(t, err, ErrNilRequest)
	_, err = e.svc.BarrierQuorumSubmit(ctx, &QuorumSubmitRequest{Domain: seal.DomainUnseal, Op: seal.OpUnseal, Username: "a"})
	assert.Error(t, err, "a submission before initialization is refused")
}

// TestCeremonyService_InitializeWithHolders drives the service-layer
// ceremony: PIN provisioning, holder enrolment and the Shamir split.
func TestCeremonyService_InitializeWithHolders(t *testing.T) {
	ctx := context.Background()
	e := newHolderSvcEnv(t, 2, 2)
	a, b := newSvcHolder(t, "alice"), newSvcHolder(t, "bob")

	st, err := e.cer.CeremonyStatus(ctx)
	require.NoError(t, err)
	assert.Equal(t, string(ceremony.StateAwaitingInit), st.State)

	res, err := e.cer.CeremonyInitialize(adminCtx(), e.ca, &CeremonyInitializeOptions{
		SOPin: "so-pin-1234", UserPin: "user-pin-1234", Threshold: 2, Policy: seal.FactorPolicyAnyEnrolled,
		Officers: []CeremonyOfficerConfig{{Username: "alice", CSRPEM: string(a.csr)}, {Username: "bob", Role: seal.RoleCustodian, CSRPEM: string(b.csr)}},
		Hostname: "db.example.com",
	})
	require.NoError(t, err)
	assert.Equal(t, 2, res.ShareCount)
	require.Len(t, res.Holders, 2)
	assert.Equal(t, seal.RoleCustodian, res.Holders[1].Role)

	signed, err := e.cer.CeremonySignCSR(ctx, "operator-1", "so-pin-1234", string(newTestOfficerCSR(t, "ignored")), "admin")
	require.NoError(t, err)
	cert, err := mtls.ParseCertificatePEM(signed.CertPEM)
	require.NoError(t, err)
	assert.Equal(t, "operator-1", cert.Subject.CommonName)

	_, err = e.cer.CeremonySignCSR(ctx, "operator-2", "wrong", string(newTestOfficerCSR(t, "x")), "admin")
	assert.ErrorIs(t, err, pin.ErrPINInvalid)

	core, ok := CeremonyServiceCore(e.cer)
	require.True(t, ok)
	assert.Same(t, e.core, core)
}

// TestClientCertRequiredError_Error pins the message every transport carries.
func TestClientCertRequiredError_Error(t *testing.T) {
	assert.Equal(t, "service: a verified client certificate is required", (&ClientCertRequiredError{}).Error())
}
