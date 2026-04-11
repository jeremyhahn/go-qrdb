//go:build qrdb_vector

// Copyright (c) 2025 Jeremy Hahn
// Copyright (c) 2025 Automate The Things, LLC
//
// This file is part of go-qrdb.
//
// go-qrdb is dual-licensed:
//
// 1. GNU Affero General Public License v3.0 (AGPL-3.0)
//    See LICENSE file or visit https://www.gnu.org/licenses/agpl-3.0.html
//
// 2. Commercial License
//    Contact licensing@automatethethings.com for commercial licensing options.

package gpu

import (
	"fmt"

	"github.com/jeremyhahn/go-qrdb/pkg/interfaces"
	"github.com/jeremyhahn/go-qrdb/pkg/vector/gpu/remote"
)

// RemoteGPUIndexFactory implements interfaces.HNSWIndexFactory. When remote
// execution is enabled it creates RemoteGPUIndex instances that delegate to the
// configured VectorIndexService. When remote execution is disabled or the config
// is nil it falls back to the provided CPU factory.
type RemoteGPUIndexFactory struct {
	config   *RemoteGPUConfig
	fallback interfaces.HNSWIndexFactory
}

// NewRemoteGPUIndexFactory returns a factory that creates RemoteGPUIndex
// instances when config.Enabled is true, otherwise delegating to fallback.
// Pass nil as fallback only when remote execution is guaranteed to be active.
func NewRemoteGPUIndexFactory(config *RemoteGPUConfig, fallback interfaces.HNSWIndexFactory) *RemoteGPUIndexFactory {
	return &RemoteGPUIndexFactory{
		config:   config,
		fallback: fallback,
	}
}

// CreateIndex creates a RemoteGPUIndex when remote execution is enabled,
// otherwise it delegates to the fallback factory. If remote is disabled and no
// fallback was provided, ErrNoFallbackFactory is returned.
func (f *RemoteGPUIndexFactory) CreateIndex(config *interfaces.HNSWConfig) (interfaces.HNSWIndex, error) {
	if f.config != nil && f.config.Enabled {
		idx, err := remote.NewRemoteGPUIndex(remoteConfigToConnConfig(f.config))
		if err != nil {
			return nil, fmt.Errorf("remote gpu factory: %w", err)
		}
		return idx, nil
	}
	return f.createFallbackIndex(config)
}

// createFallbackIndex delegates to the CPU factory, returning
// ErrNoFallbackFactory when none was provided.
func (f *RemoteGPUIndexFactory) createFallbackIndex(config *interfaces.HNSWConfig) (interfaces.HNSWIndex, error) {
	if f.fallback == nil {
		return nil, fmt.Errorf("remote gpu factory: %w", ErrNoFallbackFactory)
	}
	return f.fallback.CreateIndex(config)
}

// remoteConfigToConnConfig converts a RemoteGPUConfig to the remote.Config
// type used by the remote package, avoiding a circular import between gpu and
// gpu/remote.
func remoteConfigToConnConfig(c *RemoteGPUConfig) *remote.Config {
	return &remote.Config{
		Endpoint:  c.Endpoint,
		TLSCert:   c.TLSCert,
		TLSKey:    c.TLSKey,
		TLSCA:     c.TLSCA,
		TimeoutMs: c.TimeoutMs,
	}
}
