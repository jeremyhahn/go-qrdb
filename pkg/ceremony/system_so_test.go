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
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"io"
	"log/slog"
	"math/big"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/audit"
	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/spiffe"
	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
	"github.com/jeremyhahn/go-qrdb/pkg/pin/pintest"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
)

// The ceremony signs the system SO's CSR under the system tenant with the
// SO's own username and keeps only the PIN's hash record, which a fresh
// SOAuthenticator on another node commits and verifies.
func TestInitialize_SystemSO_CSR(t *testing.T) {
	svc := newTestCeremonyService(t, defaultConfig(), testBarrier(t))
	adminCSR, _ := generateCSRPEM(t, "ignored-cn")
	soCSR, _ := generateCSRPEM(t, "ignored-too")

	result, err := svc.Initialize(context.Background(), newMockCA(t), &InitializeOptions{
		SOPin:       "test-so-pin",
		UserPin:     "test-user-pin",
		AdminCSRPEM: adminCSR,
		SOUsername:  "secofficer",
		SOCSRPEM:    soCSR,
	})
	require.NoError(t, err)
	require.NotEmpty(t, result.SOCertPEM)

	block, _ := pem.Decode(result.SOCertPEM)
	require.NotNil(t, block)
	cert, err := x509.ParseCertificate(block.Bytes)
	require.NoError(t, err)
	assert.Equal(t, "secofficer", cert.Subject.CommonName)
	assert.Equal(t, []string{"spiffe://system/user/secofficer"}, uriStrings(cert))
	tenant, err := auth.TenantFromClientCert(cert, testTrustDomain)
	require.NoError(t, err)
	assert.Equal(t, "system", tenant)

	so := svc.LastIssuedSystemSO()
	require.NotNil(t, so)
	assert.Equal(t, "secofficer", so.Username)
	assert.Equal(t, result.SOCertPEM, so.CertPEM)
	assert.NotContains(t, so.PINRecord, "test-so-pin", "the record must not carry the PIN")

	// The copy is the caller's: mutating it leaves the ceremony's intact.
	so.CertPEM[0] ^= 0xff
	assert.Equal(t, result.SOCertPEM, svc.LastIssuedSystemSO().CertPEM)

	soAuth, err := auth.NewSOAuthenticator(kvstore.NewMemoryKVStore(), audit.NewRingBuffer(8), slog.New(slog.NewTextHandler(io.Discard, nil)))
	require.NoError(t, err)
	require.NoError(t, soAuth.CommitSOPINRecord(context.Background(), "system", so.PINRecord))
	require.NoError(t, soAuth.VerifySOPIN(context.Background(), "system", []byte("test-so-pin")))
}

// A presented system SO certificate that chains to the trust pool and
// names the system tenant is adopted unchanged.
func TestInitialize_SystemSO_PresentedCert(t *testing.T) {
	ca := newMockCA(t)
	poolPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: ca.certDER})
	presented := systemLeafPEM(t, ca, "secofficer", newDeviceSigner(t).Public())
	adminCSR, _ := generateCSRPEM(t, "ignored-cn")

	svc := newTestCeremonyService(t, defaultConfig(), testBarrier(t))
	result, err := svc.Initialize(context.Background(), ca, &InitializeOptions{
		SOPin:            "test-so-pin",
		UserPin:          "test-user-pin",
		AdminCSRPEM:      adminCSR,
		AdminCertPoolPEM: poolPEM,
		SOUsername:       "secofficer",
		SOCertPEM:        presented,
	})
	require.NoError(t, err)
	assert.Equal(t, presented, result.SOCertPEM)
	assert.Equal(t, presented, svc.LastIssuedSystemSO().CertPEM)

	// The adopted certificate logs in as the SO: MTLSAuthenticator names the
	// subject by its CN.
	block, _ := pem.Decode(presented)
	leaf, err := x509.ParseCertificate(block.Bytes)
	require.NoError(t, err)
	state := &tls.ConnectionState{PeerCertificates: []*x509.Certificate{leaf}, VerifiedChains: [][]*x509.Certificate{{leaf, ca.cert}}}
	id, err := auth.NewMTLSAuthenticatorWithTrustDomain(testTrustDomain).Authenticate(auth.WithTLSState(context.Background(), state))
	require.NoError(t, err)
	assert.Equal(t, "secofficer", id.Subject)
	assert.Equal(t, tenantid.System, id.TenantID)
}

// systemLeafPEM issues, from ca, a system-tenant client certificate for cn
// over pub.
func systemLeafPEM(t *testing.T, ca *mockCA, cn string, pub crypto.PublicKey) []byte {
	t.Helper()
	u, err := spiffe.ID{TrustDomain: tenantid.System, Kind: spiffe.KindUser, Name: cn}.URI()
	require.NoError(t, err)
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	require.NoError(t, err)
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: cn, OrganizationalUnit: []string{tenantid.System}},
		URIs:         []*url.URL{u},
		NotBefore:    time.Now().Add(-time.Minute),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca.cert, pub, ca.key)
	require.NoError(t, err)
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}

// TestInitialize_SystemSO_SharedKeyChangesNothing: an SO whose key is the
// admin's, whether by CSR or certificate, is refused before the PINs, the
// barrier or any certificate is committed.
func TestInitialize_SystemSO_SharedKeyChangesNothing(t *testing.T) {
	ca := newMockCA(t)
	poolPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: ca.certDER})
	sharedCSR, shared := generateCSRPEM(t, "secofficer")
	cases := map[string]InitializeOptions{
		"SO CSR, admin CSR":  {AdminCSRPEM: sharedCSR, SOCSRPEM: sharedCSR},
		"SO cert, admin CSR": {AdminCSRPEM: sharedCSR, SOCertPEM: systemLeafPEM(t, ca, "secofficer", shared.Public())},
		"SO CSR, admin cert": {AdminCertPEM: systemLeafPEM(t, ca, adminUsername, shared.Public()), SOCSRPEM: sharedCSR},
	}
	for name, opts := range cases {
		t.Run(name, func(t *testing.T) {
			barrier := testBarrier(t)
			svc := newTestCeremonyService(t, defaultConfig(), barrier)
			pins := pintest.NewBackend(t)
			svc.SetPINBackend(pins)
			opts.SOPin, opts.UserPin, opts.SOUsername, opts.AdminCertPoolPEM = "test-so-pin", "test-user-pin", "secofficer", poolPEM
			_, err := svc.Initialize(context.Background(), ca, &opts)
			var rejected *SystemSORejectedError
			require.ErrorAs(t, err, &rejected)
			assert.Contains(t, err.Error(), "key is the bootstrap admin's")
			assert.False(t, pins.IsInitialized(), "the PINs must stay uncommitted")
			initialized, err := barrier.Initialized(context.Background())
			require.NoError(t, err)
			assert.False(t, initialized, "the barrier must stay uninitialised")
			assert.Nil(t, svc.LastIssuedSystemSO())
			last, _ := svc.LastIssuedAdminCertPEM()
			assert.Nil(t, last)
		})
	}
}

// Naming no SO leaves the ceremony as it was: no SO certificate, no record.
func TestInitialize_SystemSO_Optional(t *testing.T) {
	svc := newTestCeremonyService(t, defaultConfig(), testBarrier(t))
	assert.Nil(t, svc.LastIssuedSystemSO())
	adminCSR, _ := generateCSRPEM(t, "ignored-cn")
	result, err := svc.Initialize(context.Background(), newMockCA(t), &InitializeOptions{
		SOPin: "test-so-pin", UserPin: "test-user-pin", AdminCSRPEM: adminCSR,
	})
	require.NoError(t, err)
	assert.Empty(t, result.SOCertPEM)
	assert.Nil(t, svc.LastIssuedSystemSO())
}

// TestInitialize_SystemSO_RequiredWhenStrict proves a strict node refuses a
// ceremony that names no system SO before anything is committed: the same
// service then initializes once the SO inputs are given.
func TestInitialize_SystemSO_RequiredWhenStrict(t *testing.T) {
	svc := newTestCeremonyService(t, defaultConfig(), testBarrier(t))
	svc.SetSeparationOfDuties(true)
	ca := newMockCA(t)
	adminCSR, _ := generateCSRPEM(t, "ignored-cn")

	_, err := svc.Initialize(context.Background(), ca, &InitializeOptions{
		SOPin: "test-so-pin", UserPin: "test-user-pin", AdminCSRPEM: adminCSR,
	})
	var rejected *SystemSORejectedError
	require.ErrorAs(t, err, &rejected)
	assert.Contains(t, err.Error(), "requires a system SO")
	assert.Nil(t, svc.LastIssuedSystemSO())

	soCSR, _ := generateCSRPEM(t, "ignored-too")
	result, err := svc.Initialize(context.Background(), ca, &InitializeOptions{
		SOPin: "test-so-pin", UserPin: "test-user-pin", AdminCSRPEM: adminCSR,
		SOUsername: "secofficer", SOCSRPEM: soCSR,
	})
	require.NoError(t, err, "the refused ceremony must have committed nothing")
	assert.NotEmpty(t, result.SOCertPEM)
}

// Every malformed system SO request is refused with a typed error before
// the ceremony writes anything, so the same service initializes cleanly
// afterwards.
func TestInitialize_SystemSO_Refusals(t *testing.T) {
	ca := newMockCA(t)
	otherCA := newMockCA(t)
	adminCSR, _ := generateCSRPEM(t, "ignored-cn")
	soCSR, _ := generateCSRPEM(t, "ignored-too")
	untrusted := adminLeafNamingTenantPEM(t, otherCA, "system")
	otherTenant := adminLeafNamingTenantPEM(t, ca, "acme")

	cases := map[string]struct {
		opts   InitializeOptions
		ca     CeremonyCA
		reason string
		want   func(error) bool
	}{
		"no username": {opts: InitializeOptions{SOCSRPEM: soCSR}, reason: "needs SOUsername"},
		"admin's username": {opts: InitializeOptions{SOUsername: "Admin", SOCSRPEM: soCSR},
			reason: "cannot be the bootstrap admin"},
		"no credential": {opts: InitializeOptions{SOUsername: "so"}, reason: "needs SOCSRPEM or SOCertPEM"},
		"both credentials": {opts: InitializeOptions{SOUsername: "so", SOCSRPEM: soCSR, SOCertPEM: untrusted},
			reason: "mutually exclusive"},
		"chain without cert": {opts: InitializeOptions{SOUsername: "so", SOCSRPEM: soCSR, SOChainPEM: untrusted},
			reason: "applies only to SOCertPEM"},
		"unparsable CSR":      {opts: InitializeOptions{SOUsername: "so", SOCSRPEM: []byte("not-a-csr")}},
		"untrusted cert":      {opts: InitializeOptions{SOUsername: "so", SOCertPEM: untrusted}},
		"tenant disagreement": {opts: InitializeOptions{SOUsername: "so", SOCertPEM: otherTenant}, reason: "disagree"},
		"CN is not SOUsername": {opts: InitializeOptions{SOUsername: "so", SOCertPEM: systemLeafPEM(t, ca, "someone-else", newDeviceSigner(t).Public())},
			reason: `subject CN "someone-else" is not SOUsername "so"`},
		"admin's key": {opts: InitializeOptions{SOUsername: "so", SOCSRPEM: adminCSR},
			reason: "key is the bootstrap admin's"},
		"no CA for the CSR": {opts: InitializeOptions{SOUsername: "so", SOCSRPEM: soCSR,
			AdminCertPEM: adminLeafPEM(t, ca, testTrustDomain), AdminCSRPEM: nil},
			ca: nilCA{}, want: func(err error) bool {
				var e *AdminCredentialRequiresCAError
				return errors.As(err, &e)
			}},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			svc := newTestCeremonyService(t, defaultConfig(), testBarrier(t))
			opts := tc.opts
			opts.SOPin, opts.UserPin = "test-so-pin", "test-user-pin"
			if opts.AdminCertPEM == nil {
				opts.AdminCSRPEM = adminCSR
			} else {
				opts.AdminCertPoolPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: ca.certDER})
			}
			var useCA CeremonyCA = ca
			if _, ok := tc.ca.(nilCA); ok {
				useCA = nil
			}
			_, err := svc.Initialize(context.Background(), useCA, &opts)
			require.Error(t, err)
			if tc.want != nil {
				assert.True(t, tc.want(err), "got %v", err)
				return
			}
			var rejected *SystemSORejectedError
			require.ErrorAs(t, err, &rejected)
			assert.True(t, strings.HasPrefix(err.Error(), "ceremony: system SO rejected: "), err.Error())
			assert.Contains(t, err.Error(), tc.reason)
			assert.Nil(t, svc.LastIssuedSystemSO())
		})
	}
}

// nilCA marks a case that runs the ceremony with no CA at all.
type nilCA struct{ CeremonyCA }

func TestSystemSORejectedError_Unwrap(t *testing.T) {
	cause := errors.New("cause")
	err := &SystemSORejectedError{Cause: cause}
	assert.ErrorIs(t, err, cause)
	assert.Equal(t, "ceremony: system SO rejected: cause", err.Error())
}

func TestSuppliedAdminKey_UnparsableIsNil(t *testing.T) {
	assert.Nil(t, suppliedAdminKey(&InitializeOptions{}))
	assert.Nil(t, suppliedAdminKey(&InitializeOptions{AdminCertPEM: []byte("-----BEGIN CERTIFICATE-----\nAAAA\n-----END CERTIFICATE-----\n")}))
	assert.Nil(t, suppliedAdminKey(&InitializeOptions{AdminCSRPEM: []byte("not-a-csr")}))
	var nilSO *IssuedSystemSO
	assert.Nil(t, nilSO.clone())
}
