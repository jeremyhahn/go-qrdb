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

//go:build !integration

package node

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"io"
	"log/slog"
	"math/big"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/ceremony"
	qrdbconfig "github.com/jeremyhahn/go-qrdb/pkg/config"
	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/firstboot"
	"github.com/jeremyhahn/go-qrdb/pkg/pin/pintest"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/server"
	"github.com/jeremyhahn/go-qrdb/sdk/go/testutil/qrdbdharness"
)

// ceremonyHandoffBarrier returns a real, unsealed *seal.StorageBarrier
// backed by an in-memory backend -- everything ceremony.CeremonyService
// needs to run a real Initialize, without any daemon-specific scaffolding.
func ceremonyHandoffBarrier(t *testing.T) *seal.StorageBarrier {
	t.Helper()
	strategy, err := seal.NewSoftwareStrategy(context.Background(), []byte("test-master-password"))
	require.NoError(t, err)
	b, err := seal.NewStorageBarrier(
		ceremonyHandoffLogger(),
		seal.NewMemoryBackend(),
		seal.BarrierConfig{RootKeyPath: "test/root-key"},
		strategy,
	)
	require.NoError(t, err)
	require.NoError(t, b.Initialize(context.Background(), seal.MustCredentials(context.Background(), []byte("test-master-password"))))
	return b
}

// ceremonyHandoffCA generates a self-signed ECDSA CA certificate and key,
// wrapped as a real ceremony.CeremonyCA (ceremony.LocalCA) -- the production
// CeremonyCA implementation, not a test double.
func ceremonyHandoffCA(t *testing.T) (ca *ceremony.LocalCA, certPEM []byte) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "ceremony-handoff test CA"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	require.NoError(t, err)
	certPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyDER, err := x509.MarshalECPrivateKey(key)
	require.NoError(t, err)
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	ca, err = ceremony.NewLocalCA(certPEM, keyPEM)
	require.NoError(t, err)
	return ca, certPEM
}

// TestNode_FirstBootWithCeremony_BoundAdminLogsInUnderRequired is the
// go-qrdb#548 parity proof for sdk/go/node: an embedder running a real
// ceremony, writing its issued admin credential to the durable hand-off,
// and running first boot through Node.FirstBootWithCeremony ends up with a
// bootstrap admin that can log in by certificate under the default
// auth.mtls_binding_policy "required" -- the SAME wiring
// (firstboot.ResolveAdminCertForFirstBoot / AdminCertPoolFromCABundle /
// UserCertBinder) cmd/qrdbd's own first boot uses, not a copy of it.
func TestNode_FirstBootWithCeremony_BoundAdminLogsInUnderRequired(t *testing.T) {
	ctx := context.Background()

	// A real ceremony, issuing the admin's credential via the generate path
	// (AdminGenerate, go-qrdb#548 review: there is no credential-less
	// default): the ceremony mints a key pair and signs it itself,
	// returning the key exactly once.
	ca, caCertPEM := ceremonyHandoffCA(t)
	cfg := &ceremony.CeremonyConfig{}
	svc, err := ceremony.NewCeremonyService(cfg, ceremonyHandoffBarrier(t), ceremonyHandoffLogger())
	require.NoError(t, err)
	svc.SetPINBackend(pintest.NewBackend(t))
	initResult, err := svc.Initialize(ctx, ca, &ceremony.InitializeOptions{
		SOPin: "test-so-pin", UserPin: "test-user-pin",
		Hostname: "localhost", SANs: []string{"localhost"}, AdminGenerate: true,
	})
	require.NoError(t, err)
	require.NotEmpty(t, initResult.AdminCertPEM)
	require.NotEmpty(t, initResult.AdminKeyPEM)

	// A fresh standalone REST node with no admin yet. The server's own TLS
	// identity comes from an UNRELATED CA (qrdbdharness) -- the point is
	// that the admin's certificate chains to the CEREMONY's CA via
	// ClientCAs, not that client and server share a root.
	certDir := t.TempDir()
	serverCA, err := qrdbdharness.NewCA(certDir)
	require.NoError(t, err)
	serverKP, err := serverCA.Issue(certDir, qrdbdharness.WithSubject("node", ""), qrdbdharness.WithServerAuth())
	require.NoError(t, err)
	serverCert, err := serverKP.TLSCertificate()
	require.NoError(t, err)

	clientCAs := x509.NewCertPool()
	require.True(t, clientCAs.AppendCertsFromPEM(caCertPEM))
	tlsCfg := &tls.Config{
		Certificates: []tls.Certificate{serverCert},
		ClientCAs:    clientCAs,
		ClientAuth:   tls.VerifyClientCertIfGiven,
		MinVersion:   tls.VersionTLS13,
	}

	nodeCfg := qrdbconfig.DefaultConfig()
	nodeCfg.Raft.DataDir = t.TempDir()
	nodeCfg.Databases = []qrdbconfig.DatabaseConfig{{Name: "default", Backend: string(database.BackendMemory)}}
	nodeCfg.Protocols.REST = true
	nodeCfg.Protocols.GRPC = false
	nodeCfg.Protocols.QUIC = false
	nodeCfg.Protocols.Unix = false
	nodeCfg.Protocols.MCP = false
	nodeCfg.Server.Host = "127.0.0.1"
	nodeCfg.Server.AdminPort = freeTCPPort(t)

	n, err := NewStandaloneNode(ctx, nodeCfg, []server.ServerOption{server.WithTLS(tlsCfg)})
	require.NoError(t, err)
	t.Cleanup(func() { _ = n.Close() })

	// The durable hand-off: write the ceremony-issued cert, then run first
	// boot through FirstBootWithCeremony -- the shared wiring under test.
	store := PendingAdminCertStore(n.Services().KV)
	require.NoError(t, firstboot.WritePendingAdminCert(ctx, store, initResult.AdminCertPEM))

	result, err := n.FirstBootWithCeremony(ctx, store, caCertPEM, 2*time.Second, firstboot.Dependencies{
		Tenants:      n.Services().Tenant,
		Users:        n.UserService(),
		Roles:        n.Services().Role,
		RoleResolver: n.Services().Role,
		Logger:       ceremonyHandoffLogger(),
	})
	require.NoError(t, err)
	require.True(t, result.AdminCreated)

	// The pending record is marked consumed once the binding commits, as a
	// one-way state transition -- never deleted (go-qrdb#560 review item
	// 8) -- so it stays readable, but a later node can never recreate it:
	// WritePendingAdminCert's create-if-absent race only ever has one
	// winner, permanently.
	consumed, getErr := store.Get(ctx, firstboot.PendingAdminCertKey)
	require.NoError(t, getErr, "the consumed record must remain readable, never deleted")
	require.NotEmpty(t, consumed)

	writeErr := firstboot.WritePendingAdminCert(ctx, store, []byte("late-arriving-cert-pem"))
	var alreadyPending *firstboot.AdminCredentialAlreadyPendingError
	require.ErrorAs(t, writeErr, &alreadyPending, "a consumed record must still refuse a later WritePendingAdminCert, exactly as a merely-pending one does")

	// The bound admin logs in by certificate under "required", over real
	// mTLS against the node's own REST listener.
	adminTLSCert, err := tls.X509KeyPair(initResult.AdminCertPEM, initResult.AdminKeyPEM)
	require.NoError(t, err)
	client := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{
		Certificates: []tls.Certificate{adminTLSCert},
		RootCAs:      serverCA.Pool,
		MinVersion:   tls.VersionTLS13,
	}}}
	resp, err := client.Get("https://" + n.Server().RESTAddr() + "/status")
	require.NoError(t, err)
	defer func() { _, _ = io.Copy(io.Discard, resp.Body); _ = resp.Body.Close() }()
	require.Equal(t, http.StatusOK, resp.StatusCode,
		"the ceremony-issued, FirstBootWithCeremony-bound admin certificate must authenticate")
}

// ceremonyHandoffLogger returns a quiet logger for the ceremony-handoff test
// fixtures.
func ceremonyHandoffLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
}
