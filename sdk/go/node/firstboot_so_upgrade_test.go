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

package node

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"log/slog"
	"math/big"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/audit"
	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/firstboot"
	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
	"github.com/jeremyhahn/go-qrdb/pkg/secure"
	"github.com/jeremyhahn/go-qrdb/pkg/user"
)

// soUpgradeSigner signs a CSR as a system-tenant client certificate under
// its own CA, standing in for the ceremony CA of an earlier version.
type soUpgradeSigner struct {
	t    *testing.T
	cert *x509.Certificate
	key  *ecdsa.PrivateKey
}

func newSOUpgradeSigner(t *testing.T) *soUpgradeSigner {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "SO upgrade test CA"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	require.NoError(t, err)
	cert, err := x509.ParseCertificate(der)
	require.NoError(t, err)
	return &soUpgradeSigner{t: t, cert: cert, key: key}
}

// withSystemSO returns deps naming a separate system SO, enrolled from a CSR
// the test signer signs. Strict separation of duties -- forced under FIPS
// 140-3 -- refuses a first boot with no system SO, so the first-boot
// fixtures name one; the standard posture accepts it as well.
func withSystemSO(t *testing.T, deps firstboot.Dependencies) firstboot.Dependencies {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	csrDER, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{
		Subject: pkix.Name{CommonName: "system-so"},
	}, key)
	require.NoError(t, err)
	pin, err := secure.NewSecret(context.Background(), []byte("so-pin-246810"))
	require.NoError(t, err)
	deps.Signer = newSOUpgradeSigner(t)
	if deps.SOPINs == nil {
		deps.SOPINs, err = auth.NewSOAuthenticator(kvstore.NewMemoryKVStore(), audit.NewRingBuffer(64), slog.New(slog.DiscardHandler))
		require.NoError(t, err)
	}
	if deps.Binder == nil {
		users, ok := deps.Users.(user.UserServicer)
		require.True(t, ok, "the SO binder enrols into the first boot's own user store")
		deps.Binder = firstboot.UserCertBinder{Users: users}
	}
	deps.SO = &firstboot.SOConfig{
		Username: "system-so",
		CSRPEM:   pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: csrDER}),
		PIN:      pin,
	}
	return deps
}

func (s *soUpgradeSigner) SignAdminCSR(_ context.Context, tenantID, subject string, csrPEM []byte) ([]byte, error) {
	block, _ := pem.Decode(csrPEM)
	if block == nil {
		return nil, errors.New("no CSR")
	}
	csr, err := x509.ParseCertificateRequest(block.Bytes)
	if err != nil {
		return nil, err
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject:      pkix.Name{CommonName: subject, OrganizationalUnit: []string{tenantID}},
		NotBefore:    time.Now().Add(-time.Minute),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, s.cert, csr.PublicKey, s.key)
	if err != nil {
		return nil, err
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), nil
}

// TestNode_FirstBootWithCeremony_StrictUpgradeRecognisesEnrolledSO pins that
// an embedded deployment upgraded from a version that wrote neither the
// pending SO record nor its consumed marker -- with a separate system SO
// holding a live certificate binding and a committed PIN -- boots under
// strict separation of duties: the persisted enrolment is recognised, as
// the daemon recognises it, rather than read as no SO.
func TestNode_FirstBootWithCeremony_StrictUpgradeRecognisesEnrolledSO(t *testing.T) {
	ctx := context.Background()
	n := newPostureNode(t, true, audit.NewRingBuffer(64))
	svcs := n.Services()
	pins, err := auth.NewSOAuthenticator(kvstore.NewMemoryKVStore(), audit.NewRingBuffer(64), slog.New(slog.DiscardHandler))
	require.NoError(t, err)
	base := firstboot.Dependencies{
		Tenants: svcs.Tenant, Users: n.UserService(), Roles: svcs.Role, RoleResolver: svcs.Role,
		Logger: slog.New(slog.DiscardHandler), SOPINs: pins,
	}

	// The earlier version's first boot: the SO is enrolled directly, with
	// no ceremony hand-off record.
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	csrDER, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{
		Subject: pkix.Name{CommonName: "secofficer"},
	}, key)
	require.NoError(t, err)
	pin, err := secure.NewSecret(ctx, []byte("so-pin-246810"))
	require.NoError(t, err)
	first := base
	first.Signer = newSOUpgradeSigner(t)
	first.Binder = firstboot.UserCertBinder{Users: n.UserService()}
	first.SO = &firstboot.SOConfig{
		Username: "secofficer",
		CSRPEM:   pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: csrDER}),
		PIN:      pin,
	}
	result, err := n.FirstBoot(ctx, first)
	require.NoError(t, err)
	require.True(t, result.SOCreated)

	store := PendingAdminCertStore(svcs.KV)
	so, err := firstboot.PendingSystemSO(ctx, store)
	require.NoError(t, err)
	require.Nil(t, so, "the upgrade starts with no pending SO record")
	require.False(t, firstboot.SystemSOConsumed(ctx, store), "the upgrade starts with no consumed marker")

	_, err = n.FirstBootWithCeremony(ctx, store, nil, 0, base)
	var required *firstboot.SystemSORequiredError
	require.False(t, errors.As(err, &required), "an enrolled SO is not a missing SO: %v", err)
	require.NoError(t, err)
	require.True(t, firstboot.SystemSOConsumed(ctx, store), "the recognised enrolment is recorded")

	// A caller that says the SO is enrolled is never overridden.
	enrolled := base
	enrolled.SOEnrolled = true
	_, err = n.FirstBootWithCeremony(ctx, store, nil, 0, enrolled)
	require.NoError(t, err)
}
