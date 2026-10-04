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
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// byoTLSConfig returns a CeremonyConfig carrying the server-cert/client-CA
// material a BYO-TLS node's composer (cmd/qrdbd) threads in when it has no
// CeremonyCA (ceremonyCAFromBundle's (nil, nil) case: the operator's own
// leaf certificate and key are the server identity, not a CA this process
// holds the private key for).
func byoTLSConfig(serverCertPEM, clientCACertPEM []byte) *CeremonyConfig {
	cfg := defaultConfig()
	cfg.ServerCertPEM = serverCertPEM
	cfg.ClientCACertPEM = clientCACertPEM
	return cfg
}

// TestInitialize_BYOTLS_SPKIPinFromServerCert is the coordinator-decision
// proof: on a BYO-TLS node (Initialize called with a nil CeremonyCA), qrdb
// init does not self-issue a TLS certificate -- there is no CA to issue one
// from -- and instead returns the operator's own configured server
// certificate unchanged, with the SPKI pin computed from THAT certificate's
// public key rather than a CA's.
func TestInitialize_BYOTLS_SPKIPinFromServerCert(t *testing.T) {
	operatorCA := newMockCA(t)
	serverLeaf, err := operatorCA.IssueCertificate(context.Background(), &IssueCertRequest{
		CommonName:   "node1.example.com",
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		ValidityDays: 365,
	})
	require.NoError(t, err)
	clientCACertPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: operatorCA.certDER})

	adminLeaf, err := operatorCA.IssueCertificate(context.Background(), &IssueCertRequest{
		CommonName:         "admin",
		OrganizationalUnit: []string{"system", "administrator"},
		KeyUsage:           x509.KeyUsageDigitalSignature,
		ExtKeyUsage:        []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
		ValidityDays:       30,
	})
	require.NoError(t, err)

	cfg := byoTLSConfig(serverLeaf.CertPEM, clientCACertPEM)
	svc := newTestCeremonyService(t, cfg, testBarrier(t))

	result, err := svc.Initialize(context.Background(), nil, &InitializeOptions{
		SOPin:        "test-so-pin",
		UserPin:      "test-user-pin",
		AdminCertPEM: adminLeaf.CertPEM,
	})
	require.NoError(t, err)

	wantPin, err := computeSPKIPin(serverLeaf.Certificate)
	require.NoError(t, err)
	assert.Equal(t, wantPin, result.SPKIPin,
		"SPKI pin must come from the server's OWN leaf certificate, not a CA")
	assert.Equal(t, serverLeaf.CertPEM, result.TLSCertPEM,
		"TLSCertPEM must be the operator's own leaf, unchanged -- nothing is self-issued")
	assert.Equal(t, clientCACertPEM, result.CACertPEM,
		"CACertPEM must be the node's configured client-CA trust anchor")
	assert.Equal(t, AdminCredentialBYOC, result.AdminCredentialSource)
	assert.Equal(t, adminLeaf.CertPEM, result.AdminCertPEM)
}

// TestInitialize_BYOTLS_NoServerCertRefuses covers the failure half: a
// BYO-TLS node (nil CeremonyCA) whose composer never set
// CeremonyConfig.ServerCertPEM has nothing to compute a pin from and is
// refused with a typed error rather than panicking or silently returning
// an empty pin. The admin-credential selection itself must be a VALID BYOC
// certificate chaining to ClientCACertPEM, not a placeholder: Initialize
// validates admin-credential inputs before resolving the TLS certificate
// (go-qrdb#548 review: B3, "init is atomic"), so a bogus certificate would
// be refused there first and this test would never reach the check it is
// actually about.
func TestInitialize_BYOTLS_NoServerCertRefuses(t *testing.T) {
	operatorCA := newMockCA(t)
	clientCACertPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: operatorCA.certDER})
	adminLeaf, err := operatorCA.IssueCertificate(context.Background(), &IssueCertRequest{
		CommonName: "admin", OrganizationalUnit: []string{"system", "administrator"},
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
		ValidityDays: 30,
	})
	require.NoError(t, err)

	cfg := byoTLSConfig(nil, clientCACertPEM)
	svc := newTestCeremonyService(t, cfg, testBarrier(t))

	_, err = svc.Initialize(context.Background(), nil, &InitializeOptions{
		SOPin:        "test-so-pin",
		UserPin:      "test-user-pin",
		AdminCertPEM: adminLeaf.CertPEM,
	})
	var noCert *NoServerCertificateError
	require.ErrorAs(t, err, &noCert)
	assert.Contains(t, noCert.Error(), "no CeremonyCA configured and no server certificate available")
	assert.Nil(t, errors.Unwrap(noCert), "an absent (not malformed) ServerCertPEM carries no Cause")
}

// TestInitialize_BYOTLS_NoServerCertRefusesBeforeAnySideEffect is the
// go-qrdb#560 review item 8 regression: NoServerCertificateError was
// raised AFTER PIN provisioning and system-barrier initialization had
// already run (ca == nil's branch is reached only after both), so a
// misconfigured BYO-TLS node (no ServerCertPEM at all) left PINs
// provisioned and the barrier initialized on a call that reported
// failure. The check must run up front, before either side effect, so a
// refused call leaves the ceremony untouched and a corrected retry starts
// clean.
func TestInitialize_BYOTLS_NoServerCertRefusesBeforeAnySideEffect(t *testing.T) {
	operatorCA := newMockCA(t)
	clientCACertPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: operatorCA.certDER})
	adminLeaf, err := operatorCA.IssueCertificate(context.Background(), &IssueCertRequest{
		CommonName: "admin", OrganizationalUnit: []string{"system", "administrator"},
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
		ValidityDays: 30,
	})
	require.NoError(t, err)

	cfg := byoTLSConfig(nil, clientCACertPEM)
	barrier := testBarrier(t)
	svc := newTestCeremonyService(t, cfg, barrier)

	_, err = svc.Initialize(context.Background(), nil, &InitializeOptions{
		SOPin:        "test-so-pin",
		UserPin:      "test-user-pin",
		Secret:       []byte("test-master-password"),
		AdminCertPEM: adminLeaf.CertPEM,
	})
	var noCert *NoServerCertificateError
	require.ErrorAs(t, err, &noCert)

	assert.False(t, svc.pins.SOPINSet(), "the SO PIN must not be provisioned on a refused Initialize")
	assert.False(t, svc.pins.UserPINSet(), "the user PIN must not be provisioned on a refused Initialize")

	initialized, err := barrier.Initialized(context.Background())
	require.NoError(t, err)
	assert.False(t, initialized, "the system barrier must not be initialized on a refused Initialize")
}

// TestInitialize_BYOTLS_UnparseableServerCertRefuses covers the OTHER half
// of NoServerCertificateError: a BYO-TLS node whose ServerCertPEM is PRESENT
// but does not parse as a certificate is refused the same way, with the
// parse failure as its Cause -- distinct from the empty-ServerCertPEM case
// TestInitialize_BYOTLS_NoServerCertRefuses covers.
func TestInitialize_BYOTLS_UnparseableServerCertRefuses(t *testing.T) {
	operatorCA := newMockCA(t)
	clientCACertPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: operatorCA.certDER})
	adminLeaf, err := operatorCA.IssueCertificate(context.Background(), &IssueCertRequest{
		CommonName: "admin", OrganizationalUnit: []string{"system", "administrator"},
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
		ValidityDays: 30,
	})
	require.NoError(t, err)

	// A well-formed PEM CERTIFICATE block whose DER payload is garbage:
	// pem.Decode succeeds (block != nil) but x509.ParseCertificate fails,
	// reaching the Cause-wrapping branch rather than the empty-input one.
	malformed := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: []byte("not valid DER")})
	cfg := byoTLSConfig(malformed, clientCACertPEM)
	svc := newTestCeremonyService(t, cfg, testBarrier(t))

	_, err = svc.Initialize(context.Background(), nil, &InitializeOptions{
		SOPin:        "test-so-pin",
		UserPin:      "test-user-pin",
		AdminCertPEM: adminLeaf.CertPEM,
	})
	var noCert *NoServerCertificateError
	require.ErrorAs(t, err, &noCert)
	assert.Contains(t, noCert.Error(), "configured server certificate could not be parsed")
	assert.NotNil(t, errors.Unwrap(noCert))
}

// TestInitialize_BYOTLS_CSRSignRefused covers the coordinator decision that
// CSR-sign needs a CA that can sign, which a BYO-TLS node has none of: it is
// refused with a typed error naming BYOC as the supported path, not a
// nil-pointer panic calling ca.SignCSR on a nil CeremonyCA.
func TestInitialize_BYOTLS_CSRSignRefused(t *testing.T) {
	operatorCA := newMockCA(t)
	serverLeaf, err := operatorCA.IssueCertificate(context.Background(), &IssueCertRequest{
		CommonName: "node1", KeyUsage: x509.KeyUsageDigitalSignature, ValidityDays: 365,
	})
	require.NoError(t, err)

	cfg := byoTLSConfig(serverLeaf.CertPEM, nil)
	svc := newTestCeremonyService(t, cfg, testBarrier(t))

	csrPEM, _ := generateCSRPEM(t, "ignored-cn")
	_, err = svc.Initialize(context.Background(), nil, &InitializeOptions{
		SOPin:       "test-so-pin",
		UserPin:     "test-user-pin",
		AdminCSRPEM: csrPEM,
	})
	var needsCA *AdminCredentialRequiresCAError
	require.ErrorAs(t, err, &needsCA)
	assert.Equal(t, "csr-sign", needsCA.Path)
	assert.Contains(t, needsCA.Error(), "csr-sign admin-credential path requires a configured CeremonyCA")
}

// TestInitialize_BYOTLS_GenerateRefused covers the same decision for the
// default (generate) path: a BYO-TLS node refuses it too, for the same
// reason -- there is no CA to sign the generated key's CSR with.
func TestInitialize_BYOTLS_GenerateRefused(t *testing.T) {
	operatorCA := newMockCA(t)
	serverLeaf, err := operatorCA.IssueCertificate(context.Background(), &IssueCertRequest{
		CommonName: "node1", KeyUsage: x509.KeyUsageDigitalSignature, ValidityDays: 365,
	})
	require.NoError(t, err)

	cfg := byoTLSConfig(serverLeaf.CertPEM, nil)
	svc := newTestCeremonyService(t, cfg, testBarrier(t))

	_, err = svc.Initialize(context.Background(), nil, &InitializeOptions{
		SOPin:         "test-so-pin",
		UserPin:       "test-user-pin",
		AdminGenerate: true,
	})
	var needsCA *AdminCredentialRequiresCAError
	require.ErrorAs(t, err, &needsCA)
	assert.Equal(t, "generate", needsCA.Path)
}

// TestInitialize_BYOTLS_BYOCDefaultsToClientCACertPEM covers BYOC's pool
// fallback on a BYO-TLS node: with no explicit AdminCertPoolPEM, the
// admin certificate is validated against CeremonyConfig.ClientCACertPEM
// (the node's own configured client-auth trust anchor) rather than
// caBundlePEM (which only exists when a CeremonyCA issued one).
func TestInitialize_BYOTLS_BYOCDefaultsToClientCACertPEM(t *testing.T) {
	operatorCA := newMockCA(t)
	serverLeaf, err := operatorCA.IssueCertificate(context.Background(), &IssueCertRequest{
		CommonName: "node1", KeyUsage: x509.KeyUsageDigitalSignature, ValidityDays: 365,
	})
	require.NoError(t, err)
	clientCACertPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: operatorCA.certDER})

	adminLeaf, err := operatorCA.IssueCertificate(context.Background(), &IssueCertRequest{
		CommonName: "admin", OrganizationalUnit: []string{"system", "administrator"},
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
		ValidityDays: 30,
	})
	require.NoError(t, err)

	cfg := byoTLSConfig(serverLeaf.CertPEM, clientCACertPEM)
	svc := newTestCeremonyService(t, cfg, testBarrier(t))

	result, err := svc.Initialize(context.Background(), nil, &InitializeOptions{
		SOPin:        "test-so-pin",
		UserPin:      "test-user-pin",
		AdminCertPEM: adminLeaf.CertPEM,
	})
	require.NoError(t, err)
	assert.Equal(t, AdminCredentialBYOC, result.AdminCredentialSource)
	assert.Equal(t, adminLeaf.CertPEM, result.AdminCertPEM)
}

// TestInitialize_BYOTLS_BYOCUntrustedRefused proves the pool fallback is
// actually enforced: an admin certificate from an UNRELATED CA, not
// present in ClientCACertPEM, is rejected rather than silently adopted.
func TestInitialize_BYOTLS_BYOCUntrustedRefused(t *testing.T) {
	operatorCA := newMockCA(t)
	serverLeaf, err := operatorCA.IssueCertificate(context.Background(), &IssueCertRequest{
		CommonName: "node1", KeyUsage: x509.KeyUsageDigitalSignature, ValidityDays: 365,
	})
	require.NoError(t, err)
	clientCACertPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: operatorCA.certDER})

	unrelatedCA := newMockCA(t)
	rogueAdminLeaf, err := unrelatedCA.IssueCertificate(context.Background(), &IssueCertRequest{
		CommonName: "admin", KeyUsage: x509.KeyUsageDigitalSignature, ValidityDays: 30,
	})
	require.NoError(t, err)

	cfg := byoTLSConfig(serverLeaf.CertPEM, clientCACertPEM)
	svc := newTestCeremonyService(t, cfg, testBarrier(t))

	_, err = svc.Initialize(context.Background(), nil, &InitializeOptions{
		SOPin:        "test-so-pin",
		UserPin:      "test-user-pin",
		AdminCertPEM: rogueAdminLeaf.CertPEM,
	})
	var rejected *AdminCredentialRejectedError
	require.ErrorAs(t, err, &rejected)
}
