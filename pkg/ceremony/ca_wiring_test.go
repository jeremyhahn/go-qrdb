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
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/authfactors/mtls"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
)

// newBareTestService builds a CeremonyService over a fresh in-memory
// barrier, with no CA, PIN backend, or factor registry wired -- the state
// SetCA/ReplaceCA's own unit tests want, distinct from newHolderEnv's
// (which wires a CA at construction for the full-ceremony tests elsewhere
// in this package).
func newBareTestService(t *testing.T) *CeremonyService {
	t.Helper()
	svc, err := NewCeremonyService(&CeremonyConfig{DataDir: t.TempDir()}, testBarrier(t), testLogger())
	require.NoError(t, err)
	return svc
}

// generateTestCSRPEM builds a minimal self-signed PKCS#10 request for cn,
// standing in for a holder's or officer's CSR.
func generateTestCSRPEM(t *testing.T, cn string) []byte {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	der, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{
		Subject: pkix.Name{CommonName: cn},
	}, key)
	require.NoError(t, err)
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: der})
}

// ---------------------------------------------------------------------------
// SetCA
// ---------------------------------------------------------------------------

// TestSetCA_NilIsNoOp verifies that a nil CA argument leaves the service
// unchanged in both the unset and already-set states.
func TestSetCA_NilIsNoOp(t *testing.T) {
	svc := newBareTestService(t)
	svc.SetCA(nil)
	assert.Nil(t, svc.CA())

	ca := testCA(t)
	svc.SetCA(ca)
	svc.SetCA(nil)
	assert.Same(t, CeremonyCA(ca), svc.CA())
}

// TestSetCA_DoesNotOverwrite verifies that a CA already installed (e.g. by
// Initialize during the full ceremony) remains authoritative -- SetCA from
// lifecycle wiring must never clobber it.
func TestSetCA_DoesNotOverwrite(t *testing.T) {
	svc := newBareTestService(t)

	first := testCA(t)
	second := testCA(t)
	svc.SetCA(first)
	svc.SetCA(second)
	assert.Same(t, CeremonyCA(first), svc.CA())
}

// TestSetCA_DoesNotOverwriteInitializeCA verifies the Initialize-then-SetCA
// ordering: the ceremony-established CA wins.
func TestSetCA_DoesNotOverwriteInitializeCA(t *testing.T) {
	svc := newTestCeremonyService(t, &CeremonyConfig{DataDir: t.TempDir()}, noneStrategyBarrier(t))
	initCA := testCA(t)
	_, err := svc.Initialize(context.Background(), initCA, initOpts(0))
	require.NoError(t, err)

	svc.SetCA(testCA(t))
	assert.Same(t, CeremonyCA(initCA), svc.CA())
}

// ---------------------------------------------------------------------------
// ReplaceCA
// ---------------------------------------------------------------------------

// TestReplaceCA_SwapsAPreviouslySetCA is the B4 proof that lifecycle wiring
// can move ceremony signing from the bootstrap-window CeremonyCA (a
// cakey.StaticProvider wrapping the in-memory cluster CA key, set via SetCA
// before any store exists) onto the cluster CA provider (set via
// ReplaceCA once the replicated store is live) -- something SetCA's own
// "never overwrite" rule cannot do, because SetCA already saw a non-nil
// value.
func TestReplaceCA_SwapsAPreviouslySetCA(t *testing.T) {
	svc := newBareTestService(t)

	bootstrapCA := testCA(t)
	svc.SetCA(bootstrapCA)
	require.Same(t, CeremonyCA(bootstrapCA), svc.CA())

	clusterCA := testCA(t)
	svc.ReplaceCA(clusterCA)
	assert.Same(t, CeremonyCA(clusterCA), svc.CA())
}

// TestReplaceCA_NilIsNoOp mirrors SetCA's own nil handling.
func TestReplaceCA_NilIsNoOp(t *testing.T) {
	svc := newBareTestService(t)
	ca := testCA(t)
	svc.SetCA(ca)

	svc.ReplaceCA(nil)
	assert.Same(t, CeremonyCA(ca), svc.CA())
}

// TestReplaceCA_TakesEffectPastInitialize proves ReplaceCA installs Key A
// on a ceremony Initialize already took operational: every node serves every
// CA operation from the replicated Key A, so the lifecycle's post-unseal
// ReplaceCA must take effect whatever the ceremony's state (CA m829).
func TestReplaceCA_TakesEffectPastInitialize(t *testing.T) {
	svc := newTestCeremonyService(t, &CeremonyConfig{DataDir: t.TempDir()}, noneStrategyBarrier(t))
	initCA := testCA(t)
	_, err := svc.Initialize(context.Background(), initCA, initOpts(0))
	require.NoError(t, err)
	require.Equal(t, StateOperational, svc.State())

	keyA := testCA(t)
	svc.ReplaceCA(keyA)
	assert.Same(t, CeremonyCA(keyA), svc.CA(),
		"ReplaceCA must install Key A on an operational ceremony")
	assert.Same(t, CeremonyCA(keyA), svc.HolderDeps().CA)
}

// TestReplaceCA_PostOperationalSigningSurvivesRestart drives the exact
// scenario the B4 ruling asks for, through a GENUINE restart: a fresh
// *CeremonyService constructed over the SAME barrier an earlier process's
// Initialize already took operational (go-qrdb#560 re-review: the previous
// version of this test never constructed a second service at all, so it
// could not have caught restoreState leaving a restarted ceremony at
// StateAwaitingInit -- see TestInitialize_RestoresStateAcrossRestart for
// that fix directly).
//
// The CA itself is in-memory only and never survives the process boundary:
// a restarted ceremony holds none until the lifecycle installs Key A via
// ReplaceCA after unseal. A holder authority built before then shares the
// service's live CA cell, so it signs with Key A as soon as it is wired.
func TestReplaceCA_PostOperationalSigningSurvivesRestart(t *testing.T) {
	barrier := noneStrategyBarrier(t)
	svc := newTestCeremonyService(t, &CeremonyConfig{DataDir: t.TempDir()}, barrier)
	initCA := testCA(t)
	_, err := svc.Initialize(context.Background(), initCA, initOpts(0))
	require.NoError(t, err)
	require.Equal(t, StateOperational, svc.State())

	// A genuine restart: a fresh service over the SAME barrier, not the
	// same Go value Initialize already ran on.
	restarted, err := NewCeremonyService(&CeremonyConfig{DataDir: t.TempDir()}, barrier, testLogger())
	require.NoError(t, err)
	require.Equal(t, StateOperational, restarted.State(),
		"state must be restored across the restart for the wiring below to mean anything")
	require.Nil(t, restarted.CA(), "a restarted ceremony holds no CA until one is wired")

	// A holder authority built before unseal sees the CA wired later,
	// because it shares the service's live CA cell.
	early := restarted.HolderDeps()
	require.Nil(t, early.ceremonyCA())

	// After unseal the lifecycle installs Key A via ReplaceCA.
	keyA := testCA(t)
	restarted.ReplaceCA(keyA)
	require.Same(t, CeremonyCA(keyA), restarted.CA(),
		"ReplaceCA must install Key A on an operational ceremony after a restart")
	require.Same(t, CeremonyCA(keyA), early.ceremonyCA(),
		"a holder authority built before unseal must see Key A once it is wired")

	csrPEM := generateTestCSRPEM(t, "post-restart-officer")
	cert, err := restarted.CA().SignCSR(context.Background(), csrPEM, &SignCSROptions{
		Username:    "officer1",
		TenantID:    tenantid.System,
		TrustDomain: testTrustDomain,
	})
	require.NoError(t, err)
	require.NotNil(t, cert)
	caCert, err := keyA.CACertificate(t.Context())
	require.NoError(t, err)
	require.NoError(t, cert.CheckSignatureFrom(caCert),
		"post-restart signing must chain to Key A")
}

// TestSignCSRInit_SignsWithCAReplacedWhileEnrolling proves sign-csr signs
// with the CA ReplaceCA installs mid-ceremony, not the one Initialize saw.
func TestSignCSRInit_SignsWithCAReplacedWhileEnrolling(t *testing.T) {
	ctx := context.Background()
	e := newHolderEnv(t, 2, 2)
	_, err := e.svc.Initialize(ctx, e.ca, initOpts(2, newHolder(t, "alice", seal.RoleSecurityOfficer), newHolder(t, "bob", seal.RoleSecurityOfficer)))
	require.NoError(t, err)
	require.Equal(t, StateEnrolling, e.svc.State())

	keyA := testCA(t)
	e.svc.ReplaceCA(keyA)
	res, err := e.svc.SignCSRInit(ctx, "operator-1", "so-pin-1234", newHolder(t, "ignored", "").csrPEM, "so")
	require.NoError(t, err)
	cert, err := mtls.ParseCertificatePEM(res.CertPEM)
	require.NoError(t, err)
	caCert, err := keyA.CACertificate(ctx)
	require.NoError(t, err)
	assert.NoError(t, cert.CheckSignatureFrom(caCert))
}

// TestSignCSRInit_NoCARefuses proves sign-csr on a node with no ceremony CA
// refuses with ErrCANotInitialized before it reads any argument.
func TestSignCSRInit_NoCARefuses(t *testing.T) {
	svc := newBareTestService(t)
	svc.mu.Lock()
	svc.state = StateEnrolling
	svc.mu.Unlock()

	_, err := svc.SignCSRInit(context.Background(), "", "", nil, "")
	assert.ErrorIs(t, err, ErrCANotInitialized)
}

// TestCACell_ConcurrentReplaceIsRaceFree drives ReplaceCA against readers
// of every copy of the holder dependencies; run under -race.
func TestCACell_ConcurrentReplaceIsRaceFree(t *testing.T) {
	svc := newBareTestService(t)
	deps := svc.HolderDeps()
	cas := []CeremonyCA{testCA(t), testCA(t)}

	var wg sync.WaitGroup
	for i := range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 200 {
				svc.ReplaceCA(cas[i%len(cas)])
				_ = deps.ceremonyCA()
				_ = svc.CA()
				_ = svc.HolderDeps()
			}
		}()
	}
	wg.Wait()
	assert.Same(t, svc.CA(), deps.ceremonyCA(), "every copy reads the same live CA")
}

// TestHolderDeps_CeremonyCA covers the live-cell lookup and its fallback to
// the deps' own CA field.
func TestHolderDeps_CeremonyCA(t *testing.T) {
	own := testCA(t)
	assert.Same(t, CeremonyCA(own), (&HolderDeps{CA: own}).ceremonyCA(), "no live cell: the deps' own CA")
	assert.Nil(t, (&HolderDeps{}).ceremonyCA())

	live := &caCell{}
	d := &HolderDeps{CA: own, live: live}
	assert.Same(t, CeremonyCA(own), d.ceremonyCA(), "an empty live cell falls back to the deps' own CA")
	wired := testCA(t)
	live.store(wired)
	assert.Same(t, CeremonyCA(wired), d.ceremonyCA(), "a wired live cell wins")
}

// TestCeremonyService_HolderDepsSnapshotsTheLiveCA proves HolderDeps reports
// the CA wired at the time of the call in its CA field.
func TestCeremonyService_HolderDepsSnapshotsTheLiveCA(t *testing.T) {
	svc := newBareTestService(t)
	assert.Nil(t, svc.HolderDeps().CA)
	ca := testCA(t)
	svc.SetCA(ca)
	assert.Same(t, CeremonyCA(ca), svc.HolderDeps().CA)
}
