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

package node

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	qrdbconfig "github.com/jeremyhahn/go-qrdb/pkg/config"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
)

// deviceStrategy stands in for a composer's device-held strategy: only its
// name differs from the software one it wraps.
type deviceStrategy struct {
	seal.SealingStrategy
	id seal.StrategyID
}

func (s deviceStrategy) ID() seal.StrategyID { return s.id }

func newDeviceStrategy(id seal.StrategyID) deviceStrategy {
	return deviceStrategy{SealingStrategy: seal.NewNoneStrategy(), id: id}
}

func sealStrategyConfig(strategy string) *qrdbconfig.Config {
	cfg := qrdbconfig.DefaultConfig()
	cfg.Seal.Strategy = strategy
	return cfg
}

func strategyIDs(strategies []seal.SealingStrategy) []seal.StrategyID {
	ids := make([]seal.StrategyID, 0, len(strategies))
	for _, s := range strategies {
		ids = append(ids, s.ID())
	}
	return ids
}

func TestResolveNodeSealStrategies_InjectedStrategySelected(t *testing.T) {
	ctx := context.Background()
	injected := newDeviceStrategy("tpm2")
	nOpts := &nodeOptions{sealStrategy: injected}

	got, err := resolveNodeSealStrategies(ctx, sealStrategyConfig(""), nOpts, nil, seal.NewMemoryBackend(), "root-key")
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, injected, got[0])

	got, err = resolveNodeSealStrategies(ctx, sealStrategyConfig("passphrase"), nOpts, nil, seal.NewMemoryBackend(), "root-key")
	require.NoError(t, err)
	assert.Equal(t, []seal.StrategyID{"tpm2"}, strategyIDs(got))
}

func TestResolveNodeSealStrategies_ConfiguredNoneOrShamirWins(t *testing.T) {
	ctx := context.Background()
	nOpts := &nodeOptions{sealStrategy: newDeviceStrategy("tpm2")}

	got, err := resolveNodeSealStrategies(ctx, sealStrategyConfig("none"), nOpts, nil, seal.NewMemoryBackend(), "root-key")
	require.NoError(t, err)
	assert.Equal(t, []seal.StrategyID{seal.StrategyNone}, strategyIDs(got))

	cfg := sealStrategyConfig("shamir")
	cfg.Seal.ShamirThreshold, cfg.Seal.ShamirTotal = 2, 3
	got, err = resolveNodeSealStrategies(ctx, cfg, nOpts, nil, seal.NewMemoryBackend(), "root-key")
	require.NoError(t, err)
	assert.Equal(t, []seal.StrategyID{seal.StrategyShamir}, strategyIDs(got))
}

// A composer's registry (server.WithStrategyRegistry) reaches the system
// barrier: a strategy it registers under the configured name replaces the
// software one.
func TestResolveNodeSealStrategies_ComposerRegistry(t *testing.T) {
	device := newDeviceStrategy(seal.StrategyPassphrase)
	composer := seal.NewStrategyRegistry()
	require.NoError(t, composer.Register(seal.StrategyPassphrase, func() (seal.SealingStrategy, error) { return device, nil }))

	got, err := resolveNodeSealStrategies(context.Background(), sealStrategyConfig(""), nil, composer, seal.NewMemoryBackend(), "root-key")
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, device, got[0])
}

func TestResolveNodeSealStrategies_InvalidShamir(t *testing.T) {
	cfg := sealStrategyConfig("shamir")
	cfg.Seal.ShamirThreshold, cfg.Seal.ShamirTotal = 5, 3
	_, err := resolveNodeSealStrategies(context.Background(), cfg, nil, nil, seal.NewMemoryBackend(), "root-key")
	require.Error(t, err)
}

// listsAGhost is a composer whose listing names a strategy its lookup does
// not have.
type listsAGhost struct{ *seal.DefaultStrategyRegistry }

func (listsAGhost) ListAvailable() []seal.StrategyID { return []seal.StrategyID{"ghost"} }

// namedStrategy is a sealing strategy reporting a fixed ID.
type namedStrategy struct {
	seal.SealingStrategy
	id seal.StrategyID
}

func (s namedStrategy) ID() seal.StrategyID { return s.id }

// TestResolveNodeSealStrategies_RegistryFaults: a composer that cannot
// produce a strategy it lists, and an injected strategy with no name, each
// fail resolution with the registry's typed error.
func TestResolveNodeSealStrategies_RegistryFaults(t *testing.T) {
	ctx := context.Background()
	cfg := qrdbconfig.DefaultConfig()

	var missing *seal.StrategyNotRegisteredError
	_, err := resolveNodeSealStrategies(ctx, cfg, nil, listsAGhost{seal.NewStrategyRegistry()}, nil, "root-key")
	if !errors.As(err, &missing) || missing.Name != "ghost" {
		t.Fatalf("resolve with a ghost composer = %v; want StrategyNotRegisteredError for ghost", err)
	}

	var register *seal.StrategyRegisterError
	_, err = resolveNodeSealStrategies(ctx, cfg, &nodeOptions{sealStrategy: namedStrategy{}}, nil, nil, "root-key")
	if !errors.As(err, &register) {
		t.Fatalf("resolve with an unnamed injected strategy = %v; want StrategyRegisterError", err)
	}
}
