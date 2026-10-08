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
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/audit"
	"github.com/jeremyhahn/go-qrdb/pkg/authfactors"
	"github.com/jeremyhahn/go-qrdb/pkg/pin/pintest"
	"github.com/jeremyhahn/go-qrdb/pkg/server"
	"github.com/jeremyhahn/go-qrdb/pkg/server/lifecycle"
)

// TestBuildServerOpts_ServesTheReplicatedAuditChainOnTheNetworkTransports: the
// node's replicated audit logger reaches the server options, so REST, gRPC,
// QUIC, Unix and MCP serve the replicated query and chain verification as the
// embedded transport and qrdbd do.
func TestBuildServerOpts_ServesTheReplicatedAuditChainOnTheNetworkTransports(t *testing.T) {
	n := newSeamNode(t, nil)
	svcNode := n.svcNode.Load()
	require.NotNil(t, svcNode)
	require.NotNil(t, svcNode.ReplicatedAudit, "BuildServices always builds the replicated audit logger")

	opts := buildServerOpts(svcNode, nil, nil, nil, true)
	require.Same(t, svcNode.ReplicatedAudit, server.ResolveCryptoOptions(opts...).ReplicatedAuditLogger)
}

// TestBuildServerOpts_CallerReplicatedAuditLoggerWins: caller options come last,
// so an embedder's own replicated audit logger replaces the node's.
func TestBuildServerOpts_CallerReplicatedAuditLoggerWins(t *testing.T) {
	n := newSeamNode(t, nil)
	svcNode := n.svcNode.Load()
	require.NotNil(t, svcNode)

	own := *svcNode.ReplicatedAudit
	opts := buildServerOpts(svcNode, nil, nil, []server.ServerOption{server.WithReplicatedAuditLogger(&own)}, true)
	require.Same(t, &own, server.ResolveCryptoOptions(opts...).ReplicatedAuditLogger)
}

// TestWithHolderInfra_CarriesTheSealedWindowsDependencies: the dependencies the
// sealed window's ceremony service is configured with -- the PIN backend, the
// factor registry, the ceremony CA, the audit trail and the holder-act limiter
// -- come from the node's server options, and buildSealServices (which builds
// that graph) takes them from the same place as buildServices.
func TestWithHolderInfra_CarriesTheSealedWindowsDependencies(t *testing.T) {
	backend := pintest.NewBackend(t)
	registry := authfactors.NewFactorRegistry()
	trail := audit.NewRingBuffer(8)
	limiter := &recordingLimiter{allow: true}

	n := &Node{}
	infra, err := n.withHolderInfra(lifecycle.Infra{}, nil, []server.ServerOption{
		server.WithPINBackend(backend),
		server.WithFactorRegistry(registry),
		server.WithAuditLogger(trail),
		server.WithHolderActLimiter(limiter),
	})
	require.NoError(t, err)
	require.NotNil(t, infra.PINBackend)
	require.Same(t, registry, infra.FactorRegistry)
	require.Same(t, trail, infra.AuditLogger)
	require.Same(t, limiter, infra.HolderActLimiter)
}
