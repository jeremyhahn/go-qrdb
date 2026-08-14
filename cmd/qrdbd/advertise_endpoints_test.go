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

package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/cluster"
)

func TestAdvertisedAPIEndpoints_ExplicitAdvertiseHostWins(t *testing.T) {
	f := &startFlags{
		advertiseHost: "10.0.0.9",
		raftAddress:   "10.0.0.2:63001",
		enableREST:    true,
		adminPort:     63002,
	}
	endpoints, err := advertisedAPIEndpoints(f)
	require.NoError(t, err)
	require.Len(t, endpoints, 1)
	assert.Equal(t, "10.0.0.9:63002", endpoints[cluster.APIProtocolREST])
}

func TestAdvertisedAPIEndpoints_FallsBackToRaftAddressHost(t *testing.T) {
	f := &startFlags{
		advertiseHost: "",
		raftAddress:   "10.0.0.2:63001",
		enableREST:    true,
		adminPort:     63002,
	}
	endpoints, err := advertisedAPIEndpoints(f)
	require.NoError(t, err)
	require.Len(t, endpoints, 1)
	assert.Equal(t, "10.0.0.2:63002", endpoints[cluster.APIProtocolREST])
}

func TestAdvertisedAPIEndpoints_OnlyEnabledProtocolsIncluded(t *testing.T) {
	f := &startFlags{
		advertiseHost: "10.0.0.2",
		enableREST:    true,
		adminPort:     63002,
		enableGRPC:    false,
		grpcPort:      63003,
		enableQUIC:    true,
		quicPort:      63004,
		enableMCP:     false,
		mcpPort:       63005,
	}
	endpoints, err := advertisedAPIEndpoints(f)
	require.NoError(t, err)
	require.Len(t, endpoints, 2)
	assert.Equal(t, "10.0.0.2:63002", endpoints[cluster.APIProtocolREST])
	assert.Equal(t, "10.0.0.2:63004", endpoints[cluster.APIProtocolQUIC])
	_, hasGRPC := endpoints[cluster.APIProtocolGRPC]
	assert.False(t, hasGRPC)
	_, hasMCP := endpoints[cluster.APIProtocolMCP]
	assert.False(t, hasMCP)
}

func TestAdvertisedAPIEndpoints_EnabledProtocolZeroPortOmitted(t *testing.T) {
	f := &startFlags{
		advertiseHost: "10.0.0.2",
		enableREST:    true,
		adminPort:     63002,
		enableGRPC:    true,
		grpcPort:      0,
	}
	endpoints, err := advertisedAPIEndpoints(f)
	require.NoError(t, err)
	require.Len(t, endpoints, 1)
	assert.Equal(t, "10.0.0.2:63002", endpoints[cluster.APIProtocolREST])
	_, hasGRPC := endpoints[cluster.APIProtocolGRPC]
	assert.False(t, hasGRPC)
}

func TestAdvertisedAPIEndpoints_WildcardRaftHostNoAdvertiseHost(t *testing.T) {
	f := &startFlags{
		advertiseHost: "",
		raftAddress:   "0.0.0.0:63001",
		enableREST:    true,
		adminPort:     63002,
	}
	endpoints, err := advertisedAPIEndpoints(f)
	require.NoError(t, err)
	assert.Nil(t, endpoints)
}

func TestAdvertisedAPIEndpoints_AllProtocolsDisabled(t *testing.T) {
	f := &startFlags{
		advertiseHost: "10.0.0.2",
		raftAddress:   "10.0.0.2:63001",
	}
	endpoints, err := advertisedAPIEndpoints(f)
	require.NoError(t, err)
	assert.Nil(t, endpoints)
}
