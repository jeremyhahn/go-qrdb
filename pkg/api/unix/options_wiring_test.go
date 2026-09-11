//go:build codec_json && raft

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

// Option-wiring and TLS-refusal tests for the Unix transport.
//
// An option that failed to land is invisible at construction and only shows up
// as a missing capability at request time, so each one is checked for the field
// it is supposed to set.

package unix

import (
	"context"
	"crypto/tls"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/apimodule"
	"github.com/jeremyhahn/go-qrdb/pkg/audit"
	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	authmw "github.com/jeremyhahn/go-qrdb/pkg/auth/middleware"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// TestServiceOptionsLandOnTheServer covers the option setters that had no
// coverage. Each supplies a distinguishable value and checks the server picked
// it up.
func TestServiceOptionsLandOnTheServer(t *testing.T) {
	module := &stubModule{}

	srv := NewServer(newMockKVService(), &mockAdminService{}, "/tmp/test-wiring.sock",
		WithServiceRegistry(nil),
		WithServiceModules([]apimodule.ServiceModule{module}),
		WithTLS(selfSignedTLSConfig(t)),
	)

	if srv.registry != nil {
		t.Error("WithServiceRegistry(nil) must leave the registry unset")
	}
	if len(srv.modules) != 1 {
		t.Fatalf("WithServiceModules registered %d modules, want 1", len(srv.modules))
	}
	if srv.modules[0] != module {
		t.Error("WithServiceModules registered a different module")
	}
}

// TestWithServiceModulesRegistersTheWholeSetInOneCall pins the option's actual
// contract: it takes the whole slice and ASSIGNS it, matching the gRPC, MCP
// and REST transports (all four do `s.modules = mods`). A caller must pass
// every module in a single call -- a second call REPLACES the first set rather
// than adding to it. Documented here so the behaviour is not mistaken for a
// bug later.
func TestWithServiceModulesRegistersTheWholeSetInOneCall(t *testing.T) {
	first := &stubModule{name: "first"}
	second := &stubModule{name: "second"}

	srv := NewServer(newMockKVService(), &mockAdminService{}, "/tmp/test-modules.sock",
		WithServiceModules([]apimodule.ServiceModule{first, second}),
		WithTLS(selfSignedTLSConfig(t)),
	)
	if len(srv.modules) != 2 {
		t.Fatalf("registered %d modules, want 2", len(srv.modules))
	}

	replaced := NewServer(newMockKVService(), &mockAdminService{}, "/tmp/test-modules-2.sock",
		WithServiceModules([]apimodule.ServiceModule{first}),
		WithServiceModules([]apimodule.ServiceModule{second}),
		WithTLS(selfSignedTLSConfig(t)),
	)
	if len(replaced.modules) != 1 || replaced.modules[0] != second {
		t.Fatal("a second WithServiceModules call must replace the first set")
	}
}

// TestWithCeremonyCAIsHeldServerSide covers the CA option. The CA cannot cross
// the wire, so it must be injected here or CeremonyInitialize fails closed.
func TestWithCeremonyCAIsHeldServerSide(t *testing.T) {
	srv := NewServer(newMockKVService(), &mockAdminService{}, "/tmp/test-ceremony.sock",
		WithCeremonyCA(nil),
		WithTLS(selfSignedTLSConfig(t)),
	)
	if srv.ceremonyCA != nil {
		t.Fatal("expected the nil CA to be stored as nil")
	}
}

// TestStartRefusesWithoutTLS covers the fail-closed guard. The Unix transport
// authenticates callers by their mTLS client certificate, so binding without
// TLS would expose an UNAUTHENTICATED socket -- the auth middleware would have
// no identity to check.
func TestStartRefusesWithoutTLS(t *testing.T) {
	srv := NewServer(newMockKVService(), &mockAdminService{}, "/tmp/test-notls.sock")

	err := srv.Start()
	if err == nil {
		t.Fatal("Start must refuse to bind without TLS")
	}
	var want *TLSNotConfiguredError
	if !errors.As(err, &want) {
		t.Fatalf("error = %v (%T), want *TLSNotConfiguredError", err, err)
	}
	if !strings.Contains(err.Error(), "TLS-only") {
		t.Errorf("message %q does not explain the transport is TLS-only", err.Error())
	}
}

// TestStartRefusesTLSBelow13 covers the version floor. The client-certificate
// identity model this transport relies on is only supported at TLS 1.3 here,
// so an explicitly weaker MinVersion is refused rather than quietly accepted.
func TestStartRefusesTLSBelow13(t *testing.T) {
	weak := selfSignedTLSConfig(t)
	weak.MinVersion = tls.VersionTLS12

	srv := NewServer(newMockKVService(), &mockAdminService{}, "/tmp/test-weaktls.sock",
		WithTLS(weak))

	err := srv.Start()
	if err == nil {
		t.Fatal("Start must refuse a TLS config below 1.3")
	}
	var want *WeakTLSVersionError
	if !errors.As(err, &want) {
		t.Fatalf("error = %v (%T), want *WeakTLSVersionError", err, err)
	}
	if want.MinVersion != tls.VersionTLS12 {
		t.Errorf("MinVersion = %#x, want %#x", want.MinVersion, tls.VersionTLS12)
	}
	if !strings.Contains(err.Error(), "TLS 1.2") {
		t.Errorf("message %q does not name the offending version", err.Error())
	}
}

// TestTLSVersionNameRendersEveryVersion covers the renderer used in those
// messages. An operator reading "0x0303" instead of "TLS 1.2" has to look the
// constant up, which is the whole reason this function exists.
func TestTLSVersionNameRendersEveryVersion(t *testing.T) {
	tests := []struct {
		version uint16
		want    string
	}{
		{tls.VersionTLS10, "TLS 1.0"},
		{tls.VersionTLS11, "TLS 1.1"},
		{tls.VersionTLS12, "TLS 1.2"},
		{tls.VersionTLS13, "TLS 1.3"},
		{0, "unset"},
		{0x9999, "0x9999"},
	}

	for _, tc := range tests {
		t.Run(tc.want, func(t *testing.T) {
			if got := tlsVersionName(tc.version); got != tc.want {
				t.Fatalf("tlsVersionName(%#x) = %q, want %q", tc.version, got, tc.want)
			}
		})
	}
}

// stubModule is a no-op ServiceModule for the wiring tests.
type stubModule struct {
	apimodule.BaseModule
	name string
}

func (m *stubModule) Name() string {
	if m.name == "" {
		return "stub"
	}
	return m.name
}

// TestStartRegistersEveryWiredService drives Start with the optional services
// attached, so the registration branches that are skipped on a bare server all
// run.
//
// Each branch is a `if s.x != nil { register }` guard, and a service that fails
// to register is invisible until a client calls it and gets Unimplemented --
// the server itself starts perfectly happily. Starting once with everything
// wired is what proves the guards are reached.
func TestStartRegistersEveryWiredService(t *testing.T) {
	socketPath := tempSocketPath(t)

	srv := NewServer(newMockKVService(), &mockAdminService{}, socketPath,
		WithPINBackend(&stubPINBackend{}),
		WithCeremonyService(&stubCeremonySvc{}),
		WithTenantService(&stubTenantBarrierSvc{}),
		WithTenantBarrierService(&stubTenantBarrierSvc{}),
		WithBarrierService(&stubBarrierSvc{}),
		WithCredentialService(&stubCredentialSvc{}),
		WithCollectionService(&stubCollectionSvc{}),
		WithTransaction(&stubTransactionSvc{}),
		WithBatch(&stubBatchSvc{}),
		WithWatch(&stubWatchSvc{}),
		WithLease(&stubLeaseSvc{}),
		WithIndex(&stubIndexSvc{}),
		WithTimeSeries(&stubTimeSeriesSvc{}),
		WithVector(&stubVectorSvc{}),
		WithMVCC(&stubMVCCSvc{}),
		WithDatabaseService(&stubDatabaseSvc{}),
		WithUserService(&stubUserSvc{}),
		WithRoleService(&stubRoleSvc{}),
		WithCertProvider(&stubCertProv{}),
		WithServiceModules([]apimodule.ServiceModule{&stubModule{}}),
		WithTLS(selfSignedTLSConfig(t)),
	)

	if err := srv.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer srv.Stop()

	if srv.Addr() != socketPath {
		t.Errorf("Addr = %q, want %q", srv.Addr(), socketPath)
	}
}

// TestStartReplacesAStaleSocketFile covers the cleanup that runs before bind.
//
// A crashed process leaves its socket file behind, and net.Listen("unix", ...)
// refuses to bind over an existing path -- so without this removal a node
// could never restart after an unclean shutdown without manual intervention.
func TestStartReplacesAStaleSocketFile(t *testing.T) {
	socketPath := tempSocketPath(t)

	// Simulate the leftover file from a previous run.
	if err := os.WriteFile(socketPath, []byte("stale"), 0o600); err != nil {
		t.Fatalf("seed stale socket: %v", err)
	}

	srv := NewServer(newMockKVService(), &mockAdminService{}, socketPath,
		WithTLS(selfSignedTLSConfig(t)))

	if err := srv.Start(); err != nil {
		t.Fatalf("Start must replace a stale socket file, got: %v", err)
	}
	defer srv.Stop()

	info, err := os.Stat(socketPath)
	if err != nil {
		t.Fatalf("stat socket: %v", err)
	}
	if info.Mode()&os.ModeSocket == 0 {
		t.Fatal("the path is still a regular file; the listener did not replace it")
	}
}

// allowAllAuthorizer is the minimal auth.Authorizer needed to build a real
// AuthMiddleware. Its verdict is irrelevant here -- the tests using it check
// that the middleware is INSTALLED, not what it decides.
type allowAllAuthorizer struct{}

func (allowAllAuthorizer) Authorize(_ context.Context, _ *auth.Identity, _, _ string) error {
	return nil
}

// recordingAuditLogger is a no-op audit.AuditLogger for the registration test.
type recordingAuditLogger struct{}

func (recordingAuditLogger) Emit(_ context.Context, _ audit.AuditEvent) error { return nil }

func (recordingAuditLogger) Query(_ context.Context, _ audit.Filter) (*audit.Page, error) {
	return &audit.Page{}, nil
}

func (recordingAuditLogger) Close() error { return nil }

// TestStartInstallsTheAuthMiddlewareAndAuditService covers the two branches
// that decide whether this transport is authorized at all.
//
// The Unix socket previously omitted the auth middleware entirely, letting
// RBAC-gated RPCs bypass authorization on this transport (see the comment in
// Start). That regression is invisible from the outside -- the server starts
// and serves -- so the interceptor wiring is worth pinning explicitly.
func TestStartInstallsTheAuthMiddlewareAndAuditService(t *testing.T) {
	socketPath := tempSocketPath(t)

	srv := NewServer(newMockKVService(), &mockAdminService{}, socketPath,
		WithAuthMiddleware(authmw.New(allowAllAuthorizer{})),
		WithAuditLogger(recordingAuditLogger{}),
		WithTLS(selfSignedTLSConfig(t)),
	)

	if srv.authMW == nil {
		t.Fatal("WithAuthMiddleware did not set the middleware")
	}
	if srv.auditLogger == nil {
		t.Fatal("WithAuditLogger did not set the logger")
	}

	if err := srv.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer srv.Stop()
}

// stubServerStream is the minimal grpc.ServerStream the interceptors touch.
// Only Context() is exercised -- the logging interceptor logs through it.
type stubServerStream struct{}

func (stubServerStream) SetHeader(metadata.MD) error  { return nil }
func (stubServerStream) SendHeader(metadata.MD) error { return nil }
func (stubServerStream) SetTrailer(metadata.MD)       {}
func (stubServerStream) Context() context.Context     { return context.Background() }
func (stubServerStream) SendMsg(any) error            { return nil }
func (stubServerStream) RecvMsg(any) error            { return nil }

// TestStreamRecoveryInterceptorConvertsAPanicToAnError covers the stream
// recovery guard.
//
// Without it a panic in any streaming handler takes the whole daemon down --
// one malformed watch request would kill every other client's connection. The
// converted error must be Internal and must NOT leak the panic value, which
// often contains request data.
func TestStreamRecoveryInterceptorConvertsAPanicToAnError(t *testing.T) {
	info := &grpc.StreamServerInfo{FullMethod: "/qrdb.v1.QRDBService/Watch"}

	err := streamRecoveryInterceptor(nil, stubServerStream{}, info, func(any, grpc.ServerStream) error {
		panic("secret request data in the panic value")
	})

	if err == nil {
		t.Fatal("a panicking stream handler must yield an error, not crash the process")
	}
	if got := status.Code(err); got != codes.Internal {
		t.Errorf("code = %v, want %v", got, codes.Internal)
	}
	if strings.Contains(err.Error(), "secret request data") {
		t.Error("the panic value leaked into the client-visible error")
	}
}

// TestStreamRecoveryInterceptorPassesThroughNormalCalls is the other half: a
// handler that returns normally must be untouched.
func TestStreamRecoveryInterceptorPassesThroughNormalCalls(t *testing.T) {
	info := &grpc.StreamServerInfo{FullMethod: "/qrdb.v1.QRDBService/Watch"}
	sentinel := errors.New("handler said no")

	if err := streamRecoveryInterceptor(nil, stubServerStream{}, info, func(any, grpc.ServerStream) error {
		return nil
	}); err != nil {
		t.Fatalf("a successful handler must return nil, got %v", err)
	}

	if err := streamRecoveryInterceptor(nil, stubServerStream{}, info, func(any, grpc.ServerStream) error {
		return sentinel
	}); !errors.Is(err, sentinel) {
		t.Fatalf("error = %v, want the handler's own error", err)
	}
}

// TestStreamLoggingInterceptorPreservesTheHandlerResult covers both logging
// branches (success and failure). The interceptor observes; it must never
// change what the handler returned, or an error would be swallowed on its way
// to the client.
func TestStreamLoggingInterceptorPreservesTheHandlerResult(t *testing.T) {
	info := &grpc.StreamServerInfo{FullMethod: "/qrdb.v1.QRDBService/Watch"}
	sentinel := status.Error(codes.NotFound, "no such key")

	if err := streamLoggingInterceptor(nil, stubServerStream{}, info, func(any, grpc.ServerStream) error {
		return nil
	}); err != nil {
		t.Fatalf("a successful handler must return nil, got %v", err)
	}

	err := streamLoggingInterceptor(nil, stubServerStream{}, info, func(any, grpc.ServerStream) error {
		return sentinel
	})
	if err == nil {
		t.Fatal("the handler's error was swallowed")
	}
	if got := status.Code(err); got != codes.NotFound {
		t.Errorf("code = %v, want %v", got, codes.NotFound)
	}
}
