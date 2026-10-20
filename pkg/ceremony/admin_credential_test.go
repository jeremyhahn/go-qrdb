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

	"github.com/jeremyhahn/go-qrdb/pkg/pin"
	"github.com/jeremyhahn/go-qrdb/pkg/pin/pintest"
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
		PublicKey:          newDeviceSigner(t).Public(),
		CommonName:         "admin",
		OrganizationalUnit: []string{"system", "administrator"},
		ValidityDays:       30,
		KeyUsage:           x509.KeyUsageDigitalSignature,
		ExtKeyUsage:        []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
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
		PublicKey:          newDeviceSigner(t).Public(),
		CommonName:         "admin",
		OrganizationalUnit: []string{"system", "administrator"},
		ValidityDays:       30,
		KeyUsage:           x509.KeyUsageDigitalSignature,
		ExtKeyUsage:        []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
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
		PublicKey:    newDeviceSigner(t).Public(),
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

// TestInitialize_AdminCredential_BYOC_ProfileChecklist covers go-qrdb#548
// review item 8's BYOC checklist: a presented certificate that chains to a
// trusted pool but fails any ONE of CN=admin, system-tenant-first-OU,
// no-holder-OU, or client-auth KeyUsage/ExtKeyUsage is refused BEFORE it is
// ever adopted -- chaining to a trusted root is necessary but not
// sufficient to stand for the bootstrap admin.
func TestInitialize_AdminCredential_BYOC_ProfileChecklist(t *testing.T) {
	cases := map[string]*IssueCertRequest{
		"wrong common name": {
			CommonName:         "not-admin",
			OrganizationalUnit: []string{"system", "administrator"},
			ValidityDays:       30,
			KeyUsage:           x509.KeyUsageDigitalSignature,
			ExtKeyUsage:        []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
		},
		"no organizational unit at all": {
			CommonName:   "admin",
			ValidityDays: 30,
			KeyUsage:     x509.KeyUsageDigitalSignature,
			ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
		},
		"not the system tenant": {
			CommonName:         "admin",
			OrganizationalUnit: []string{"some-other-tenant", "administrator"},
			ValidityDays:       30,
			KeyUsage:           x509.KeyUsageDigitalSignature,
			ExtKeyUsage:        []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
		},
		"holder OU (security officer)": {
			CommonName:         "admin",
			OrganizationalUnit: []string{"system", "security-officer"},
			ValidityDays:       30,
			KeyUsage:           x509.KeyUsageDigitalSignature,
			ExtKeyUsage:        []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
		},
		"holder OU (custodian)": {
			CommonName:         "admin",
			OrganizationalUnit: []string{"system", "custodian"},
			ValidityDays:       30,
			KeyUsage:           x509.KeyUsageDigitalSignature,
			ExtKeyUsage:        []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
		},
		"no digital signature key usage": {
			CommonName:         "admin",
			OrganizationalUnit: []string{"system", "administrator"},
			ValidityDays:       30,
			KeyUsage:           x509.KeyUsageKeyEncipherment,
			ExtKeyUsage:        []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
		},
		"no client auth ext key usage": {
			CommonName:         "admin",
			OrganizationalUnit: []string{"system", "administrator"},
			ValidityDays:       30,
			KeyUsage:           x509.KeyUsageDigitalSignature,
			ExtKeyUsage:        []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		},
	}
	for name, req := range cases {
		t.Run(name, func(t *testing.T) {
			ca := newMockCA(t)
			req.PublicKey = newDeviceSigner(t).Public()
			presented, err := ca.IssueCertificate(context.Background(), req)
			require.NoError(t, err)

			pool := x509.NewCertPool()
			pool.AddCert(ca.cert)
			poolPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: ca.certDER})

			svc := newTestCeremonyService(t, defaultConfig(), testBarrier(t))

			_, err = svc.Initialize(context.Background(), ca, &InitializeOptions{
				SOPin:            "test-so-pin",
				UserPin:          "test-user-pin",
				AdminCertPEM:     presented.CertPEM,
				AdminCertPoolPEM: poolPEM,
			})
			var rejected *AdminCredentialRejectedError
			require.ErrorAs(t, err, &rejected, "%s must be refused", name)
		})
	}
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
	assert.ErrorIs(t, err, ErrNotInitialized)
	assert.NotErrorIs(t, err, ErrNotInitializedOrEnrolling, "a reissue refusal is not a sign-csr refusal")
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

	second, err := svc.ReissueAdminCredential(context.Background(), &InitializeOptions{SOPin: "test-so-pin", AdminGenerate: true})
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

// TestReissueAdminCredential_BYOTLSBYOCBinds is the go-qrdb#560 re-review
// item 3 regression test: on a BYO-TLS node (Initialize called with a nil
// CeremonyCA, exactly as cmd/qrdbd and sdk/go/node wire a node with no
// cluster CA private key), ReissueAdminCredential used to refuse outright
// with ErrNotInitialized the instant deps.CA was nil -- regardless of
// state -- and would have nil-pointer panicked on deps.CA.CABundle() had
// that guard not caught it first. BYOC reissue needs no CeremonyCA at all
// (issueAdminCredential's own BYOC branch never reads ca), so the fix
// reuses resolveCABundlePEM exactly as Initialize does: a configured CA's
// bundle, or this node's configured ClientCACertPEM when ca is nil.
//
// Models the full scenario: BYO-TLS init with a first BYOC admin
// certificate, that certificate is lost (the "restart into awaiting
// admin credential" case firstboot's own tests cover at the orchestration
// layer -- this test isolates the ceremony-level bug beneath it), and a
// SO-PIN-gated reissue with a SECOND BYOC certificate must bind.
func TestReissueAdminCredential_BYOTLSBYOCBinds(t *testing.T) {
	operatorCA := newMockCA(t)
	serverLeaf, err := operatorCA.IssueCertificate(context.Background(), &IssueCertRequest{
		PublicKey:  newDeviceSigner(t).Public(),
		CommonName: "node1", KeyUsage: x509.KeyUsageDigitalSignature, ValidityDays: 365,
	})
	require.NoError(t, err)
	clientCACertPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: operatorCA.certDER})

	firstAdminLeaf, err := operatorCA.IssueCertificate(context.Background(), &IssueCertRequest{
		PublicKey:  newDeviceSigner(t).Public(),
		CommonName: "admin", OrganizationalUnit: []string{"system", "administrator"},
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
		ValidityDays: 30,
	})
	require.NoError(t, err)

	cfg := byoTLSConfig(serverLeaf.CertPEM, clientCACertPEM)
	svc := newTestCeremonyService(t, cfg, testBarrier(t))

	// BYO-TLS init via BYOC: ca is nil throughout, exactly as a real
	// BYO-TLS composition calls Initialize.
	first, err := svc.Initialize(context.Background(), nil, &InitializeOptions{
		SOPin: "test-so-pin", UserPin: "test-user-pin",
		AdminCertPEM: firstAdminLeaf.CertPEM,
	})
	require.NoError(t, err)
	require.Equal(t, AdminCredentialBYOC, first.AdminCredentialSource)

	// The first certificate is lost (expired, or simply never reached the
	// admin account's binding before a restart); a second BYOC certificate
	// is presented to the SO-PIN-gated reissue path instead of re-running
	// the whole ceremony.
	secondAdminLeaf, err := operatorCA.IssueCertificate(context.Background(), &IssueCertRequest{
		PublicKey:  newDeviceSigner(t).Public(),
		CommonName: "admin", OrganizationalUnit: []string{"system", "administrator"},
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
		ValidityDays: 30,
	})
	require.NoError(t, err)

	second, err := svc.ReissueAdminCredential(context.Background(), &InitializeOptions{
		SOPin:        "test-so-pin",
		AdminCertPEM: secondAdminLeaf.CertPEM,
	})
	require.NoError(t, err, "BYOC reissue on a BYO-TLS node (deps.CA == nil) must not refuse or panic")
	assert.Equal(t, AdminCredentialBYOC, second.AdminCredentialSource)
	assert.Equal(t, secondAdminLeaf.CertPEM, second.AdminCertPEM)
	assert.NotEqual(t, first.AdminCertPEM, second.AdminCertPEM, "reissue must bind the SECOND certificate, not replay the first")

	last, source := svc.LastIssuedAdminCertPEM()
	assert.Equal(t, second.AdminCertPEM, last)
	assert.Equal(t, AdminCredentialBYOC, source)
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
	result, err := svc.ReissueAdminCredential(context.Background(), &InitializeOptions{SOPin: "test-so-pin", AdminCSRPEM: csrPEM})
	require.NoError(t, err)
	assert.Empty(t, result.AdminKeyPEM)
	assert.Equal(t, AdminCredentialCSR, result.AdminCredentialSource)
}

// TestReissueAdminCredential_UnprovisionedBackendRefuses is the go-qrdb#560
// re-review's B1 fix: ReissueAdminCredential verifies the SO PIN through
// pin.RequireProvisionedSOPIN, not pin.RequireSOPIN. RequireSOPIN's own
// documented convenience -- an UNPROVISIONED backend admits any PIN,
// because "the node has no PIN to check yet, and provisioning one is the
// first administrative act" -- is correct for a FRESH ceremony but is never
// the right answer for a reissue: an already-initialized ceremony always
// provisioned its SO PIN during Initialize (validateInitOptions refuses an
// empty one), so a backend that reads as unprovisioned here can only mean
// something is wrong, and must refuse rather than silently agree with a
// caller who presented no PIN at all. Constructed by swapping in a fresh,
// never-provisioned backend after Initialize -- not a path production
// reaches, but the one RequireSOPIN-vs-RequireProvisionedSOPIN difference
// this call can only expose that way.
func TestReissueAdminCredential_UnprovisionedBackendRefuses(t *testing.T) {
	svc := newTestCeremonyService(t, defaultConfig(), testBarrier(t))

	_, err := svc.Initialize(context.Background(), newMockCA(t), &InitializeOptions{
		SOPin:         "test-so-pin",
		UserPin:       "test-user-pin",
		AdminGenerate: true,
	})
	require.NoError(t, err)

	svc.SetPINBackend(pintest.NewBackend(t))

	_, err = svc.ReissueAdminCredential(context.Background(), &InitializeOptions{SOPin: "whatever", AdminGenerate: true})
	assert.ErrorIs(t, err, pin.ErrPINNotSet)
}

// TestVerifyReissueSOPIN gates on the provisioned SO PIN alone: the PIN
// Initialize set is admitted, a wrong one is refused, and a backend that was
// never provisioned refuses even a PIN it has never seen.
func TestVerifyReissueSOPIN(t *testing.T) {
	svc := newTestCeremonyService(t, defaultConfig(), testBarrier(t))

	_, err := svc.Initialize(context.Background(), newMockCA(t), &InitializeOptions{
		SOPin:         "test-so-pin",
		UserPin:       "test-user-pin",
		AdminGenerate: true,
	})
	require.NoError(t, err)

	require.NoError(t, svc.VerifyReissueSOPIN(context.Background(), "test-so-pin"))
	assert.ErrorIs(t, svc.VerifyReissueSOPIN(context.Background(), "wrong-so-pin"), pin.ErrPINInvalid)

	svc.SetPINBackend(pintest.NewBackend(t))
	assert.ErrorIs(t, svc.VerifyReissueSOPIN(context.Background(), "test-so-pin"), pin.ErrPINNotSet)
}

// TestReissueAdminCredential_WrongPINRefusedBeforeAnyCredentialWork is the
// core-gate regression test the re-review asked for alongside the BYO-TLS
// fix above: the WRONG SO PIN against an ALREADY-provisioned ceremony must
// refuse before resolveCABundlePEM or issueAdminCredential ever run, not
// merely before an unprovisioned backend's different refusal (the existing
// TestReissueAdminCredential_UnprovisionedBackendRefuses). Proves the SO
// PIN gate and the credential-issuance path stay in that order on both
// compositions this call reaches -- a configured CA (generate) and
// BYO-TLS, CA nil (BYOC) -- so a caller presenting no valid PIN can reach
// neither.
func TestReissueAdminCredential_WrongPINRefusedBeforeAnyCredentialWork(t *testing.T) {
	t.Run("configured CA (generate)", func(t *testing.T) {
		svc := newTestCeremonyService(t, defaultConfig(), testBarrier(t))

		first, err := svc.Initialize(context.Background(), newMockCA(t), &InitializeOptions{
			SOPin:         "test-so-pin",
			UserPin:       "test-user-pin",
			AdminGenerate: true,
		})
		require.NoError(t, err)

		_, err = svc.ReissueAdminCredential(context.Background(), &InitializeOptions{
			SOPin:         "definitely-the-wrong-pin",
			AdminGenerate: true,
		})
		require.ErrorIs(t, err, pin.ErrPINInvalid)

		// The refusal must be pure: no fresh credential was minted, and
		// the certificate Initialize already issued is still the last
		// one recorded.
		last, source := svc.LastIssuedAdminCertPEM()
		assert.Equal(t, first.AdminCertPEM, last)
		assert.Equal(t, AdminCredentialGenerated, source)
	})

	t.Run("BYO-TLS, CA nil (BYOC)", func(t *testing.T) {
		operatorCA := newMockCA(t)
		serverLeaf, err := operatorCA.IssueCertificate(context.Background(), &IssueCertRequest{
			PublicKey:  newDeviceSigner(t).Public(),
			CommonName: "node1", KeyUsage: x509.KeyUsageDigitalSignature, ValidityDays: 365,
		})
		require.NoError(t, err)
		clientCACertPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: operatorCA.certDER})
		adminLeaf, err := operatorCA.IssueCertificate(context.Background(), &IssueCertRequest{
			PublicKey:  newDeviceSigner(t).Public(),
			CommonName: "admin", OrganizationalUnit: []string{"system", "administrator"},
			KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
			ValidityDays: 30,
		})
		require.NoError(t, err)

		svc := newTestCeremonyService(t, byoTLSConfig(serverLeaf.CertPEM, clientCACertPEM), testBarrier(t))
		first, err := svc.Initialize(context.Background(), nil, &InitializeOptions{
			SOPin: "test-so-pin", UserPin: "test-user-pin",
			AdminCertPEM: adminLeaf.CertPEM,
		})
		require.NoError(t, err)

		_, err = svc.ReissueAdminCredential(context.Background(), &InitializeOptions{
			SOPin:        "definitely-the-wrong-pin",
			AdminCertPEM: adminLeaf.CertPEM,
		})
		require.ErrorIs(t, err, pin.ErrPINInvalid)

		last, source := svc.LastIssuedAdminCertPEM()
		assert.Equal(t, first.AdminCertPEM, last)
		assert.Equal(t, AdminCredentialBYOC, source)
	})
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

// TestGenerateAdminCSR_ProducesAVerifiableCSRAndMatchingKey is the
// regression guard for wiping generateAdminCSR's intermediate keyDER
// (go-qrdb#560 review item 7): it must run strictly AFTER
// pem.EncodeToMemory has already copied those bytes into keyPEM's own PEM
// block, never before. Two independent calls are made to rule out any
// aliasing between them (e.g. a wipe reaching into the wrong call's
// buffer), and each call's own CSR must verify against its own key.
func TestGenerateAdminCSR_ProducesAVerifiableCSRAndMatchingKey(t *testing.T) {
	for i := 0; i < 2; i++ {
		csrPEM, keyPEM, err := generateAdminCSR()
		require.NoError(t, err)

		keyBlock, _ := pem.Decode(keyPEM)
		require.NotNil(t, keyBlock, "keyPEM must decode")
		priv, err := x509.ParseECPrivateKey(keyBlock.Bytes)
		require.NoError(t, err)

		csrBlock, _ := pem.Decode(csrPEM)
		require.NotNil(t, csrBlock, "csrPEM must decode")
		csr, err := x509.ParseCertificateRequest(csrBlock.Bytes)
		require.NoError(t, err)
		require.NoError(t, csr.CheckSignature(), "the CSR must verify against its own (unwiped) public key")

		assert.True(t, priv.PublicKey.Equal(csr.PublicKey), "keyPEM and csrPEM must describe the same key pair")
	}
}
