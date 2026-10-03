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
	"crypto/x509"
	"encoding/pem"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestInitialize_AdminCredential_Generate covers the default path (go-qrdb#548):
// neither AdminCSRPEM nor AdminCertPEM supplied, so a key pair is generated
// in-process and signed. The key is returned exactly once in InitResult.
func TestInitialize_AdminCredential_Generate(t *testing.T) {
	svc := newTestCeremonyService(t, defaultConfig(), testBarrier(t))

	result, err := svc.Initialize(context.Background(), newMockCA(t), &InitializeOptions{
		SOPin:         "test-so-pin",
		UserPin:       "test-user-pin",
		AdminGenerate: true,
	})
	require.NoError(t, err)
	assert.NotEmpty(t, result.AdminCertPEM)
	assert.NotEmpty(t, result.AdminKeyPEM)
	assert.Equal(t, AdminCredentialGenerated, result.AdminCredentialSource)

	last, source := svc.LastIssuedAdminCertPEM()
	assert.Equal(t, result.AdminCertPEM, last)
	assert.Equal(t, AdminCredentialGenerated, source)

	result.WipeAdminKeyPEM()
	assert.Nil(t, result.AdminKeyPEM)
}

// TestInitialize_AdminCredential_CSR covers the CSR-sign path: the ceremony CA
// signs the operator-supplied CSR and the admin's own key never leaves the
// caller -- no key is returned.
func TestInitialize_AdminCredential_CSR(t *testing.T) {
	svc := newTestCeremonyService(t, defaultConfig(), testBarrier(t))

	csrPEM, _ := generateCSRPEM(t, "ignored-cn")
	result, err := svc.Initialize(context.Background(), newMockCA(t), &InitializeOptions{
		SOPin:       "test-so-pin",
		UserPin:     "test-user-pin",
		AdminCSRPEM: csrPEM,
	})
	require.NoError(t, err)
	assert.NotEmpty(t, result.AdminCertPEM)
	assert.Empty(t, result.AdminKeyPEM)
	assert.Equal(t, AdminCredentialCSR, result.AdminCredentialSource)

	block, _ := pem.Decode(result.AdminCertPEM)
	require.NotNil(t, block)
	cert, err := x509.ParseCertificate(block.Bytes)
	require.NoError(t, err)
	assert.Equal(t, "admin", cert.Subject.CommonName)
	assert.Equal(t, []string{"system", "administrator"}, cert.Subject.OrganizationalUnit)
}

// TestInitialize_AdminCredential_BYOC covers bring-your-own-certificate: a
// certificate the admin already holds is validated against the given pool
// and adopted unchanged -- nothing is signed, no key is generated.
func TestInitialize_AdminCredential_BYOC(t *testing.T) {
	ca := newMockCA(t)
	presented, err := ca.IssueCertificate(context.Background(), &IssueCertRequest{
		CommonName:   "admin",
		ValidityDays: 30,
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	})
	require.NoError(t, err)

	pool := x509.NewCertPool()
	pool.AddCert(ca.cert)
	poolPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: ca.certDER})

	svc := newTestCeremonyService(t, defaultConfig(), testBarrier(t))

	result, err := svc.Initialize(context.Background(), ca, &InitializeOptions{
		SOPin:            "test-so-pin",
		UserPin:          "test-user-pin",
		AdminCertPEM:     presented.CertPEM,
		AdminCertPoolPEM: poolPEM,
	})
	require.NoError(t, err)
	assert.Equal(t, presented.CertPEM, result.AdminCertPEM)
	assert.Empty(t, result.AdminKeyPEM)
	assert.Equal(t, AdminCredentialBYOC, result.AdminCredentialSource)
}

// TestInitialize_AdminCredential_BYOC_DefaultsToClusterCA covers BYOC with no
// explicit pool: the certificate is validated against the ceremony's own CA
// bundle instead (docs/firstboot/user.md).
func TestInitialize_AdminCredential_BYOC_DefaultsToClusterCA(t *testing.T) {
	ca := newMockCA(t)
	presented, err := ca.IssueCertificate(context.Background(), &IssueCertRequest{
		CommonName:   "admin",
		ValidityDays: 30,
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	})
	require.NoError(t, err)

	svc := newTestCeremonyService(t, defaultConfig(), testBarrier(t))

	result, err := svc.Initialize(context.Background(), ca, &InitializeOptions{
		SOPin:        "test-so-pin",
		UserPin:      "test-user-pin",
		AdminCertPEM: presented.CertPEM,
	})
	require.NoError(t, err)
	assert.Equal(t, presented.CertPEM, result.AdminCertPEM)
	assert.Equal(t, AdminCredentialBYOC, result.AdminCredentialSource)
}

// TestInitialize_AdminCredential_BYOC_Untrusted covers a presented certificate
// that does not chain to the given pool: rejected rather than adopted.
func TestInitialize_AdminCredential_BYOC_Untrusted(t *testing.T) {
	foreign := newMockCA(t)
	presented, err := foreign.IssueCertificate(context.Background(), &IssueCertRequest{
		CommonName:   "admin",
		ValidityDays: 30,
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	})
	require.NoError(t, err)

	svc := newTestCeremonyService(t, defaultConfig(), testBarrier(t))

	// The ceremony's own CA is a DIFFERENT CA than the one that issued
	// presented, so the default (cluster-CA) pool must reject it.
	_, err = svc.Initialize(context.Background(), newMockCA(t), &InitializeOptions{
		SOPin:        "test-so-pin",
		UserPin:      "test-user-pin",
		AdminCertPEM: presented.CertPEM,
	})
	var rejected *AdminCredentialRejectedError
	require.ErrorAs(t, err, &rejected)
}

// TestInitialize_AdminCredential_ConflictingInputs covers both AdminCSRPEM
// and AdminCertPEM supplied together: refused, neither path is guessed.
func TestInitialize_AdminCredential_ConflictingInputs(t *testing.T) {
	svc := newTestCeremonyService(t, defaultConfig(), testBarrier(t))

	csrPEM, _ := generateCSRPEM(t, "admin")
	_, err := svc.Initialize(context.Background(), newMockCA(t), &InitializeOptions{
		SOPin:        "test-so-pin",
		UserPin:      "test-user-pin",
		AdminCSRPEM:  csrPEM,
		AdminCertPEM: []byte("not used"),
	})
	var conflict *ConflictingAdminCredentialInputsError
	require.ErrorAs(t, err, &conflict)
}

// TestLastIssuedAdminCertPEM_BeforeInitialize covers the zero-value case:
// nothing issued yet.
func TestLastIssuedAdminCertPEM_BeforeInitialize(t *testing.T) {
	svc := newTestCeremonyService(t, defaultConfig(), testBarrier(t))

	certPEM, source := svc.LastIssuedAdminCertPEM()
	assert.Nil(t, certPEM)
	assert.Equal(t, AdminCredentialNone, source)
}

// TestReissueAdminCredential_NotInitialized covers calling the retry path
// before Initialize has ever run.
func TestReissueAdminCredential_NotInitialized(t *testing.T) {
	svc := newTestCeremonyService(t, defaultConfig(), testBarrier(t))

	_, err := svc.ReissueAdminCredential(context.Background(), nil)
	assert.ErrorIs(t, err, ErrNotInitializedOrEnrolling)
}

// TestReissueAdminCredential_Succeeds covers the recovery path (go-qrdb#548):
// re-running the admin-credential step on an already-initialized ceremony
// issues a FRESH credential (a new generated key, here) and updates
// LastIssuedAdminCertPEM, without touching the barrier or PIN state.
func TestReissueAdminCredential_Succeeds(t *testing.T) {
	svc := newTestCeremonyService(t, defaultConfig(), testBarrier(t))

	first, err := svc.Initialize(context.Background(), newMockCA(t), &InitializeOptions{
		SOPin:         "test-so-pin",
		UserPin:       "test-user-pin",
		AdminGenerate: true,
	})
	require.NoError(t, err)

	second, err := svc.ReissueAdminCredential(context.Background(), &InitializeOptions{AdminGenerate: true})
	require.NoError(t, err)
	assert.NotEmpty(t, second.AdminCertPEM)
	assert.NotEmpty(t, second.AdminKeyPEM)
	assert.NotEqual(t, first.AdminCertPEM, second.AdminCertPEM, "reissue must mint a fresh certificate, not replay the first")

	last, source := svc.LastIssuedAdminCertPEM()
	assert.Equal(t, second.AdminCertPEM, last)
	assert.Equal(t, AdminCredentialGenerated, source)

	// The ceremony itself stays operational; reissue does not reset state.
	assert.Equal(t, StateOperational, svc.State())
}

// TestReissueAdminCredential_HonoursCSRPath covers that reissue applies the
// SAME path resolution as Initialize: supplying a CSR selects CSR-sign.
func TestReissueAdminCredential_HonoursCSRPath(t *testing.T) {
	svc := newTestCeremonyService(t, defaultConfig(), testBarrier(t))

	_, err := svc.Initialize(context.Background(), newMockCA(t), &InitializeOptions{
		SOPin:         "test-so-pin",
		UserPin:       "test-user-pin",
		AdminGenerate: true,
	})
	require.NoError(t, err)

	csrPEM, _ := generateCSRPEM(t, "ignored-cn")
	result, err := svc.ReissueAdminCredential(context.Background(), &InitializeOptions{AdminCSRPEM: csrPEM})
	require.NoError(t, err)
	assert.Empty(t, result.AdminKeyPEM)
	assert.Equal(t, AdminCredentialCSR, result.AdminCredentialSource)
}

// TestCACertPool_BeforeInitialize covers the not-yet-initialized case.
func TestCACertPool_BeforeInitialize(t *testing.T) {
	svc := newTestCeremonyService(t, defaultConfig(), testBarrier(t))

	_, err := svc.CACertPool()
	assert.ErrorIs(t, err, ErrCANotInitialized)
}

// TestCACertPool_AfterInitialize covers the normal case: a pool trusting the
// ceremony's own CA, used by firstboot to validate the admin certificate
// before binding it.
func TestCACertPool_AfterInitialize(t *testing.T) {
	ca := newMockCA(t)
	svc := newTestCeremonyService(t, defaultConfig(), testBarrier(t))

	_, err := svc.Initialize(context.Background(), ca, &InitializeOptions{
		SOPin:         "test-so-pin",
		UserPin:       "test-user-pin",
		AdminGenerate: true,
	})
	require.NoError(t, err)

	pool, err := svc.CACertPool()
	require.NoError(t, err)

	last, _ := svc.LastIssuedAdminCertPEM()
	block, _ := pem.Decode(last)
	require.NotNil(t, block)
	cert, err := x509.ParseCertificate(block.Bytes)
	require.NoError(t, err)
	_, err = cert.Verify(x509.VerifyOptions{Roots: pool, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageAny}})
	assert.NoError(t, err)
}

// TestWipeAdminKeyPEM_NilSafe covers the nil-receiver and empty-key no-ops.
func TestWipeAdminKeyPEM_NilSafe(t *testing.T) {
	var r *InitResult
	r.WipeAdminKeyPEM() // must not panic

	r2 := &InitResult{}
	r2.WipeAdminKeyPEM()
	assert.Nil(t, r2.AdminKeyPEM)
}
