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
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
	resttransport "github.com/jeremyhahn/go-qrdb/sdk/go/transport/rest"
	unixtransport "github.com/jeremyhahn/go-qrdb/sdk/go/transport/unix"
)

// TestStart_RESTOnly_EncryptionMigratorReachableWithoutBarrier is the
// go-qrdb#558 REST-side regression test, mirroring
// TestStart_UnixOnly_EncryptionMigratorReachable in server_unix_parity_test.go
// exactly except for the transport: a node with an EncryptionMigrator
// configured but NO barrier service (mirroring seal.enabled: false in
// raft/clustered mode -- the daemon's system migrator is wired independent of
// whether a barrier exists) must still reach the migrator over REST.
//
// Before the fix, POST /v1/barrier/enable-encryption was registered only
// when BOTH barrierOn (a barrier service was wired) AND an EncryptionMigrator
// were configured (pkg/api/rest/server.go's registerRoutes), so this exact
// configuration 404'd at the router -- the request never reached the handler,
// let alone the migrator -- while gRPC and Unix (proven by the sibling test
// above), which never depended on barrierOn, worked correctly. This test
// dials the SAME kind of node over REST instead of Unix and proves parity.
func TestStart_RESTOnly_EncryptionMigratorReachableWithoutBarrier(t *testing.T) {
	cfg := testConfigFull(t, protocolFlags{rest: true}, filepath.Join(t.TempDir(), "qrdb.sock"))

	invoked := make(chan service.BarrierScope, 1)
	migrator := encMigratorFunc(func(_ context.Context, scope service.BarrierScope, _ seal.MigrationOptions) error {
		invoked <- scope
		return nil
	})

	// Deliberately no WithBarrierService: mirrors seal.enabled: false.
	srv := New(cfg, newMockKVService(), &mockAdminService{}, WithEncryptionMigrator(migrator), WithCeremonyCA(testServerCA))

	if err := srv.Start(); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer srv.Stop()

	tr, err := resttransport.New(transport.WithAddress("https://"+srv.RESTAddr()), testAutoProvisionClientTLSOption(t))
	if err != nil {
		t.Fatalf("rest transport New: %v", err)
	}
	if err := tr.Connect(context.Background()); err != nil {
		t.Fatalf("rest transport Connect: %v", err)
	}
	defer tr.Close()

	resp, err := tr.BarrierEnableEncryption(context.Background(), &transport.BarrierEnableEncryptionRequest{
		Strategy:   "passphrase",
		Passphrase: []byte("pw"),
	})
	if err != nil {
		var reqErr *transport.RequestError
		if errors.As(err, &reqErr) && reqErr.StatusCode == 404 {
			t.Fatalf("BarrierEnableEncryption over REST: 404 (route not registered without a barrier service, go-qrdb#558): %v", err)
		}
		t.Fatalf("BarrierEnableEncryption over REST: %v (migrator not reachable)", err)
	}
	if resp.Status != "migration_started" {
		t.Errorf("Status = %q, want %q", resp.Status, "migration_started")
	}

	select {
	case scope := <-invoked:
		if scope.Kind != service.ScopeSystem {
			t.Errorf("scope kind = %q, want system", scope.Kind)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("encryption migrator was not invoked within 3s")
	}
}

// TestStart_RESTAndUnix_NoBarrierNoMigrator_SameTypedRefusal is the
// go-qrdb#558 transport-matrix test: a node with NEITHER a barrier service
// NOR an EncryptionMigrator configured (standalone mode with seal disabled)
// must refuse BarrierEnableEncryption on every transport rather than 404 on
// some and a proper typed error on others. Both REST and Unix (which shares
// gRPC's service registration) reach the handler here -- REST via its
// always-registered route with an internal nil-migrator check, Unix/gRPC via
// their always-registered RPC with the same kind of check -- and neither
// gives a router/service "not found" response.
//
// QUIC and MCP are not dialed here: their SDK CLIENT wrappers
// (sdk/go/transport/quic, sdk/go/transport/mcp) stub BarrierEnableEncryption
// unconditionally with a NotImplementedError regardless of server state (a
// separate, pre-existing, documented limitation -- see
// docs/seal/enabling-encryption.md's "Transport support" note), so they
// cannot observe server-side behavior through this client. Their SERVER-side
// handlers are covered directly in pkg/api/quic and pkg/api/mcp's own test
// suites (TestHandleBarrierEnableEncryption_NilService and
// TestHandleBarrierEnableEncryption_MCP_NilMigratorRefusesTyped /
// TestServer_BarrierEnableEncryption_RegisteredWithoutBarrierService).
func TestStart_RESTAndUnix_NoBarrierNoMigrator_SameTypedRefusal(t *testing.T) {
	socketPath := filepath.Join(t.TempDir(), "qrdb.sock")
	cfg := testConfigFull(t, protocolFlags{rest: true, unix: true}, socketPath)

	// Deliberately no WithBarrierService and no WithEncryptionMigrator:
	// mirrors standalone mode with seal.enabled: false.
	srv := New(cfg, newMockKVService(), &mockAdminService{}, WithCeremonyCA(testServerCA))
	if err := srv.Start(); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer srv.Stop()

	req := &transport.BarrierEnableEncryptionRequest{Strategy: "passphrase", Passphrase: []byte("pw")}

	restTr, err := resttransport.New(transport.WithAddress("https://"+srv.RESTAddr()), testAutoProvisionClientTLSOption(t))
	if err != nil {
		t.Fatalf("rest transport New: %v", err)
	}
	if err := restTr.Connect(context.Background()); err != nil {
		t.Fatalf("rest transport Connect: %v", err)
	}
	defer restTr.Close()

	if _, err := restTr.BarrierEnableEncryption(context.Background(), req); err == nil {
		t.Fatal("REST: expected an error with no migrator configured")
	} else if reqErr := new(transport.RequestError); errors.As(err, &reqErr) && reqErr.StatusCode == 404 {
		t.Fatalf("REST: 404 (route not registered) instead of a typed refusal: %v", err)
	}

	unixTr, err := unixtransport.New(transport.WithAddress(srv.UnixAddr()), testAutoProvisionClientTLSOption(t))
	if err != nil {
		t.Fatalf("unix transport New: %v", err)
	}
	if err := unixTr.Connect(context.Background()); err != nil {
		t.Fatalf("unix transport Connect: %v", err)
	}
	defer unixTr.Close()

	if _, err := unixTr.BarrierEnableEncryption(context.Background(), req); err == nil {
		t.Fatal("Unix: expected an error with no migrator configured")
	} else if !errors.As(err, new(*transport.CapabilityUnsupportedError)) {
		t.Fatalf("Unix: err = %v (%T), want *transport.CapabilityUnsupportedError (mapped from codes.Unimplemented)", err, err)
	}
}
