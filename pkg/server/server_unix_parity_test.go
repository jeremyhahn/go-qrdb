// Copyright 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package server

import (
	"context"
	"crypto/tls"
	"path/filepath"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	authmw "github.com/jeremyhahn/go-qrdb/pkg/auth/middleware"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
	unixtransport "github.com/jeremyhahn/go-qrdb/sdk/go/transport/unix"
)

// TestStart_UnixOnly_TenantBarrierServiceReachable proves the unix
// (gRPC-over-UDS) server registers TenantBarrierService whenever a tenant
// servicer is configured, closing the parity gap where "qrdb tenant
// barrier-init" over the default (unix) transport failed with "unknown
// service qrdb.v1.tenantbarrierservice" even though REST/gRPC/QUIC exposed
// it.
func TestStart_UnixOnly_TenantBarrierServiceReachable(t *testing.T) {
	socketPath := filepath.Join(t.TempDir(), "qrdb.sock")
	cfg := testConfigFull(t, protocolFlags{unix: true}, socketPath)
	svc := &mockTenantServicer{}
	srv := New(cfg, newMockKVService(), &mockAdminService{}, WithTenantService(svc), WithCeremonyCA(testServerCA))

	if err := srv.Start(); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer srv.Stop()

	tr, err := unixtransport.New(transport.WithAddress(srv.UnixAddr()), testAutoProvisionClientTLSOption(t))
	if err != nil {
		t.Fatalf("unix transport New: %v", err)
	}
	if err := tr.Connect(context.Background()); err != nil {
		t.Fatalf("unix transport Connect: %v", err)
	}
	defer tr.Close()

	resp, err := tr.TenantBarrierStatus(context.Background(), &transport.TenantBarrierStatusRequest{TenantID: "acme"})
	if err != nil {
		t.Fatalf("TenantBarrierStatus over unix: %v (want the RPC to be registered and reach the mock servicer)", err)
	}
	if resp.TenantID != "acme" {
		t.Errorf("TenantID = %q, want %q", resp.TenantID, "acme")
	}
}

// TestStart_UnixOnly_EncryptionMigratorReachable proves the unix
// (gRPC-over-UDS) server wires the enable-encryption migrator whenever one
// is configured, so BarrierEnableEncryption over the default (unix)
// transport behaves the same as REST/gRPC instead of always returning
// "encryption-migration: not configured".
func TestStart_UnixOnly_EncryptionMigratorReachable(t *testing.T) {
	socketPath := filepath.Join(t.TempDir(), "qrdb.sock")
	cfg := testConfigFull(t, protocolFlags{unix: true}, socketPath)

	invoked := make(chan service.BarrierScope, 1)
	migrator := encMigratorFunc(func(_ context.Context, scope service.BarrierScope, _ seal.MigrationOptions) error {
		invoked <- scope
		return nil
	})

	srv := New(cfg, newMockKVService(), &mockAdminService{}, WithEncryptionMigrator(migrator), WithCeremonyCA(testServerCA))

	if err := srv.Start(); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer srv.Stop()

	tr, err := unixtransport.New(transport.WithAddress(srv.UnixAddr()), testAutoProvisionClientTLSOption(t))
	if err != nil {
		t.Fatalf("unix transport New: %v", err)
	}
	if err := tr.Connect(context.Background()); err != nil {
		t.Fatalf("unix transport Connect: %v", err)
	}
	defer tr.Close()

	resp, err := tr.BarrierEnableEncryption(context.Background(), &transport.BarrierEnableEncryptionRequest{
		Strategy:   "passphrase",
		Passphrase: "pw",
	})
	if err != nil {
		t.Fatalf("BarrierEnableEncryption over unix: %v (migrator not reachable)", err)
	}
	if resp.Status != "migration_started" {
		t.Errorf("Status = %q, want %q", resp.Status, "migration_started")
	}

	// The migration runs asynchronously after a flush grace; wait for it so
	// the assertion is not vacuous.
	select {
	case scope := <-invoked:
		if scope.Kind != service.ScopeSystem {
			t.Errorf("scope kind = %q, want system", scope.Kind)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("encryption migrator was not invoked within 3s")
	}
}

// TestStart_UnixOnly_RequiresTLS proves the unix (gRPC-over-UDS) server now
// enforces TLS when configured with WithTLS, exactly like the TCP gRPC
// server: a client that dials with insecure (plaintext) credentials against
// a TLS-configured unix listener fails to connect instead of silently
// succeeding over an unencrypted socket. This closes half of the RBAC-bypass
// gap (see TestStart_UnixOnly_AuthMiddlewareRejectsUnauthenticated for the
// other half: authentication/authorization enforcement once TLS is up).
func TestStart_UnixOnly_RequiresTLS(t *testing.T) {
	socketPath := filepath.Join(t.TempDir(), "qrdb.sock")
	cfg := testConfigFull(t, protocolFlags{unix: true}, socketPath)

	tlsCfg := selfSignedTLSConfig(t)
	srv := New(cfg, newMockKVService(), &mockAdminService{}, WithTLS(tlsCfg))

	if err := srv.Start(); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer srv.Stop()

	// A plaintext (insecure-credentials) client must not be able to reach a
	// TLS-configured unix listener.
	tr, err := unixtransport.New(transport.WithAddress(srv.UnixAddr()))
	if err != nil {
		t.Fatalf("unix transport New: %v", err)
	}
	defer tr.Close()

	if err := tr.Connect(context.Background()); err == nil {
		t.Fatal("expected insecure Connect to fail against a TLS-configured unix listener")
	}
}

// TestStart_UnixOnly_AuthMiddlewareRejectsUnauthenticated proves the unix
// (gRPC-over-UDS) server applies the same authentication/authorization
// middleware as the TCP gRPC server instead of bypassing it as a
// "local trust" shortcut: a client that completes the TLS handshake but
// presents no client certificate is rejected before reaching the KV
// handler, exactly as it would be over TCP gRPC.
func TestStart_UnixOnly_AuthMiddlewareRejectsUnauthenticated(t *testing.T) {
	socketPath := filepath.Join(t.TempDir(), "qrdb.sock")
	cfg := testConfigFull(t, protocolFlags{unix: true}, socketPath)

	tlsCfg := selfSignedTLSConfig(t)
	authMW := authmw.New(&auth.NoopAuthorizer{}, authmw.WithAuthenticator(auth.NewMTLSAuthenticator()))

	srv := New(cfg, newMockKVService(), &mockAdminService{}, WithTLS(tlsCfg), WithAuthMiddleware(authMW))

	if err := srv.Start(); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer srv.Stop()

	// Trust the server's self-signed cert but present no client certificate
	// of our own, mirroring an unauthenticated caller.
	clientTLS := &tls.Config{
		RootCAs:    tlsCfg.RootCAs,
		ServerName: "localhost",
		MinVersion: tls.VersionTLS13,
	}
	tr, err := unixtransport.New(
		transport.WithAddress(srv.UnixAddr()),
		transport.WithTLSConfig(clientTLS),
	)
	if err != nil {
		t.Fatalf("unix transport New: %v", err)
	}
	defer tr.Close()

	err = tr.Connect(context.Background())
	if err == nil {
		t.Fatal("expected Connect to fail for an unauthenticated (no client certificate) unix caller")
	}
	t.Logf("unauthenticated unix Connect rejected as expected: %v", err)
}
