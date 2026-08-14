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
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/authfactors"
	"github.com/jeremyhahn/go-qrdb/pkg/pin"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
)

// TestWithSealStrategy_Applied asserts that an injected SealingStrategy
// is reachable from the server's serverOptions for the lifecycle layer
// to consume at barrier-initialization time.
func TestWithSealStrategy_Applied(t *testing.T) {
	strategy := seal.NewNoneStrategy()
	srv := New(testConfig(true, true), newMockKVService(), &mockAdminService{},
		WithSealStrategy(strategy),
	)
	if srv.opts.sealStrategy != strategy {
		t.Fatal("sealStrategy not stored in opts")
	}
}

// TestWithStrategyRegistry_Applied asserts that an injected
// StrategyRegistry is reachable from the server's serverOptions and that
// the daemon can resolve names through it.
func TestWithStrategyRegistry_Applied(t *testing.T) {
	reg := seal.NewDefaultStrategyRegistry()
	srv := New(testConfig(true, true), newMockKVService(), &mockAdminService{},
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
	srv := New(testConfig(true, true), newMockKVService(), &mockAdminService{},
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

// TestWithFactorRegistry_Applied asserts that an injected FactorRegistry
// is reachable from the server's serverOptions for the AuthFactorService.
func TestWithFactorRegistry_Applied(t *testing.T) {
	reg := authfactors.NewFactorRegistry()
	srv := New(testConfig(true, true), newMockKVService(), &mockAdminService{},
		WithFactorRegistry(reg),
	)
	if srv.opts.factorRegistry != reg {
		t.Fatal("factorRegistry not stored in opts")
	}
	if len(srv.opts.factorRegistry.List()) != 0 {
		t.Fatalf("fresh registry should be empty, got %v", srv.opts.factorRegistry.List())
	}
}

// TestWithAllInjections_Compose asserts that all four Phase-3 injections
// coexist on the same server without conflicting.
func TestWithAllInjections_Compose(t *testing.T) {
	strategy := seal.NewNoneStrategy()
	reg := seal.NewDefaultStrategyRegistry()
	backend, _ := pin.NewSoftwareBackend(nil, pin.DefaultHashConfig())
	factors := authfactors.NewFactorRegistry()

	srv := New(testConfig(true, true), newMockKVService(), &mockAdminService{},
		WithSealStrategy(strategy),
		WithStrategyRegistry(reg),
		WithPINBackend(backend),
		WithFactorRegistry(factors),
	)

	switch {
	case srv.opts.sealStrategy != strategy:
		t.Fatal("sealStrategy clobbered by other options")
	case srv.opts.strategyRegistry != reg:
		t.Fatal("strategyRegistry clobbered by other options")
	case srv.opts.pinBackend != backend:
		t.Fatal("pinBackend clobbered by other options")
	case srv.opts.factorRegistry != factors:
		t.Fatal("factorRegistry clobbered by other options")
	}
}
