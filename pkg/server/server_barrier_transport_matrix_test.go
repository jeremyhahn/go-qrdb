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
	grpctransport "github.com/jeremyhahn/go-qrdb/sdk/go/transport/grpc"
	mcptransport "github.com/jeremyhahn/go-qrdb/sdk/go/transport/mcp"
	quictransport "github.com/jeremyhahn/go-qrdb/sdk/go/transport/quic"
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

// TestStart_AllTransports_NoBarrierNoMigrator_SameTypedRefusal is the
// go-qrdb#558 transport-matrix test: a node with NEITHER a barrier service
// NOR an EncryptionMigrator configured (standalone mode with seal disabled)
// must refuse BarrierEnableEncryption on every transport rather than 404 on
// some and a proper typed error on others. REST, gRPC, Unix (which shares
// gRPC's service registration), QUIC and MCP all reach the handler here --
// REST via its always-registered route with an internal nil-migrator check,
// the others via their always-registered RPC/tool with the same kind of
// check -- and none of them gives a router/service "not found" response.
//
// go-qrdb#550's fourth commit closed the gap that used to carve QUIC and MCP
// out of this test: their SDK CLIENT wrappers (sdk/go/transport/quic,
// sdk/go/transport/mcp) used to stub BarrierEnableEncryption unconditionally
// with a NotImplementedError regardless of server state, so they could not
// observe server-side behavior through this client at all. Now that both
// forward to their already-working server handlers (the same marshal/dial
// pattern they use for every other barrier call), they belong in this matrix
// on the same terms as REST, gRPC and Unix.
func TestStart_AllTransports_NoBarrierNoMigrator_SameTypedRefusal(t *testing.T) {
	socketPath := filepath.Join(t.TempDir(), "qrdb.sock")
	cfg := testConfigFull(t, protocolFlags{rest: true, grpc: true, quic: true, unix: true, mcp: true}, socketPath)

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
	} else if !errors.As(err, &reqErr) || reqErr.StatusCode != 501 {
		t.Fatalf("REST: err = %v (%T), want a 501 (service.ErrMigrationProviderUnavailable)", err, err)
	}

	grpcTr, err := grpctransport.New(transport.WithAddress(srv.GRPCAddr()), testAutoProvisionClientTLSOption(t))
	if err != nil {
		t.Fatalf("grpc transport New: %v", err)
	}
	if err := grpcTr.Connect(context.Background()); err != nil {
		t.Fatalf("grpc transport Connect: %v", err)
	}
	defer grpcTr.Close()

	if _, err := grpcTr.BarrierEnableEncryption(context.Background(), req); err == nil {
		t.Fatal("gRPC: expected an error with no migrator configured")
	} else if !errors.As(err, new(*transport.CapabilityUnsupportedError)) {
		t.Fatalf("gRPC: err = %v (%T), want *transport.CapabilityUnsupportedError (mapped from codes.Unimplemented)", err, err)
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

	quicTr, err := quictransport.New(transport.WithAddress(srv.QUICAddr()), testAutoProvisionClientTLSOption(t))
	if err != nil {
		t.Fatalf("quic transport New: %v", err)
	}
	if err := quicTr.Connect(context.Background()); err != nil {
		t.Fatalf("quic transport Connect: %v", err)
	}
	defer quicTr.Close()

	if _, err := quicTr.BarrierEnableEncryption(context.Background(), req); err == nil {
		t.Fatal("QUIC: expected an error with no migrator configured")
	} else if !errors.As(err, new(*transport.CapabilityUnsupportedError)) {
		t.Fatalf("QUIC: err = %v (%T), want *transport.CapabilityUnsupportedError (mapped from codes.Unimplemented)", err, err)
	}

	mcpTr, err := mcptransport.New(transport.WithAddress("https://"+srv.MCPAddr()+"/mcp"), testAutoProvisionClientTLSOption(t))
	if err != nil {
		t.Fatalf("mcp transport New: %v", err)
	}
	if err := mcpTr.Connect(context.Background()); err != nil {
		t.Fatalf("mcp transport Connect: %v", err)
	}
	defer mcpTr.Close()

	if _, err := mcpTr.BarrierEnableEncryption(context.Background(), req); err == nil {
		t.Fatal("MCP: expected an error with no migrator configured")
	} else if !errors.As(err, new(*transport.CapabilityUnsupportedError)) {
		t.Fatalf("MCP: err = %v (%T), want *transport.CapabilityUnsupportedError (mapped from codes.Unimplemented)", err, err)
	}
}

// TestBarrierEnableEncryption_ConformsAcrossEverySDKTransport is the
// go-qrdb#550 fourth-commit conformance test: enabling encryption through
// every SDK transport against a real node gives the same outcome. REST,
// gRPC, Unix, QUIC and MCP all dial the SAME running *Server, wired with one
// shared EncryptionMigrator, and every one of them must report
// "migration_started" for the same system-scope request with the migrator
// observing the same scope.
//
// The migrator here is the same kind of test double
// TestStart_RESTOnly_EncryptionMigratorReachableWithoutBarrier already uses,
// not a real seal barrier: a real system-scope migration's Quiesce phase
// physically tears down and rebuilds the data-plane server answering the
// request (see pkg/api/rest/handlers_barrier.go's migrationAcceptWindow
// doc), and racing that teardown against this package's *Server across five
// transports in one process is a real-migration timing hazard belonging to
// the migration/accept-window design, not to whether an SDK client reaches
// its handler -- which is the one thing this test exists to pin. The
// embedded transport is not dialed here: it does not reach a *Server at all
// (sdk/go/node wires it directly against the service layer), and its own
// REAL migration -- including the server-recycle-equivalent teardown and the
// strategy actually flipping from "none" to "passphrase" -- is covered
// end-to-end by sdk/go/node's TestNode_EnableEncryption_ViaEmbeddedTransport.
// Together the two tests cover all six SDK transports against the promise
// each is positioned to check.
func TestBarrierEnableEncryption_ConformsAcrossEverySDKTransport(t *testing.T) {
	socketPath := filepath.Join(t.TempDir(), "qrdb.sock")
	cfg := testConfigFull(t, protocolFlags{rest: true, grpc: true, quic: true, unix: true, mcp: true}, socketPath)

	invoked := make(chan service.BarrierScope, 1)
	migrator := encMigratorFunc(func(_ context.Context, scope service.BarrierScope, _ seal.MigrationOptions) error {
		invoked <- scope
		return nil
	})

	// Deliberately no WithBarrierService: mirrors seal.enabled: false, the
	// exact configuration under which the fourth commit's gaps hid.
	srv := New(cfg, newMockKVService(), &mockAdminService{}, WithEncryptionMigrator(migrator), WithCeremonyCA(testServerCA))
	if err := srv.Start(); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer srv.Stop()

	req := &transport.BarrierEnableEncryptionRequest{Strategy: "passphrase", Passphrase: []byte("pw")}

	assertMigrationStarted := func(t *testing.T, transportName string, resp *transport.BarrierEnableEncryptionResponse, err error) {
		t.Helper()
		if err != nil {
			t.Fatalf("%s: BarrierEnableEncryption: %v", transportName, err)
		}
		if resp.Status != "migration_started" {
			t.Errorf("%s: Status = %q, want %q", transportName, resp.Status, "migration_started")
		}
		select {
		case scope := <-invoked:
			if scope.Kind != service.ScopeSystem {
				t.Errorf("%s: scope kind = %q, want system", transportName, scope.Kind)
			}
		case <-time.After(3 * time.Second):
			t.Fatalf("%s: encryption migrator was not invoked within 3s", transportName)
		}
	}

	restTr, err := resttransport.New(transport.WithAddress("https://"+srv.RESTAddr()), testAutoProvisionClientTLSOption(t))
	if err != nil {
		t.Fatalf("rest transport New: %v", err)
	}
	if err := restTr.Connect(context.Background()); err != nil {
		t.Fatalf("rest transport Connect: %v", err)
	}
	defer restTr.Close()
	resp, err := restTr.BarrierEnableEncryption(context.Background(), req)
	assertMigrationStarted(t, "REST", resp, err)

	grpcTr, err := grpctransport.New(transport.WithAddress(srv.GRPCAddr()), testAutoProvisionClientTLSOption(t))
	if err != nil {
		t.Fatalf("grpc transport New: %v", err)
	}
	if err := grpcTr.Connect(context.Background()); err != nil {
		t.Fatalf("grpc transport Connect: %v", err)
	}
	defer grpcTr.Close()
	resp, err = grpcTr.BarrierEnableEncryption(context.Background(), req)
	assertMigrationStarted(t, "gRPC", resp, err)

	unixTr, err := unixtransport.New(transport.WithAddress(srv.UnixAddr()), testAutoProvisionClientTLSOption(t))
	if err != nil {
		t.Fatalf("unix transport New: %v", err)
	}
	if err := unixTr.Connect(context.Background()); err != nil {
		t.Fatalf("unix transport Connect: %v", err)
	}
	defer unixTr.Close()
	resp, err = unixTr.BarrierEnableEncryption(context.Background(), req)
	assertMigrationStarted(t, "Unix", resp, err)

	quicTr, err := quictransport.New(transport.WithAddress(srv.QUICAddr()), testAutoProvisionClientTLSOption(t))
	if err != nil {
		t.Fatalf("quic transport New: %v", err)
	}
	if err := quicTr.Connect(context.Background()); err != nil {
		t.Fatalf("quic transport Connect: %v", err)
	}
	defer quicTr.Close()
	resp, err = quicTr.BarrierEnableEncryption(context.Background(), req)
	assertMigrationStarted(t, "QUIC", resp, err)

	mcpTr, err := mcptransport.New(transport.WithAddress("https://"+srv.MCPAddr()+"/mcp"), testAutoProvisionClientTLSOption(t))
	if err != nil {
		t.Fatalf("mcp transport New: %v", err)
	}
	if err := mcpTr.Connect(context.Background()); err != nil {
		t.Fatalf("mcp transport Connect: %v", err)
	}
	defer mcpTr.Close()
	resp, err = mcpTr.BarrierEnableEncryption(context.Background(), req)
	assertMigrationStarted(t, "MCP", resp, err)
}
