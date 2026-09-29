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
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"log/slog"
	"math/big"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/authfactors"
	"github.com/jeremyhahn/go-qrdb/pkg/authfactors/factortest"
	"github.com/jeremyhahn/go-qrdb/pkg/authfactors/mtls"
	"github.com/jeremyhahn/go-qrdb/pkg/keydelivery"
	"github.com/jeremyhahn/go-qrdb/pkg/pin"
	"github.com/jeremyhahn/go-qrdb/pkg/pin/pintest"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
)

// testTrustDomain is the SPIFFE trust domain of test holder identities.
const testTrustDomain = "db.example.com"

// testFactorName is the name the assertion-factor double is registered under.
const testFactorName = "fido2"

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
}

// testCA builds a LocalCA over a fresh self-signed P-256 CA.
func testCA(t testing.TB) *LocalCA {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "Test Ceremony CA"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	require.NoError(t, err)
	keyDER, err := x509.MarshalECPrivateKey(key)
	require.NoError(t, err)
	ca, err := NewLocalCA(
		pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}),
	)
	require.NoError(t, err)
	return ca
}

// testHolder is one share holder's client side: its certificate key, an
// optional separate envelope key, and an optional assertion authenticator.
type testHolder struct {
	username string
	role     seal.HolderRole
	certKey  crypto.Signer
	csrPEM   []byte
	encKEM   string
	encPriv  []byte // raw HPKE private key when a separate key is enrolled
	encPub   []byte
	auth     *factortest.Authenticator
	certPEM  []byte
	thumb    string
	envelope []byte
}

// newHolderWithKey builds a holder whose certificate key is key. The CSR
// asks for an identity the CA must ignore.
func newHolderWithKey(t testing.TB, username string, role seal.HolderRole, key crypto.Signer) *testHolder {
	t.Helper()
	der, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{
		Subject: pkix.Name{CommonName: "admin", OrganizationalUnit: []string{"system", "security-officer"}},
	}, key)
	require.NoError(t, err)
	return &testHolder{
		username: username,
		role:     role,
		certKey:  key,
		csrPEM:   pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: der}),
	}
}

// newHolder builds a holder with a P-256 certificate key.
func newHolder(t testing.TB, username string, role seal.HolderRole) *testHolder {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	return newHolderWithKey(t, username, role, key)
}

// newP384Holder builds a holder with a P-384 certificate key.
func newP384Holder(t testing.TB, username string, role seal.HolderRole) *testHolder {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	require.NoError(t, err)
	return newHolderWithKey(t, username, role, key)
}

// newRSAHolder builds a holder with an RSA certificate key, which no KEM
// seals to directly.
func newRSAHolder(t testing.TB, username string, role seal.HolderRole) *testHolder {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	return newHolderWithKey(t, username, role, key)
}

// withEncryptionKey gives the holder a separate envelope key of kem.
func (h *testHolder) withEncryptionKey(t testing.TB, kem string) *testHolder {
	t.Helper()
	suite := mustSuite(t, kem)
	sk, err := suite.GenerateKey()
	require.NoError(t, err)
	priv, err := sk.Bytes()
	require.NoError(t, err)
	h.encKEM = kem
	h.encPriv = priv
	h.encPub = sk.PublicKey().Bytes()
	return h
}

// withAuthenticator gives the holder an assertion-factor authenticator.
func (h *testHolder) withAuthenticator(t testing.TB) *testHolder {
	t.Helper()
	a, err := factortest.NewAuthenticator()
	require.NoError(t, err)
	h.auth = a
	return h
}

// request builds the holder's registration request.
func (h *testHolder) request(enroller string) RegisterOfficerRequest {
	req := RegisterOfficerRequest{Username: h.username, Role: h.role, CSRPEM: h.csrPEM, Enroller: enroller}
	if h.encKEM != "" {
		req.EncryptionKey = &EncryptionKeyEnrolment{KEM: h.encKEM, PublicKey: h.encPub}
	}
	if h.auth != nil {
		req.Factors = []FactorEnrolment{{Name: testFactorName, Params: h.auth.EnrollParams()}}
	}
	return req
}

// recordCert stores the issued certificate on the holder.
func (h *testHolder) recordCert(t testing.TB, certPEM []byte) {
	t.Helper()
	cert, err := mtls.ParseCertificatePEM(certPEM)
	require.NoError(t, err)
	h.certPEM = certPEM
	h.thumb = mtls.Thumbprint(cert.Raw)
}

// openShare opens the holder's claimed envelope with its own key.
func (h *testHolder) openShare(t testing.TB) []byte {
	t.Helper()
	env, err := ParseShareEnvelope(h.envelope)
	require.NoError(t, err)
	if h.encKEM == "" {
		share, err := env.OpenWithKey(h.certKey)
		require.NoError(t, err)
		return share
	}
	sk, err := mustSuite(t, h.encKEM).PrivateKey(h.encPriv)
	require.NoError(t, err)
	share, err := env.Open(sk)
	require.NoError(t, err)
	return share
}

// answer produces the holder's response to a quorum challenge.
func (h *testHolder) answer(t testing.TB, ch *QuorumChallenge) []byte {
	t.Helper()
	if ch.Factor == testFactorName {
		require.NotNil(t, h.auth, "holder has no authenticator")
		return h.auth.Assert(ch.Nonce)
	}
	sig, err := mtls.SignNonce(h.certKey, ch.Nonce)
	require.NoError(t, err)
	return sig
}

// testBarrier returns an uninitialized passphrase barrier over memory.
func testBarrier(t testing.TB) *seal.StorageBarrier {
	t.Helper()
	strategy, err := seal.NewSoftwareStrategy([]byte("test-master-password"))
	require.NoError(t, err)
	b, err := seal.NewStorageBarrier(testLogger(), seal.NewMemoryBackend(), seal.BarrierConfig{RootKeyPath: "test/root-key"}, strategy)
	require.NoError(t, err)
	return b
}

// testFactors returns a registry carrying the assertion-factor double.
func testFactors(t testing.TB) *authfactors.FactorRegistry {
	t.Helper()
	reg := authfactors.NewFactorRegistry()
	require.NoError(t, reg.Register(factortest.New(testFactorName, 0)))
	return reg
}

// shamirBarrier builds an uninitialized barrier over backend configured for
// Shamir custody at threshold-of-total.
func shamirBarrier(t testing.TB, backend seal.StorageBackend, threshold, total int) *seal.StorageBarrier {
	t.Helper()
	strat, err := seal.NewShamirStrategy(threshold, total)
	require.NoError(t, err)
	b, err := seal.NewStorageBarrier(testLogger(), backend, seal.BarrierConfig{
		RootKeyPath: "test/root-key",
		Shamir:      &seal.ShamirConfig{Threshold: threshold, TotalShares: total},
	}, strat)
	require.NoError(t, err)
	return b
}

// holderEnv is a ceremony service over a Shamir barrier, with its CA, PIN
// backend and factor registry wired as a composer wires them.
type holderEnv struct {
	t       testing.TB
	backend seal.StorageBackend
	barrier *seal.StorageBarrier
	svc     *CeremonyService
	ca      *LocalCA
	pins    *pin.SoftwareBackend
	factors *authfactors.FactorRegistry
}

// newHolderEnv builds a fresh environment for threshold-of-total custody.
func newHolderEnv(t testing.TB, threshold, total int) *holderEnv {
	t.Helper()
	e := &holderEnv{t: t, backend: seal.NewMemoryBackend(), ca: testCA(t), pins: pintest.NewBackend(t), factors: testFactors(t)}
	e.barrier = shamirBarrier(t, e.backend, threshold, total)
	e.svc = e.newService(e.barrier)
	return e
}

// newService builds a ceremony service over barrier with the env's deps.
func (e *holderEnv) newService(barrier *seal.StorageBarrier) *CeremonyService {
	e.t.Helper()
	svc, err := NewCeremonyService(&CeremonyConfig{DataDir: e.t.TempDir()}, barrier, testLogger())
	require.NoError(e.t, err)
	svc.SetCA(e.ca)
	svc.SetTrustDomain(testTrustDomain)
	svc.SetFactorRegistry(e.factors)
	svc.SetPINBackend(e.pins)
	return svc
}

// register enrols holders in domain, recording their certificates.
func (e *holderEnv) register(domain seal.QuorumDomain, holders ...*testHolder) {
	e.t.Helper()
	for _, h := range holders {
		res, err := e.svc.RegisterOfficer(context.Background(), domain, h.request("admin"))
		require.NoError(e.t, err, h.username)
		h.recordCert(e.t, res.CertPEM)
	}
}

// claim runs the holder's claim handshake in domain and stores its envelope.
func (e *holderEnv) claim(domain seal.QuorumDomain, h *testHolder) {
	e.t.Helper()
	ctx := context.Background()
	ch, err := e.svc.BeginClaimShare(ctx, domain, h.username, h.thumb)
	require.NoError(e.t, err, h.username)
	sig, err := mtls.SignNonce(h.certKey, ch.Nonce)
	require.NoError(e.t, err)
	res, err := e.svc.CompleteClaimShare(ctx, domain, h.username, hex.EncodeToString(ch.Nonce), sig, h.thumb)
	require.NoError(e.t, err, h.username)
	h.envelope = res.Envelope
}

// initShamir enrols holders in the unseal domain, initializes Shamir custody
// and has every holder claim its envelope.
func (e *holderEnv) initShamir(policy seal.FactorPolicy, holders ...*testHolder) {
	e.t.Helper()
	e.register(seal.DomainUnseal, holders...)
	_, err := e.barrier.InitializeShamir(context.Background(), policy)
	require.NoError(e.t, err)
	for _, h := range holders {
		e.claim(seal.DomainUnseal, h)
	}
}

// submit authenticates h with factor and submits its share to op in domain.
func (e *holderEnv) submit(svc *CeremonyService, barrier *seal.StorageBarrier, domain seal.QuorumDomain, op seal.QuorumOp, h *testHolder, factor string) (*seal.QuorumOutcome, error) {
	e.t.Helper()
	ctx := context.Background()
	ch, err := svc.BeginQuorum(ctx, domain, op, h.username, factor)
	if err != nil {
		return nil, err
	}
	return barrier.SubmitHolderShare(ctx, &seal.HolderSubmission{
		Domain: domain,
		Op:     op,
		Holder: h.username,
		Proof:  seal.HolderProof{ChallengeID: ch.ChallengeID, Factor: ch.Factor, Response: h.answer(e.t, ch)},
		Share:  h.openShare(e.t),
	})
}

// restart simulates a node restart: a fresh, sealed barrier and ceremony
// service over the same persisted state.
func (e *holderEnv) restart(threshold, total int) (*seal.StorageBarrier, *CeremonyService) {
	e.t.Helper()
	b := shamirBarrier(e.t, e.backend, threshold, total)
	require.True(e.t, b.IsSealed())
	return b, e.newService(b)
}

// mustSuite looks up an HPKE suite.
func mustSuite(t testing.TB, kem string) *keydelivery.Suite {
	t.Helper()
	s, err := keydelivery.LookupSuite(kem)
	require.NoError(t, err)
	return s
}

// errorAs is a typed errors.As helper.
func errorAs[T error](err error) bool {
	var target T
	return errors.As(err, &target)
}
