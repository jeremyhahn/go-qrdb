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
