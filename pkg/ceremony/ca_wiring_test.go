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
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

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

// TestSetCA_WiresWhenUnset verifies that lifecycle wiring can install a
// CeremonyCA on a service that has not run Initialize, enabling officer
// registration in daemon deployments.
func TestSetCA_WiresWhenUnset(t *testing.T) {
	svc := newBareTestService(t)
	require.Nil(t, svc.CA())

	ca := testCA(t)
	svc.SetCA(ca)
	assert.Same(t, CeremonyCA(ca), svc.CA())
}

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

// TestReplaceCA_DoesNotOverwriteInitializeCA proves ReplaceCA respects the
// SAME authority rule SetCA does: once Initialize has run and established
// its own CA, ReplaceCA from lifecycle wiring (e.g. a later boot's cluster
// CA provider becoming available) must not clobber it. This is the
// "post-operational ceremony signing works after a restart" case: a node
// that completed its ceremony keeps signing with the CA Initialize chose,
// not whatever cmd/qrdbd's boot sequence tries to install next.
func TestReplaceCA_DoesNotOverwriteInitializeCA(t *testing.T) {
	svc := newTestCeremonyService(t, &CeremonyConfig{DataDir: t.TempDir()}, noneStrategyBarrier(t))
	initCA := testCA(t)
	_, err := svc.Initialize(context.Background(), initCA, initOpts(0))
	require.NoError(t, err)
	require.Equal(t, StateOperational, svc.State())

	svc.ReplaceCA(testCA(t))
	assert.Same(t, CeremonyCA(initCA), svc.CA())
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
// deps.CA itself is in-memory only and never survives the process boundary
// on its own -- the composer's boot sequence re-wires the SAME underlying
// CA key material via SetCA on every boot, operational ceremony or not,
// which is what makes ReplaceCA's guard (go-qrdb#560 review B4: "never
// overwrite an already-initialized ceremony's own CA") meaningful here: it
// holds only because restoreState correctly reports StateOperational
// across the restart, not StateAwaitingInit.
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
		"state must be restored across the restart for ReplaceCA's guard below to mean anything")

	// The boot sequence re-wires the SAME underlying CA key material via
	// SetCA, exactly as it does on every boot.
	restarted.SetCA(initCA)
	require.Same(t, CeremonyCA(initCA), restarted.CA())

	// Simulate the restart's lifecycle wiring LATER trying to install the
	// (by-then-available) cluster CA provider via ReplaceCA.
	restarted.ReplaceCA(testCA(t))
	require.Same(t, CeremonyCA(initCA), restarted.CA(),
		"ReplaceCA must not touch an operational ceremony's own CA, even across a restart")

	csrPEM := generateTestCSRPEM(t, "post-restart-officer")
	cert, err := restarted.CA().SignCSR(context.Background(), csrPEM, &SignCSROptions{
		Username:    "officer1",
		TenantID:    tenantid.System,
		TrustDomain: testTrustDomain,
	})
	require.NoError(t, err)
	require.NotNil(t, cert)
	caCert, err := initCA.CACertificate(t.Context())
	require.NoError(t, err)
	require.NoError(t, cert.CheckSignatureFrom(caCert),
		"post-restart signing must still chain to the ceremony's own CA")
}
