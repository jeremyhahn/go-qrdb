// Copyright (c) 2025 Jeremy Hahn
// SPDX-License-Identifier: AGPL-3.0-or-later

package sdk

import (
	"context"
	"errors"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// newPlainClient wraps plainTransport, which implements transport.Transport
// and nothing else, so every capability probe on it fails.
func newPlainClient(t *testing.T) *Client {
	t.Helper()
	c, err := NewClient(plainTransport{})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

// capabilityPanic runs call and returns the string it panicked with, failing
// the test if it did not panic.
func capabilityPanic(t *testing.T, call func()) string {
	t.Helper()
	var msg string
	func() {
		defer func() {
			r := recover()
			if r == nil {
				t.Fatal("accessor returned on a transport lacking the capability, want a panic")
			}
			s, ok := r.(string)
			if !ok {
				t.Fatalf("panic value = %T (%v), want string", r, r)
			}
			msg = s
		}()
		call()
	}()
	return msg
}

func TestClient_CapabilityAccessors_PanicNamingTheMissingInterface(t *testing.T) {
	c := newPlainClient(t)

	tests := []struct {
		method string
		iface  string
		call   func()
	}{
		{"Batch", "transport.BatchClient", func() { c.Batch() }},
		{"Lock", "transport.LockClient", func() { c.Lock() }},
		{"TimeSeries", "transport.TimeSeriesClient", func() { c.TimeSeries() }},
		{"Vector", "transport.VectorClient", func() { c.Vector() }},
		{"Users", "transport.UserClient", func() { c.Users() }},
		{"Roles", "transport.RoleClient", func() { c.Roles() }},
		{"Certs", "transport.CertClient", func() { c.Certs() }},
		{"Invitations", "transport.InvitationClient", func() { c.Invitations() }},
		{"Database", "transport.DatabaseClient", func() { c.Database() }},
		{"DEK", "transport.DEKClient", func() { c.DEK() }},
		{"Barrier", "transport.BarrierClient", func() { c.Barrier() }},
		{"Ceremony", "transport.CeremonyClient", func() { c.Ceremony() }},
		{"Credentials", "transport.CredentialClient", func() { c.Credentials() }},
		{"ABACPolicies", "transport.ABACPolicyClient", func() { c.ABACPolicies() }},
		{"Collections", "transport.CollectionClient", func() { c.Collections() }},
		{"SealStore", "transport.SealStoreClient", func() { c.SealStore() }},
		{"AuthFactors", "transport.AuthFactorClient", func() { c.AuthFactors() }},
		{"Tenants", "transport.TenantClient", func() { c.Tenants() }},
		{"VolumePools", "transport.VolumePoolClient", func() { c.VolumePools() }},
	}
	for _, tt := range tests {
		t.Run(tt.method, func(t *testing.T) {
			want := "sdk: Client." + tt.method + ": transport does not implement " + tt.iface
			if got := capabilityPanic(t, tt.call); got != want {
				t.Fatalf("panic = %q, want %q", got, want)
			}
		})
	}
}

func TestClient_OptionalAccessors_NilWhenTransportLacksThem(t *testing.T) {
	c := newPlainClient(t)

	if c.SupportsInvitations() {
		t.Fatal("SupportsInvitations = true on a transport without InvitationClient")
	}
	if got := c.Backup(); got != nil {
		t.Fatalf("Backup = %v, want nil", got)
	}
	if got := c.Embedding(); got != nil {
		t.Fatalf("Embedding = %v, want nil", got)
	}
	if got := c.Rebalance(); got != nil {
		t.Fatalf("Rebalance = %v, want nil", got)
	}
	if got := c.UserAuthFactors(); got != nil {
		t.Fatalf("UserAuthFactors = %v, want nil", got)
	}
	if got := c.Permissions(); got != nil {
		t.Fatalf("Permissions = %v, want nil", got)
	}
	if got := c.Audit(); got != nil {
		t.Fatalf("Audit = %v, want nil", got)
	}
	if got := c.RBAC(); got != nil {
		t.Fatalf("RBAC = %v, want nil", got)
	}
	if got := c.Factors(); got != nil {
		t.Fatalf("Factors = %v, want nil", got)
	}
	if got := c.Login(); got != nil {
		t.Fatalf("Login = %v, want nil", got)
	}
	if got := c.Sessions(); got != nil {
		t.Fatalf("Sessions = %v, want nil", got)
	}
}

func TestClient_SQL_UnsupportedTransportRefusesTyped(t *testing.T) {
	c := newPlainClient(t)

	sql := c.SQL()
	if sql == nil {
		t.Fatal("SQL returned nil, want the refusing client")
	}
	res, err := sql.Query(context.Background(), "app", "SELECT 1")
	if res != nil {
		t.Fatalf("Query result = %v, want nil", res)
	}
	var nie *transport.NotImplementedError
	if !errors.As(err, &nie) || nie.Method != "SQL" {
		t.Fatalf("Query error = %T (%v), want *transport.NotImplementedError{Method: SQL}", err, err)
	}
}

func TestClient_SQL_ClosedClientRefusesTyped(t *testing.T) {
	c := newPlainClient(t)
	if err := c.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	_, err := c.SQL().Query(context.Background(), "app", "SELECT 1")
	var nie *transport.NotImplementedError
	if !errors.As(err, &nie) || nie.Method != "SQL" {
		t.Fatalf("Query error = %T (%v), want *transport.NotImplementedError{Method: SQL}", err, err)
	}
}

func TestClient_Coordination_UnsupportedTransportNamesKV(t *testing.T) {
	c := newPlainClient(t)

	coord, err := c.Coordination()
	if coord != nil {
		t.Fatalf("Coordination = %v, want nil alongside the error", coord)
	}
	var cue *CoordinationUnsupportedError
	if !errors.As(err, &cue) || cue.Capability != "kv" {
		t.Fatalf("Coordination error = %T (%v), want *CoordinationUnsupportedError{kv}", err, err)
	}
	if want := "sdk: transport does not support coordination capability: kv"; err.Error() != want {
		t.Fatalf("Error() = %q, want %q", err.Error(), want)
	}
}

func TestClient_Coordination_ClosedClient(t *testing.T) {
	c := newPlainClient(t)
	if err := c.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	coord, err := c.Coordination()
	var cce *ClientClosedError
	if coord != nil || !errors.As(err, &cce) {
		t.Fatalf("Coordination = (%v, %T %v), want (nil, *ClientClosedError)", coord, err, err)
	}
}

func TestClient_WithTenant_ClosedClient(t *testing.T) {
	c := newPlainClient(t)
	if err := c.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	kv, err := c.WithTenant("acme")
	var cce *ClientClosedError
	if kv != nil || !errors.As(err, &cce) {
		t.Fatalf("WithTenant = (%v, %T %v), want (nil, *ClientClosedError)", kv, err, err)
	}
}

func TestKVStoreAdapter_CompoundIndexRefusesTyped(t *testing.T) {
	a := &kvStoreAdapter{}
	ctx := context.Background()
	values := [][]byte{[]byte("alpha"), []byte("beta")}

	ids, err := a.QueryCompoundIndex(ctx, "team_role", values)
	if ids != nil || !errors.Is(err, kvstore.ErrIndexUnsupported) {
		t.Fatalf("QueryCompoundIndex = (%v, %v), want (nil, ErrIndexUnsupported)", ids, err)
	}
	scanned, err := a.ScanCompoundIndex(ctx, "team_role", values, nil, nil)
	if scanned != nil || !errors.Is(err, kvstore.ErrIndexUnsupported) {
		t.Fatalf("ScanCompoundIndex = (%v, %v), want (nil, ErrIndexUnsupported)", scanned, err)
	}
}

func TestMembershipDiffHelpers(t *testing.T) {
	m1 := Member{ReplicaID: 1}
	m2 := Member{ReplicaID: 2, IsLeader: true}
	m3 := Member{ReplicaID: 3}

	prev := membershipSet([]Member{m1, m2})
	cur := []Member{m2, m3}

	if got := diffMembers(cur, prev); !reflect.DeepEqual(got, []Member{m3}) {
		t.Fatalf("added = %+v, want [replica 3]", got)
	}
	if got := diffMembers(prevSlice(membershipSet(cur)), prev); !reflect.DeepEqual(got, []Member{m3}) {
		t.Fatalf("added via prevSlice = %+v, want [replica 3]", got)
	}
	if got := diffMembers(prevSlice(prev), membershipSet(cur)); !reflect.DeepEqual(got, []Member{m1}) {
		t.Fatalf("removed = %+v, want [replica 1]", got)
	}
	if got := diffMembers(cur, membershipSet(cur)); got != nil {
		t.Fatalf("diff of a set against itself = %+v, want nil", got)
	}

	back := prevSlice(prev)
	sort.Slice(back, func(i, j int) bool { return back[i].ReplicaID < back[j].ReplicaID })
	if !reflect.DeepEqual(back, []Member{m1, m2}) {
		t.Fatalf("prevSlice = %+v, want both members", back)
	}
	if got := prevSlice(nil); got == nil || len(got) != 0 {
		t.Fatalf("prevSlice(nil) = %#v, want an empty non-nil slice", got)
	}

	if got := leaderOf(cur); got != 2 {
		t.Fatalf("leaderOf = %d, want 2", got)
	}
	if got := leaderOf([]Member{m1, m3}); got != 0 {
		t.Fatalf("leaderOf without a leader = %d, want 0", got)
	}
}

func TestTypedErrors_Messages(t *testing.T) {
	tests := []struct {
		err  error
		want string
	}{
		{&ShardNotFoundError{ShardID: 42}, "sdk: shard not found in membership view: 42"},
		{&NoLeaderError{ShardID: 7}, "sdk: no leader elected for shard 7"},
		{
			&LeaderEndpointUnknownError{ShardID: 3, ReplicaID: 9, Protocol: APIProtocolREST},
			"sdk: leader replica 9 of shard 3 advertises no " + string(APIProtocolREST) + " api endpoint",
		},
		{&LeaseHeldError{Key: "locks/a"}, "sdk: lease key already held: locks/a"},
		{&LeaseLostError{Key: "locks/b"}, "sdk: lease lost before renewal was acknowledged: locks/b"},
		{&CoordinationUnsupportedError{Capability: "watch"}, "sdk: transport does not support coordination capability: watch"},
		{&SwapLostError{Key: []byte("acct/1")}, `qrdb: guarded swap lost the record "acct/1" to another writer`},
		{&SwapContendedError{Key: []byte("acct/2"), Attempts: 5}, `qrdb: guarded swap on "acct/2" gave up after 5 contended attempts`},
		{
			&SwapStalledError{Key: []byte("acct/3"), Attempts: 2},
			`qrdb: guarded swap on "acct/3" was refused without the record changing; retrying cannot succeed`,
		},
		{&SwapConfigError{Reason: "nil mutate"}, "qrdb: invalid guarded-swap request: nil mutate"},
	}
	for _, tt := range tests {
		if got := tt.err.Error(); got != tt.want {
			t.Errorf("%T.Error() = %q, want %q", tt.err, got, tt.want)
		}
	}

	// A key carrying a quote or control byte is quoted, never spliced raw.
	if got := (&SwapLostError{Key: []byte("a\"b\n")}).Error(); !strings.Contains(got, `"a\"b\n"`) {
		t.Errorf("SwapLostError did not quote its key: %q", got)
	}
}

// TestLeaseExpiredBeforeBindError_MessageCarriesTTL pins the TTL rendering,
// which the caller needs to choose a longer one.
func TestLeaseExpiredBeforeBindError_MessageCarriesTTL(t *testing.T) {
	inner := errors.New("lease not found")
	err := &LeaseExpiredBeforeBindError{Key: "locks/c", TTL: 2 * time.Second, Err: inner}
	if want := "sdk: lease expired before the key was bound (ttl 2s): locks/c"; err.Error() != want {
		t.Fatalf("Error() = %q, want %q", err.Error(), want)
	}
	if !errors.Is(err, inner) {
		t.Fatal("errors.Is did not reach the wrapped bind error")
	}
}
