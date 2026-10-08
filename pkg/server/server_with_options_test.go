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

package server

import (
	"context"
	"crypto/tls"
	"errors"
	"path/filepath"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/authfactors"
	"github.com/jeremyhahn/go-qrdb/pkg/cakey"
	"github.com/jeremyhahn/go-qrdb/pkg/keywrap"
	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
	"github.com/jeremyhahn/go-qrdb/pkg/pin"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/trustanchor"
)

// TestWithStrategyRegistry_Applied asserts that an injected
// StrategyRegistry is reachable from the server's serverOptions and that
// the daemon can resolve names through it.
func TestWithStrategyRegistry_Applied(t *testing.T) {
	reg := seal.NewDefaultStrategyRegistry()
	srv := New(testConfig(t, true, true), newMockKVService(), &mockAdminService{},
		WithStrategyRegistry(reg),
	)
	if srv.opts.strategyRegistry != reg {
		t.Fatal("strategyRegistry not stored in opts")
	}
	// Sanity: the injected registry can resolve a default strategy.
	if _, err := reg.LookupFactory(seal.StrategyPassphrase); err != nil {
		t.Fatalf("default registry LookupFactory(software): %v", err)
	}
}

// TestWithPINBackend_Applied asserts that an injected PINBackend is
// reachable from the server's serverOptions for the ceremony service.
func TestWithPINBackend_Applied(t *testing.T) {
	backend, err := pin.NewSoftwareBackend(nil, pin.DefaultHashConfig())
	if err != nil {
		t.Fatalf("NewSoftwareBackend: %v", err)
	}
	srv := New(testConfig(t, true, true), newMockKVService(), &mockAdminService{},
		WithPINBackend(backend),
	)
	if srv.opts.pinBackend == nil {
		t.Fatal("pinBackend not stored in opts")
	}
	if srv.opts.pinBackend.Strategy() != pin.StrategySoftware {
		t.Fatalf("pinBackend Strategy = %q, want %q",
			srv.opts.pinBackend.Strategy(), pin.StrategySoftware)
	}
}

// TestWithPINBackend_OneLockoutPerOption: the option decorates the backend
// with the SO PIN lockout when it is built, so every resolution of it -- the
// server's own and a composer's ResolveCryptoOptions for lifecycle -- yields
// the same decorated instance, and therefore one failure counter. A nil
// backend stays nil and fails closed.
func TestWithPINBackend_OneLockoutPerOption(t *testing.T) {
	backend, err := pin.NewSoftwareBackend(nil, pin.DefaultHashConfig())
	if err != nil {
		t.Fatalf("NewSoftwareBackend: %v", err)
	}
	opt := WithPINBackend(backend)
	first := ResolveCryptoOptions(opt).PINBackend
	second := ResolveCryptoOptions(opt).PINBackend
	if first == nil || first != second {
		t.Fatal("two resolutions of one WithPINBackend option must yield the same backend instance")
	}
	if pin.NewLockoutBackend(first) != first {
		t.Fatal("WithPINBackend must store the backend decorated with the SO PIN lockout")
	}
	if ResolveCryptoOptions(WithPINBackend(first)).PINBackend != first {
		t.Fatal("re-injecting a decorated backend must not add a second lockout")
	}
	if ResolveCryptoOptions(WithPINBackend(nil)).PINBackend != nil {
		t.Fatal("a nil backend must stay nil")
	}
}

// stubKeyWrapProvider is a keywrap.Provider with a non-default ID; it only
// needs to be identifiable, never to wrap.
type stubKeyWrapProvider struct{}

func (stubKeyWrapProvider) ProviderID() string { return "stub-hsm" }
func (stubKeyWrapProvider) Wrap(context.Context, string, []byte) ([]byte, error) {
	return nil, errors.New("stubKeyWrapProvider: not used")
}
func (stubKeyWrapProvider) Unwrap(context.Context, string, []byte) ([]byte, error) {
	return nil, errors.New("stubKeyWrapProvider: not used")
}

// stubCertKeyProviderFactory is a cakey.ProviderFactory that backs no tenant.
type stubCertKeyProviderFactory struct{}

func (*stubCertKeyProviderFactory) ProviderForTenant(string) (cakey.Provider, error) {
	return nil, errors.New("stubCertKeyProviderFactory: not used")
}
func (*stubCertKeyProviderFactory) TenantIDs(context.Context) ([]string, error) { return nil, nil }

// TestResolveCryptoOptions_ForwardsKeyCustodyOptions: the backup key-wrap
// registry and provider, the software provider's key directory and the Key B
// provider factory are all consumed during lifecycle.BuildServices -- before
// server.New runs -- so a composer reaches them only through
// ResolveCryptoOptions. Each must come back exactly as injected; one dropped
// here silently falls back to the built-in software custody.
func TestResolveCryptoOptions_ForwardsKeyCustodyOptions(t *testing.T) {
	reg := keywrap.NewRegistry()
	provider := stubKeyWrapProvider{}
	factory := &stubCertKeyProviderFactory{}
	keyDir := filepath.Join(t.TempDir(), "keys")

	got := ResolveCryptoOptions(
		WithKeyWrapRegistry(reg),
		WithKeyWrapProvider(provider),
		WithKeyDir(keyDir),
		WithCertKeyProviderFactory(factory),
		nil, // a nil option is skipped, not dereferenced
	)
	if got.KeyWrapRegistry != reg {
		t.Errorf("KeyWrapRegistry = %v, want the injected registry", got.KeyWrapRegistry)
	}
	if got.KeyWrapProvider != provider {
		t.Errorf("KeyWrapProvider = %v, want the injected provider", got.KeyWrapProvider)
	}
	if got.KeyDir != keyDir {
		t.Errorf("KeyDir = %q, want %q", got.KeyDir, keyDir)
	}
	if got.CertKeyProviderFactory != factory {
		t.Errorf("CertKeyProviderFactory = %v, want the injected factory", got.CertKeyProviderFactory)
	}

	if empty := ResolveCryptoOptions(); empty.KeyWrapRegistry != nil || empty.KeyWrapProvider != nil ||
		empty.KeyDir != "" || empty.CertKeyProviderFactory != nil {
		t.Errorf("ResolveCryptoOptions() with no options = %+v, want every key-custody field unset", empty)
	}
}

// TestResolveTLSConfig_ReturnsTheExplicitConfigOnly: an embedder wiring the
// client-trust pool onto the listener config before server.New gets exactly
// the *tls.Config passed via WithTLS, and nil when none was -- the signal that
// the server will auto-provision from the CeremonyCA instead. Options that do
// not carry TLS, and nil options, do not disturb the answer.
func TestResolveTLSConfig_ReturnsTheExplicitConfigOnly(t *testing.T) {
	tlsCfg := &tls.Config{MinVersion: tls.VersionTLS13}
	if got := ResolveTLSConfig(WithKeyDir("/unused"), nil, WithTLS(tlsCfg)); got != tlsCfg {
		t.Fatalf("ResolveTLSConfig = %p, want the WithTLS config %p", got, tlsCfg)
	}
	if got := ResolveTLSConfig(WithKeyDir("/unused"), nil); got != nil {
		t.Fatalf("ResolveTLSConfig without WithTLS = %v, want nil", got)
	}
}

// TestWithFactorRegistry_Applied asserts that an injected FactorRegistry
// is reachable from the server's serverOptions for the AuthFactorService.
func TestWithFactorRegistry_Applied(t *testing.T) {
	reg := authfactors.NewFactorRegistry()
	srv := New(testConfig(t, true, true), newMockKVService(), &mockAdminService{},
		WithFactorRegistry(reg),
	)
	if srv.opts.factorRegistry != reg {
		t.Fatal("factorRegistry not stored in opts")
	}
	if len(srv.opts.factorRegistry.List()) != 0 {
		t.Fatalf("fresh registry should be empty, got %v", srv.opts.factorRegistry.List())
	}
}

// TestWithTrustAnchors_Applied asserts that an injected trustanchor.Registry
// is reachable from the server's serverOptions, in place of qrdb's built-in
// store, for the lifecycle layer to forward into the cert-enrollment service.
func TestWithTrustAnchors_Applied(t *testing.T) {
	reg := trustanchor.NewKVRegistry(kvstore.NewMemoryKVStore())
	srv := New(testConfig(t, true, true), newMockKVService(), &mockAdminService{},
		WithTrustAnchors(reg),
	)
	if srv.opts.trustAnchors != reg {
		t.Fatal("trustAnchors not stored in opts")
	}
}

// TestWithTrustAnchors_UnsetIsNil asserts that a server built without
// WithTrustAnchors carries a nil registry in opts, matching every other
// crypto-injection option's absent state (the lifecycle layer falls back to
// qrdb's built-in trustanchor.NewKVRegistry when this is nil).
func TestWithTrustAnchors_UnsetIsNil(t *testing.T) {
	srv := New(testConfig(t, true, true), newMockKVService(), &mockAdminService{})
	if srv.opts.trustAnchors != nil {
		t.Fatalf("trustAnchors = %v, want nil when WithTrustAnchors is not supplied", srv.opts.trustAnchors)
	}
}

// TestResolveCryptoOptions_SurfacesTrustAnchors asserts that
// ResolveCryptoOptions carries an injected trustanchor.Registry through to
// embedders (the SDK node builders), which must resolve it BEFORE
// lifecycle.BuildServices runs -- passing it only to server.New would be too
// late for the cert-enrollment service's construction.
func TestResolveCryptoOptions_SurfacesTrustAnchors(t *testing.T) {
	reg := trustanchor.NewKVRegistry(kvstore.NewMemoryKVStore())

	crypto := ResolveCryptoOptions(WithTrustAnchors(reg))
	if crypto.TrustAnchors != reg {
		t.Fatal("ResolveCryptoOptions: TrustAnchors is not the supplied registry")
	}
}

// TestResolveCryptoOptions_TrustAnchorsNilWhenAbsent verifies that
// ResolveCryptoOptions returns a nil TrustAnchors when WithTrustAnchors is
// not among the options, so an embedder can tell "use the built-in store"
// apart from an explicit injection.
func TestResolveCryptoOptions_TrustAnchorsNilWhenAbsent(t *testing.T) {
	crypto := ResolveCryptoOptions()
	if crypto.TrustAnchors != nil {
		t.Fatalf("ResolveCryptoOptions: TrustAnchors = %v, want nil when option not supplied", crypto.TrustAnchors)
	}
}

// TestStart_AllProtocols_WithCertEnrollmentAndOnboarding asserts that
// WithCertEnrollment and WithTenantOnboarding reach every protocol server's
// own startup path (startREST, startGRPC, startQUIC, startUnix, startMCP
// each forward them with their own guard clause), not merely the REST
// handler the other options are typically exercised against.
func TestStart_AllProtocols_WithCertEnrollmentAndOnboarding(t *testing.T) {
	socketPath := filepath.Join(t.TempDir(), "qrdb.sock")
	tlsCfg := selfSignedTLSConfig(t)
	cfg := testConfigFull(t, protocolFlags{
		rest: true,
		grpc: true,
		quic: true,
		unix: true,
		mcp:  true,
	}, socketPath)
	srv := New(cfg, newMockKVService(), &mockAdminService{},
		WithCeremonyCA(testServerCA),
		WithTLS(tlsCfg),
		WithCertEnrollment(mockCertEnrollmentServicer{}),
		WithTenantOnboarding(mockTenantOnboardingServicer{}),
	)

	if err := srv.Start(); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer srv.Stop()

	if srv.RESTAddr() == "" {
		t.Error("expected non-empty RESTAddr")
	}
	if srv.GRPCAddr() == "" {
		t.Error("expected non-empty GRPCAddr")
	}
	if srv.QUICAddr() == "" {
		t.Error("expected non-empty QUICAddr")
	}
	if srv.UnixAddr() == "" {
		t.Error("expected non-empty UnixAddr")
	}
	if srv.MCPAddr() == "" {
		t.Error("expected non-empty MCPAddr")
	}
}

// TestWithAllInjections_Compose asserts that the Phase-3 injections coexist
// on the same server without conflicting.
func TestWithAllInjections_Compose(t *testing.T) {
	reg := seal.NewDefaultStrategyRegistry()
	backend, _ := pin.NewSoftwareBackend(nil, pin.DefaultHashConfig())
	factors := authfactors.NewFactorRegistry()

	srv := New(testConfig(t, true, true), newMockKVService(), &mockAdminService{},
		WithStrategyRegistry(reg),
		WithPINBackend(backend),
		WithFactorRegistry(factors),
	)

	switch {
	case srv.opts.strategyRegistry != reg:
		t.Fatal("strategyRegistry clobbered by other options")
	case srv.opts.pinBackend == nil || pin.NewLockoutBackend(srv.opts.pinBackend) != srv.opts.pinBackend:
		t.Fatal("pinBackend clobbered by other options, or stored without the SO PIN lockout")
	case srv.opts.factorRegistry != factors:
		t.Fatal("factorRegistry clobbered by other options")
	}
}
