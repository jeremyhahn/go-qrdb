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

package cluster

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// APIProtocol.Valid / String
// ---------------------------------------------------------------------------

func TestAPIProtocol_Valid_Success(t *testing.T) {
	for _, p := range APIProtocols {
		assert.Truef(t, p.Valid(), "protocol %q should be valid", p)
	}
}

func TestAPIProtocol_Valid_Invalid(t *testing.T) {
	tests := []APIProtocol{
		APIProtocol("bogus"),
		APIProtocol(""),
	}
	for _, p := range tests {
		assert.Falsef(t, p.Valid(), "protocol %q should be invalid", p)
	}
}

func TestAPIProtocol_String(t *testing.T) {
	assert.Equal(t, "rest", APIProtocolREST.String())
	assert.Equal(t, "grpc", APIProtocolGRPC.String())
	assert.Equal(t, "quic", APIProtocolQUIC.String())
	assert.Equal(t, "mcp", APIProtocolMCP.String())
}

// ---------------------------------------------------------------------------
// ParseAPIProtocol
// ---------------------------------------------------------------------------

func TestParseAPIProtocol_Success(t *testing.T) {
	for _, p := range APIProtocols {
		got, err := ParseAPIProtocol(string(p))
		require.NoError(t, err)
		assert.Equal(t, p, got)
	}
}

func TestParseAPIProtocol_Unknown(t *testing.T) {
	_, err := ParseAPIProtocol("carrier-pigeon")
	require.Error(t, err)

	var upe *UnknownAPIProtocolError
	require.True(t, errors.As(err, &upe))
	assert.Equal(t, "carrier-pigeon", upe.Name)
	assert.Contains(t, err.Error(), "carrier-pigeon")
}

// ---------------------------------------------------------------------------
// Member.APIEndpoint
// ---------------------------------------------------------------------------

func TestMember_APIEndpoint_Present(t *testing.T) {
	m := Member{
		NodeID: 1,
		APIEndpoints: map[APIProtocol]string{
			APIProtocolREST: "10.0.0.2:63002",
		},
	}
	addr, ok := m.APIEndpoint(APIProtocolREST)
	assert.True(t, ok)
	assert.Equal(t, "10.0.0.2:63002", addr)
}

func TestMember_APIEndpoint_AbsentProtocol(t *testing.T) {
	m := Member{
		NodeID: 1,
		APIEndpoints: map[APIProtocol]string{
			APIProtocolREST: "10.0.0.2:63002",
		},
	}
	addr, ok := m.APIEndpoint(APIProtocolGRPC)
	assert.False(t, ok)
	assert.Equal(t, "", addr)
}

func TestMember_APIEndpoint_EmptyMappedAddress(t *testing.T) {
	m := Member{
		NodeID: 1,
		APIEndpoints: map[APIProtocol]string{
			APIProtocolREST: "",
		},
	}
	addr, ok := m.APIEndpoint(APIProtocolREST)
	assert.False(t, ok)
	assert.Equal(t, "", addr)
}

func TestMember_APIEndpoint_NilMap(t *testing.T) {
	m := Member{NodeID: 1}
	require.NotPanics(t, func() {
		addr, ok := m.APIEndpoint(APIProtocolREST)
		assert.False(t, ok)
		assert.Equal(t, "", addr)
	})
}

// ---------------------------------------------------------------------------
// ValidateAPIEndpoints
// ---------------------------------------------------------------------------

func TestValidateAPIEndpoints_Success(t *testing.T) {
	tests := map[string]map[APIProtocol]string{
		"nil map":   nil,
		"empty map": {},
		"valid map": {
			APIProtocolREST: "10.0.0.2:63002",
			APIProtocolGRPC: "10.0.0.2:63003",
		},
	}
	for name, endpoints := range tests {
		t.Run(name, func(t *testing.T) {
			assert.NoError(t, ValidateAPIEndpoints(endpoints))
		})
	}
}

func TestValidateAPIEndpoints_UnknownProtocol(t *testing.T) {
	err := ValidateAPIEndpoints(map[APIProtocol]string{
		APIProtocol("bogus"): "10.0.0.2:63002",
	})
	require.Error(t, err)
	var upe *UnknownAPIProtocolError
	require.True(t, errors.As(err, &upe))
	assert.Equal(t, "bogus", upe.Name)
}

func TestValidateAPIEndpoints_EmptyAddress(t *testing.T) {
	err := ValidateAPIEndpoints(map[APIProtocol]string{
		APIProtocolREST: "",
	})
	require.Error(t, err)
	var eae *EmptyAPIEndpointError
	require.True(t, errors.As(err, &eae))
	assert.Equal(t, APIProtocolREST, eae.Protocol)
}

// ---------------------------------------------------------------------------
// AdvertisableHost
// ---------------------------------------------------------------------------

func TestAdvertisableHost_Unadvertisable(t *testing.T) {
	for _, host := range []string{"", "0.0.0.0", "::", "[::]"} {
		assert.Falsef(t, AdvertisableHost(host), "host %q should not be advertisable", host)
	}
}

func TestAdvertisableHost_Advertisable(t *testing.T) {
	for _, host := range []string{"10.0.0.2", "example.com", "127.0.0.1"} {
		assert.Truef(t, AdvertisableHost(host), "host %q should be advertisable", host)
	}
}

// ---------------------------------------------------------------------------
// BuildAPIEndpoints
// ---------------------------------------------------------------------------

func TestBuildAPIEndpoints_Subset(t *testing.T) {
	endpoints, err := BuildAPIEndpoints("10.0.0.2", map[APIProtocol]int{
		APIProtocolREST: 63002,
		APIProtocolGRPC: 63003,
	})
	require.NoError(t, err)
	require.Len(t, endpoints, 2)
	assert.Equal(t, "10.0.0.2:63002", endpoints[APIProtocolREST])
	assert.Equal(t, "10.0.0.2:63003", endpoints[APIProtocolGRPC])
}

func TestBuildAPIEndpoints_NonPositivePortOmitted(t *testing.T) {
	endpoints, err := BuildAPIEndpoints("10.0.0.2", map[APIProtocol]int{
		APIProtocolREST: 63002,
		APIProtocolGRPC: 0,
		APIProtocolQUIC: -1,
	})
	require.NoError(t, err)
	require.Len(t, endpoints, 1)
	assert.Equal(t, "10.0.0.2:63002", endpoints[APIProtocolREST])
	_, hasGRPC := endpoints[APIProtocolGRPC]
	assert.False(t, hasGRPC)
	_, hasQUIC := endpoints[APIProtocolQUIC]
	assert.False(t, hasQUIC)
}

func TestBuildAPIEndpoints_IPv6HostBracketed(t *testing.T) {
	endpoints, err := BuildAPIEndpoints("fe80::1", map[APIProtocol]int{
		APIProtocolREST: 63002,
	})
	require.NoError(t, err)
	require.Len(t, endpoints, 1)
	assert.Equal(t, "[fe80::1]:63002", endpoints[APIProtocolREST])
}

func TestBuildAPIEndpoints_WildcardHost(t *testing.T) {
	for _, host := range []string{"", "0.0.0.0", "::", "[::]"} {
		endpoints, err := BuildAPIEndpoints(host, map[APIProtocol]int{
			APIProtocolREST: 63002,
		})
		require.NoError(t, err)
		assert.Nilf(t, endpoints, "host %q should yield a nil map", host)
	}
}

func TestBuildAPIEndpoints_UnknownProtocolInPorts(t *testing.T) {
	_, err := BuildAPIEndpoints("10.0.0.2", map[APIProtocol]int{
		APIProtocol("bogus"): 63002,
	})
	require.Error(t, err)
	var upe *UnknownAPIProtocolError
	require.True(t, errors.As(err, &upe))
	assert.Equal(t, "bogus", upe.Name)
}

func TestBuildAPIEndpoints_AllNonPositivePorts(t *testing.T) {
	endpoints, err := BuildAPIEndpoints("10.0.0.2", map[APIProtocol]int{
		APIProtocolREST: 0,
		APIProtocolGRPC: -5,
	})
	require.NoError(t, err)
	assert.Nil(t, endpoints)
}

// ---------------------------------------------------------------------------
// HostFromAddress
// ---------------------------------------------------------------------------

func TestHostFromAddress_Success(t *testing.T) {
	assert.Equal(t, "10.0.0.2", HostFromAddress("10.0.0.2:63001"))
	assert.Equal(t, "fe80::1", HostFromAddress("[fe80::1]:63001"))
}

func TestHostFromAddress_Invalid(t *testing.T) {
	assert.Equal(t, "", HostFromAddress("0.0.0.0:63001"))
	assert.Equal(t, "", HostFromAddress("no-port"))
	assert.Equal(t, "", HostFromAddress(""))
}

// ---------------------------------------------------------------------------
// NewAdvertiser
// ---------------------------------------------------------------------------

func TestNewAdvertiser_Success(t *testing.T) {
	store := newFakeStore()
	self := Member{
		NodeID:  1,
		Address: "10.0.0.2:63001",
		APIEndpoints: map[APIProtocol]string{
			APIProtocolREST: "10.0.0.2:63002",
		},
	}
	a, err := NewAdvertiser(store, self, AdvertiseConfig{})
	require.NoError(t, err)
	require.NotNil(t, a)
	assert.Equal(t, self, a.Self())
}

func TestNewAdvertiser_NilStore(t *testing.T) {
	self := Member{NodeID: 1, Address: "10.0.0.2:63001"}
	a, err := NewAdvertiser(nil, self, AdvertiseConfig{})
	require.Error(t, err)
	assert.Nil(t, a)
	var me *MembershipError
	require.True(t, errors.As(err, &me))
}

func TestNewAdvertiser_UnknownProtocolInEndpoints(t *testing.T) {
	store := newFakeStore()
	self := Member{
		NodeID:  1,
		Address: "10.0.0.2:63001",
		APIEndpoints: map[APIProtocol]string{
			APIProtocol("bogus"): "10.0.0.2:63002",
		},
	}
	a, err := NewAdvertiser(store, self, AdvertiseConfig{})
	require.Error(t, err)
	assert.Nil(t, a)
	var upe *UnknownAPIProtocolError
	require.True(t, errors.As(err, &upe))
}

func TestNewAdvertiser_WithAdvertiseLoggerNilIgnored(t *testing.T) {
	store := newFakeStore()
	self := Member{NodeID: 1, Address: "10.0.0.2:63001"}
	var a *Advertiser
	require.NotPanics(t, func() {
		var err error
		a, err = NewAdvertiser(store, self, AdvertiseConfig{}, WithAdvertiseLogger(nil))
		require.NoError(t, err)
	})
	require.NotNil(t, a)
	assert.NotNil(t, a.logger, "a nil logger option should not clobber the default logger")
}

// ---------------------------------------------------------------------------
// Advertiser.Self
// ---------------------------------------------------------------------------

func TestAdvertiser_Self(t *testing.T) {
	store := newFakeStore()
	self := Member{
		NodeID:  7,
		Address: "10.0.0.2:63001",
		APIEndpoints: map[APIProtocol]string{
			APIProtocolGRPC: "10.0.0.2:63003",
		},
	}
	a, err := NewAdvertiser(store, self, AdvertiseConfig{})
	require.NoError(t, err)
	assert.Equal(t, self, a.Self())
}

// ---------------------------------------------------------------------------
// Advertiser.Register
// ---------------------------------------------------------------------------

func TestAdvertiser_Register_WritesMember(t *testing.T) {
	store := newFakeStore()
	self := Member{
		NodeID:  3,
		Address: "10.0.0.2:63001",
		APIEndpoints: map[APIProtocol]string{
			APIProtocolREST: "10.0.0.2:63002",
			APIProtocolGRPC: "10.0.0.2:63003",
		},
	}
	a, err := NewAdvertiser(store, self, AdvertiseConfig{})
	require.NoError(t, err)

	require.NoError(t, a.Register(context.Background()))

	data, ok := store.puts[self.NodeID]
	require.True(t, ok)

	var stored Member
	require.NoError(t, json.Unmarshal(data, &stored))
	assert.Equal(t, self, stored)
}

func TestAdvertiser_Register_StoreErrorWrapped(t *testing.T) {
	store := newFakeStore()
	store.putErr = errors.New("put failed")
	self := Member{NodeID: 3, Address: "10.0.0.2:63001"}
	a, err := NewAdvertiser(store, self, AdvertiseConfig{})
	require.NoError(t, err)

	err = a.Register(context.Background())
	require.Error(t, err)
	var ce *MembershipCauseError
	require.True(t, errors.As(err, &ce))
	assert.ErrorIs(t, err, store.putErr)
}

// ---------------------------------------------------------------------------
// Backward compatibility: no APIEndpoints
// ---------------------------------------------------------------------------

func TestMember_MarshalUnmarshal_NoAPIEndpoints(t *testing.T) {
	m := Member{NodeID: 9, Address: "10.0.0.2:63001"}

	data, err := json.Marshal(m)
	require.NoError(t, err)
	assert.False(t, strings.Contains(string(data), "api_endpoints"),
		"marshaled member with no endpoints should omit api_endpoints key: %s", data)

	var out Member
	require.NoError(t, json.Unmarshal(data, &out))
	assert.Nil(t, out.APIEndpoints)
	assert.Equal(t, m, out)
}

// ---------------------------------------------------------------------------
// Advertiser.Start / Close
// ---------------------------------------------------------------------------

func TestAdvertiser_Start_RegistersAndKeepsAlive(t *testing.T) {
	store := newFakeStore()
	self := Member{
		NodeID:  4,
		Address: "10.0.0.2:63001",
		APIEndpoints: map[APIProtocol]string{
			APIProtocolREST: "10.0.0.2:63002",
		},
	}
	a, err := NewAdvertiser(store, self, AdvertiseConfig{
		LeaseTTL:          200 * time.Millisecond,
		KeepAliveInterval: 10 * time.Millisecond,
	}, WithAdvertiseLogger(slog.Default()))
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	require.NoError(t, a.Start(ctx))
	require.Equal(t, 1, store.putCount())

	// keepAlive should refresh the registration at least once more.
	require.Eventually(t, func() bool {
		return store.putCallCount() >= 2
	}, 500*time.Millisecond, 5*time.Millisecond, "keepAlive did not refresh the registration")

	require.NoError(t, a.Close())
}

func TestAdvertiser_Start_SecondStartErrors(t *testing.T) {
	store := newFakeStore()
	self := Member{NodeID: 5, Address: "10.0.0.2:63001"}
	a, err := NewAdvertiser(store, self, AdvertiseConfig{
		KeepAliveInterval: 20 * time.Millisecond,
	})
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	require.NoError(t, a.Start(ctx))
	defer func() { require.NoError(t, a.Close()) }()

	err = a.Start(ctx)
	require.Error(t, err)
	var me *MembershipError
	require.True(t, errors.As(err, &me))
}

func TestAdvertiser_Start_OnClosedErrors(t *testing.T) {
	store := newFakeStore()
	self := Member{NodeID: 6, Address: "10.0.0.2:63001"}
	a, err := NewAdvertiser(store, self, AdvertiseConfig{})
	require.NoError(t, err)

	require.NoError(t, a.Close())

	err = a.Start(context.Background())
	require.Error(t, err)
	var me *MembershipError
	require.True(t, errors.As(err, &me))
}

func TestAdvertiser_Start_RegistrationFailurePropagates(t *testing.T) {
	store := newFakeStore()
	store.putErr = errors.New("registration boom")
	self := Member{NodeID: 8, Address: "10.0.0.2:63001"}
	a, err := NewAdvertiser(store, self, AdvertiseConfig{})
	require.NoError(t, err)

	err = a.Start(context.Background())
	require.Error(t, err)
	assert.ErrorIs(t, err, store.putErr)
}

func TestAdvertiser_Close_IdempotentAndDeregisters(t *testing.T) {
	store := newFakeStore()
	self := Member{NodeID: 10, Address: "10.0.0.2:63001"}
	a, err := NewAdvertiser(store, self, AdvertiseConfig{
		KeepAliveInterval: 20 * time.Millisecond,
	})
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	require.NoError(t, a.Start(ctx))

	require.NoError(t, a.Close())
	assert.Equal(t, 1, store.deleteCount())

	// Second Close must not panic or hang, and must not deregister again.
	require.NotPanics(t, func() {
		require.NoError(t, a.Close())
	})
	assert.Equal(t, 1, store.deleteCount())
}

func TestAdvertiser_Close_NeverStartedIsSafe(t *testing.T) {
	store := newFakeStore()
	self := Member{NodeID: 11, Address: "10.0.0.2:63001"}
	a, err := NewAdvertiser(store, self, AdvertiseConfig{})
	require.NoError(t, err)

	require.NotPanics(t, func() {
		require.NoError(t, a.Close())
	})
	// Close only deregisters after a successful Start.
	assert.Equal(t, 0, store.deleteCount())
}
