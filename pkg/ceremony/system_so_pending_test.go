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
	"bytes"
	"context"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"math/big"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/auth/spiffe"
	"github.com/jeremyhahn/go-qrdb/pkg/holderprofile"
	"github.com/jeremyhahn/go-qrdb/pkg/pin/pintest"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
)

// The system SO Initialize issues is recorded in the barrier, so a
// ceremony service started again over the same barrier still hands it to
// first boot, adopted trust pool included, until first boot forgets it.
func TestPendingSystemSO_SurvivesARestartUntilForgotten(t *testing.T) {
	ctx := context.Background()
	ca := newMockCA(t)
	poolPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: ca.certDER})
	presented := systemLeafPEM(t, ca, "secofficer", newDeviceSigner(t).Public())
	adminCSR, _ := generateCSRPEM(t, "ignored-cn")
	barrier := testBarrier(t)

	svc := newTestCeremonyService(t, defaultConfig(), barrier)
	_, err := svc.Initialize(ctx, ca, &InitializeOptions{
		SOPin:            "test-so-pin",
		UserPin:          "test-user-pin",
		AdminCSRPEM:      adminCSR,
		AdminCertPoolPEM: poolPEM,
		SOUsername:       "secofficer",
		SOCertPEM:        presented,
	})
	require.NoError(t, err)

	live, err := svc.PendingSystemSO(ctx)
	require.NoError(t, err)
	require.NotNil(t, live)
	assert.Equal(t, poolPEM, live.PoolPEM, "the adopted SO keeps the pool that validated it")
	live.PoolPEM[0] ^= 0xff
	again, err := svc.PendingSystemSO(ctx)
	require.NoError(t, err)
	assert.Equal(t, poolPEM, again.PoolPEM, "the caller's copy is its own")

	restarted := newTestCeremonyService(t, defaultConfig(), barrier)
	stored, err := restarted.PendingSystemSO(ctx)
	require.NoError(t, err)
	require.NotNil(t, stored)
	assert.Equal(t, "secofficer", stored.Username)
	assert.Equal(t, presented, stored.CertPEM)
	assert.Equal(t, poolPEM, stored.PoolPEM)
	assert.Equal(t, again.PINRecord, stored.PINRecord)

	require.NoError(t, restarted.ForgetPendingSystemSO(ctx))
	gone, err := restarted.PendingSystemSO(ctx)
	require.NoError(t, err)
	assert.Nil(t, gone)
	require.NoError(t, restarted.ForgetPendingSystemSO(ctx), "forgetting twice is not an error")

	require.NoError(t, barrier.Put(ctx, pendingSystemSOKey, nil))
	empty, err := restarted.PendingSystemSO(ctx)
	require.NoError(t, err)
	assert.Nil(t, empty, "an empty record names no SO")
}

func TestPendingSystemSO_ReportsUnreadableRecords(t *testing.T) {
	ctx := context.Background()
	barrier := testBarrier(t)
	svc := newTestCeremonyService(t, defaultConfig(), barrier)
	adminCSR, _ := generateCSRPEM(t, "ignored-cn")
	_, err := svc.Initialize(ctx, newMockCA(t), &InitializeOptions{SOPin: "test-so-pin", UserPin: "test-user-pin", AdminCSRPEM: adminCSR})
	require.NoError(t, err)

	restarted := newTestCeremonyService(t, defaultConfig(), barrier)
	none, err := restarted.PendingSystemSO(ctx)
	require.NoError(t, err)
	assert.Nil(t, none, "a ceremony with no SO records none")

	require.NoError(t, barrier.Put(ctx, pendingSystemSOKey, []byte("{not json")))
	_, err = restarted.PendingSystemSO(ctx)
	var persist *SystemSOPersistError
	require.ErrorAs(t, err, &persist)
	assert.Equal(t, "decode", persist.Op)
	assert.True(t, strings.HasPrefix(err.Error(), "ceremony: "), err.Error())
	assert.NotNil(t, errors.Unwrap(err))

	require.NoError(t, barrier.Seal())
	_, err = restarted.PendingSystemSO(ctx)
	require.ErrorAs(t, err, &persist)
	assert.Equal(t, "read", persist.Op)
	assert.ErrorIs(t, err, seal.ErrSealed)

	err = restarted.ForgetPendingSystemSO(ctx)
	require.ErrorAs(t, err, &persist)
	assert.Equal(t, "remove", persist.Op)
	assert.ErrorIs(t, err, seal.ErrSealed)
}

// An SO certificate the operator presents is checked against
// AdminCertPoolPEM when one is given: a pool with no certificate in it
// trusts nothing.
func TestInitialize_SystemSO_UnusablePoolRefused(t *testing.T) {
	ca := newMockCA(t)
	adminCSR, _ := generateCSRPEM(t, "ignored-cn")
	svc := newTestCeremonyService(t, defaultConfig(), testBarrier(t))
	_, err := svc.Initialize(context.Background(), ca, &InitializeOptions{
		SOPin:            "test-so-pin",
		UserPin:          "test-user-pin",
		AdminCSRPEM:      adminCSR,
		AdminCertPoolPEM: []byte("no certificates here"),
		SOUsername:       "secofficer",
		SOCertPEM:        systemLeafPEM(t, ca, "secofficer", newDeviceSigner(t).Public()),
	})
	var rejected *SystemSORejectedError
	require.ErrorAs(t, err, &rejected)
	assert.Contains(t, err.Error(), "no usable certificates")
	assert.Nil(t, svc.LastIssuedSystemSO())
}

// A CA that signs the admin's CSR but faults on the SO's leaves no SO
// behind for first boot.
func TestInitialize_SystemSO_SigningFaultIssuesNoSO(t *testing.T) {
	ctx := context.Background()
	adminCSR, _ := generateCSRPEM(t, "ignored-cn")
	soCSR, _ := generateCSRPEM(t, "ignored-too")
	svc := newTestCeremonyService(t, defaultConfig(), testBarrier(t))
	_, err := svc.Initialize(ctx, &failAfterNCA{CeremonyCA: newMockCA(t), n: 1}, &InitializeOptions{
		SOPin:       "test-so-pin",
		UserPin:     "test-user-pin",
		AdminCSRPEM: adminCSR,
		SOUsername:  "secofficer",
		SOCSRPEM:    soCSR,
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "simulated CA signing fault")
	assert.Nil(t, svc.LastIssuedSystemSO())
	pending, err := svc.PendingSystemSO(ctx)
	require.NoError(t, err)
	assert.Nil(t, pending)
}

// faultCA fails the TLS issuance or the CA certificate read Initialize
// makes after the SO inputs have passed preflight, or reads no CA
// certificate at all.
type faultCA struct {
	CeremonyCA
	issueErr, caCertErr error
	noCACert            bool
}

func (c faultCA) IssueCertificate(ctx context.Context, req *IssueCertRequest) (*IssuedCert, error) {
	if c.issueErr != nil {
		return nil, c.issueErr
	}
	return c.CeremonyCA.IssueCertificate(ctx, req)
}

func (c faultCA) CACertificate(ctx context.Context) (*x509.Certificate, error) {
	if c.caCertErr != nil || c.noCACert {
		return nil, c.caCertErr
	}
	return c.CeremonyCA.CACertificate(ctx)
}

// A ceremony that fails after the SO inputs passed preflight records no
// SO: first boot is never handed one the ceremony did not finish.
func TestInitialize_SystemSO_CAFaultsRecordNoSO(t *testing.T) {
	fault := errors.New("simulated CA fault")
	cases := map[string]struct {
		ca   faultCA
		want func(*testing.T, error)
	}{
		"TLS issuance": {ca: faultCA{issueErr: fault}, want: func(t *testing.T, err error) {
			assert.ErrorIs(t, err, ErrInitFailed)
		}},
		"CA certificate": {ca: faultCA{caCertErr: fault}, want: func(t *testing.T, err error) {
			var bundle *CABundleError
			assert.ErrorAs(t, err, &bundle)
			assert.ErrorIs(t, err, fault)
		}},
		"no CA certificate": {ca: faultCA{noCACert: true}, want: func(t *testing.T, err error) {
			var pin *SPKIPinError
			assert.ErrorAs(t, err, &pin)
		}},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			adminCSR, _ := generateCSRPEM(t, "ignored-cn")
			soCSR, _ := generateCSRPEM(t, "ignored-too")
			ca := tc.ca
			ca.CeremonyCA = newMockCA(t)
			svc := newTestCeremonyService(t, defaultConfig(), testBarrier(t))
			_, err := svc.Initialize(ctx, ca, &InitializeOptions{
				SOPin:       "test-so-pin",
				UserPin:     "test-user-pin",
				AdminCSRPEM: adminCSR,
				SOUsername:  "secofficer",
				SOCSRPEM:    soCSR,
			})
			require.Error(t, err)
			tc.want(t, err)
			assert.Nil(t, svc.LastIssuedSystemSO())
			pending, err := svc.PendingSystemSO(ctx)
			require.NoError(t, err)
			assert.Nil(t, pending)
		})
	}
}

// issueSystemSO re-checks a presented SO certificate itself rather than
// trusting that preflight ran, and a record the barrier cannot take is
// reported, not dropped.
func TestSystemSO_IssueAndPersistRefusals(t *testing.T) {
	ctx := context.Background()
	ca := newMockCA(t)
	so, err := issueSystemSO(ctx, ca, testTrustDomain, &InitializeOptions{
		SOPin:            "test-so-pin",
		SOUsername:       "secofficer",
		SOCertPEM:        systemLeafPEM(t, ca, "secofficer", newDeviceSigner(t).Public()),
		AdminCertPoolPEM: []byte("no certificates here"),
	}, nil)
	var rejected *SystemSORejectedError
	require.ErrorAs(t, err, &rejected)
	assert.Nil(t, so)

	barrier := testBarrier(t)
	svc := newTestCeremonyService(t, defaultConfig(), barrier)
	require.NoError(t, barrier.Seal())
	err = svc.persistIssuedSystemSOLocked(ctx, &IssuedSystemSO{Username: "secofficer"})
	var persist *SystemSOPersistError
	require.ErrorAs(t, err, &persist)
	assert.Equal(t, "write", persist.Op)
	assert.ErrorIs(t, err, seal.ErrSealed)
}

// tenantLeafPEM is systemLeafPEM for any tenant, with extraOUs after the
// tenant's; an empty tenant leaves the certificate without a SPIFFE
// identity.
func tenantLeafPEM(t *testing.T, ca *mockCA, tenant, cn string, extraOUs ...string) []byte {
	t.Helper()
	return tenantLeafPEMWithEKU(t, ca, tenant, cn, []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}, extraOUs...)
}

// tenantLeafPEMWithEKU is tenantLeafPEM with the extended key usages given.
func tenantLeafPEMWithEKU(t *testing.T, ca *mockCA, tenant, cn string, ekus []x509.ExtKeyUsage, extraOUs ...string) []byte {
	t.Helper()
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject:      pkix.Name{CommonName: cn},
		NotBefore:    time.Now().Add(-time.Minute),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  ekus,
	}
	if tenant != "" {
		u, err := spiffe.ID{TrustDomain: tenant, Kind: spiffe.KindUser, Name: cn}.URI()
		require.NoError(t, err)
		tmpl.Subject.OrganizationalUnit = append([]string{tenant}, extraOUs...)
		tmpl.URIs = []*url.URL{u}
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca.cert, newDeviceSigner(t).Public(), ca.key)
	require.NoError(t, err)
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}

// The SO certificate must name the system tenant: one naming another
// tenant, or none, is refused before any SO is recorded.
func TestSystemSO_AdoptRefusesANonSystemCertificate(t *testing.T) {
	ca := newMockCA(t)
	poolPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: ca.certDER})
	for name, certPEM := range map[string][]byte{
		"another tenant": tenantLeafPEM(t, ca, "acme", "secofficer"),
		"no tenant":      tenantLeafPEM(t, ca, "", "secofficer"),
	} {
		t.Run(name, func(t *testing.T) {
			cert, err := adoptSystemSOCert(&InitializeOptions{
				SOUsername:       "secofficer",
				SOCertPEM:        certPEM,
				AdminCertPoolPEM: poolPEM,
			}, nil, testTrustDomain)
			var rejected *SystemSORejectedError
			require.ErrorAs(t, err, &rejected)
			assert.Nil(t, cert)
		})
	}
}

// A share-holder certificate is a custody identity, never a login: RBAC
// refuses it every permission, so adopting one as the SO's login would
// enrol an SO that can do nothing. Both holder profiles are refused before
// anything is provisioned, and no SO record is left for first boot.
func TestInitialize_SystemSO_HolderCertificateRefused(t *testing.T) {
	for _, profile := range []string{holderprofile.SecurityOfficer, holderprofile.Custodian} {
		t.Run(profile, func(t *testing.T) {
			ctx := context.Background()
			ca := newMockCA(t)
			poolPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: ca.certDER})
			holderPEM := tenantLeafPEM(t, ca, tenantid.System, "secofficer", profile)

			cert, err := adoptSystemSOCert(&InitializeOptions{
				SOUsername:       "secofficer",
				SOCertPEM:        holderPEM,
				AdminCertPoolPEM: poolPEM,
			}, nil, testTrustDomain)
			var holder *SystemSOHolderCertError
			require.ErrorAs(t, err, &holder)
			assert.Equal(t, profile, holder.Profile)
			var rejected *SystemSORejectedError
			require.ErrorAs(t, err, &rejected)
			assert.Nil(t, cert)

			adminCSR, _ := generateCSRPEM(t, "ignored-cn")
			barrier := testBarrier(t)
			svc := newTestCeremonyService(t, defaultConfig(), barrier)
			_, err = svc.Initialize(ctx, ca, &InitializeOptions{
				SOPin:            "test-so-pin",
				UserPin:          "test-user-pin",
				AdminCSRPEM:      adminCSR,
				AdminCertPoolPEM: poolPEM,
				SOUsername:       "secofficer",
				SOCertPEM:        holderPEM,
			})
			require.ErrorAs(t, err, &holder)
			assert.Equal(t, StateAwaitingInit, svc.State())
			assert.Nil(t, svc.LastIssuedSystemSO())
			initialized, err := barrier.Initialized(ctx)
			require.NoError(t, err)
			assert.False(t, initialized, "a refused SO certificate commits nothing")
		})
	}
}

// TestInitialize_SystemSO_ServerOnlyCertificateRefused pins that an adopted
// SO certificate must be able to authenticate as a TLS client: a trusted
// certificate with the right CN and tenant but only the ServerAuth EKU is
// refused before anything is committed, and the ceremony stays retryable.
func TestInitialize_SystemSO_ServerOnlyCertificateRefused(t *testing.T) {
	ctx := context.Background()
	ca := newMockCA(t)
	poolPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: ca.certDER})
	serverPEM := tenantLeafPEMWithEKU(t, ca, tenantid.System, "secofficer", []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth})

	cert, err := adoptSystemSOCert(&InitializeOptions{
		SOUsername:       "secofficer",
		SOCertPEM:        serverPEM,
		AdminCertPoolPEM: poolPEM,
	}, nil, testTrustDomain)
	var profile *ClientAuthProfileError
	require.ErrorAs(t, err, &profile)
	var rejected *SystemSORejectedError
	require.ErrorAs(t, err, &rejected)
	assert.Nil(t, cert)

	adminCSR, _ := generateCSRPEM(t, "ignored-cn")
	barrier := testBarrier(t)
	svc := newTestCeremonyService(t, defaultConfig(), barrier)
	opts := func(soCert []byte) *InitializeOptions {
		return &InitializeOptions{
			SOPin:            "test-so-pin",
			UserPin:          "test-user-pin",
			AdminCSRPEM:      adminCSR,
			AdminCertPoolPEM: poolPEM,
			SOUsername:       "secofficer",
			SOCertPEM:        soCert,
		}
	}
	_, err = svc.Initialize(ctx, ca, opts(serverPEM))
	require.ErrorAs(t, err, &profile)
	assert.Equal(t, StateAwaitingInit, svc.State())
	assert.Nil(t, svc.LastIssuedSystemSO())
	initialized, err := barrier.Initialized(ctx)
	require.NoError(t, err)
	assert.False(t, initialized, "a refused SO certificate commits nothing")

	// The same ceremony with a client certificate completes.
	_, err = svc.Initialize(ctx, ca, opts(tenantLeafPEM(t, ca, tenantid.System, "secofficer")))
	require.NoError(t, err)
	assert.Equal(t, StateOperational, svc.State())
	require.NotNil(t, svc.LastIssuedSystemSO())
}

// An SO named without a PIN is refused in preflight, and a certificate
// that is not PEM parses to nothing.
func TestSystemSO_PINlessSOAndNonPEMCertificate(t *testing.T) {
	soCSR, _ := generateCSRPEM(t, "ignored-too")
	err := validateSystemSOInputs(newMockCA(t), testTrustDomain, &InitializeOptions{
		SOUsername: "secofficer",
		SOCSRPEM:   soCSR,
	}, nil)
	var rejected *SystemSORejectedError
	require.ErrorAs(t, err, &rejected)
	assert.Contains(t, err.Error(), "SOPin")

	assert.Nil(t, parsePEMCert([]byte("not a certificate")))
}

// failSOSignCA faults signing one CSR, the SO's, and signs every other.
type failSOSignCA struct {
	CeremonyCA
	soCSR []byte
}

func (c *failSOSignCA) SignCSR(ctx context.Context, csrPEM []byte, opts *SignCSROptions) (*x509.Certificate, error) {
	if bytes.Equal(csrPEM, c.soCSR) {
		return nil, errors.New("simulated SO signing fault")
	}
	return c.CeremonyCA.SignCSR(ctx, csrPEM, opts)
}

// A single-admin ceremony whose SO signing faults must not leave the
// system barrier initialized without the SO record: the node restarts into
// the sealed window again, and a retry completes the ceremony with the SO
// recorded for first boot.
func TestInitialize_SystemSO_SigningFaultIsRetriableAcrossRestart(t *testing.T) {
	ctx := context.Background()
	backend := seal.NewMemoryBackend()
	pins := pintest.NewBackend(t)
	start := func() (*CeremonyService, *seal.StorageBarrier) {
		barrier, err := seal.NewStorageBarrier(testLogger(), backend, seal.BarrierConfig{RootKeyPath: "test/root-key"}, seal.NewNoneStrategy())
		require.NoError(t, err)
		svc, err := NewCeremonyService(defaultConfig(), barrier, testLogger())
		require.NoError(t, err)
		svc.SetPINBackend(pins)
		svc.SetTrustDomain(testTrustDomain)
		return svc, barrier
	}
	adminCSR, _ := generateCSRPEM(t, "ignored-cn")
	soCSR, _ := generateCSRPEM(t, "ignored-too")
	opts := func() *InitializeOptions {
		return &InitializeOptions{
			SOPin:       "test-so-pin",
			UserPin:     "test-user-pin",
			AdminCSRPEM: adminCSR,
			SOUsername:  "secofficer",
			SOCSRPEM:    soCSR,
		}
	}
	ca := newMockCA(t)

	svc, failed := start()
	_, err := svc.Initialize(ctx, &failSOSignCA{CeremonyCA: ca, soCSR: soCSR}, opts())
	require.ErrorContains(t, err, "simulated SO signing fault")
	assert.Equal(t, StateAwaitingInit, svc.State(), "a failed SO issuance leaves the ceremony open")
	// The composer serves the initialization listener, rather than auto-
	// unsealing into first boot, only while the barrier is uninitialized.
	assert.True(t, failed.IsSealed(), "the barrier is not unsealed for a ceremony that failed")
	initialized, err := failed.Initialized(ctx)
	require.NoError(t, err)
	assert.False(t, initialized, "the barrier is not initialized for a ceremony that failed")

	restarted, barrier := start()
	initialized, err = barrier.Initialized(ctx)
	require.NoError(t, err)
	assert.False(t, initialized, "the restarted node finds no initialized barrier and serves the initialization window again")
	assert.Equal(t, StateAwaitingInit, restarted.State())
	result, err := restarted.Initialize(ctx, ca, opts())
	require.NoError(t, err, "the retried ceremony completes")
	assert.Equal(t, StateOperational, result.State)
	assert.NotEmpty(t, result.SOCertPEM)
	assert.False(t, barrier.IsSealed())
	pending, err := restarted.PendingSystemSO(ctx)
	require.NoError(t, err)
	require.NotNil(t, pending, "first boot is handed the SO")
	assert.Equal(t, "secofficer", pending.Username)
}
