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

package service

import (
	"context"
	"errors"
	"strings"
	"testing"

	quicraft "github.com/jeremyhahn/go-quicraft/pkg"

	"github.com/jeremyhahn/go-qrdb/pkg/statedigest"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
)

// digestHost is a mockHost replicating the given shards, each answering a
// statedigest.Query with a digest whose applied index is the shard ID and
// whose records are the shard's records.
func digestHost(id string, records map[uint64][][2]string) *mockHost {
	return &mockHost{
		idFn: func() string { return id },
		getNodeHostInfoFn: func(quicraft.NodeHostInfoOption) *quicraft.NodeHostInfo {
			info := &quicraft.NodeHostInfo{}
			for shardID := range records {
				info.ShardInfoList = append(info.ShardInfoList, quicraft.ShardInfo{ShardID: shardID, ReplicaID: 2})
			}
			return info
		},
		staleReadFn: func(_ context.Context, shardID uint64, query interface{}) (interface{}, error) {
			if _, ok := query.(statedigest.Query); !ok {
				return nil, errors.New("unexpected query")
			}
			b := statedigest.NewBuilder()
			for _, kv := range records[shardID] {
				if err := b.Add([]byte(kv[0]), []byte(kv[1])); err != nil {
					return nil, err
				}
			}
			return b.Finish(shardID), nil
		},
	}
}

// staticTenantHosts is a TenantReplicaHosts over a fixed map.
type staticTenantHosts map[string]Host

func (h staticTenantHosts) StartedHosts() map[string]Host { return h }

func newStateHashAdmin(host Host) *AdminService {
	return NewAdminService(host).(*AdminService)
}

func TestStateHashes_EverySystemShardSorted(t *testing.T) {
	admin := newStateHashAdmin(digestHost("node-a", map[uint64][][2]string{
		7: {{"a", "1"}},
		1: {{"a", "1"}, {"b", "2"}},
	}))

	resp, err := admin.StateHashes(context.Background(), nil)
	if err != nil {
		t.Fatalf("StateHashes: %v", err)
	}
	if resp.NodeID != "node-a" {
		t.Fatalf("NodeID = %q, want node-a", resp.NodeID)
	}
	if len(resp.Replicas) != 2 || resp.Replicas[0].ShardID != 1 || resp.Replicas[1].ShardID != 7 {
		t.Fatalf("replicas = %+v, want shards [1 7]", resp.Replicas)
	}
	r := resp.Replicas[0]
	if r.TenantID != "" || r.ReplicaID != 2 || r.AppliedIndex != 1 || r.Records != 2 || r.Error != "" {
		t.Fatalf("shard 1 entry = %+v", r)
	}
	if len(r.Hash) != 64 || strings.ToLower(r.Hash) != r.Hash {
		t.Fatalf("hash %q is not 64 lowercase hex digits", r.Hash)
	}
	if resp.Replicas[0].Hash == resp.Replicas[1].Hash {
		t.Fatal("shards with different records report the same hash")
	}
}

func TestStateHashes_ShardFilter(t *testing.T) {
	admin := newStateHashAdmin(digestHost("n", map[uint64][][2]string{1: nil, 3: nil, 5: nil}))

	resp, err := admin.StateHashes(context.Background(), []uint64{5, 3, 5})
	if err != nil {
		t.Fatalf("StateHashes: %v", err)
	}
	if len(resp.Replicas) != 2 || resp.Replicas[0].ShardID != 3 || resp.Replicas[1].ShardID != 5 {
		t.Fatalf("replicas = %+v, want shards [3 5]", resp.Replicas)
	}

	resp, err = admin.StateHashes(context.Background(), []uint64{99})
	if err != nil {
		t.Fatalf("unknown shard: %v, want no error", err)
	}
	if len(resp.Replicas) != 0 {
		t.Fatalf("unknown shard selected %+v, want nothing", resp.Replicas)
	}
}

func TestStateHashes_TenantHostsSortedAfterSystem(t *testing.T) {
	admin := newStateHashAdmin(digestHost("node-a", map[uint64][][2]string{2: nil}))
	admin.SetTenantReplicaHosts(staticTenantHosts{
		"zeta": digestHost("zeta-host", map[uint64][][2]string{1: {{"k", "z"}}}),
		"acme": digestHost("acme-host", map[uint64][][2]string{1: {{"k", "a"}}}),
	})

	resp, err := admin.StateHashes(context.Background(), nil)
	if err != nil {
		t.Fatalf("StateHashes: %v", err)
	}
	var got []string
	for _, r := range resp.Replicas {
		got = append(got, r.TenantID)
	}
	if strings.Join(got, ",") != ",acme,zeta" {
		t.Fatalf("tenant order = %q, want [\"\" acme zeta]", got)
	}
	if resp.NodeID != "node-a" {
		t.Fatalf("NodeID = %q, want the system host's ID", resp.NodeID)
	}
	if resp.Replicas[1].Hash == resp.Replicas[2].Hash {
		t.Fatal("tenants holding different records report the same hash")
	}

	// The filter applies inside tenant hosts too.
	resp, err = admin.StateHashes(context.Background(), []uint64{1})
	if err != nil {
		t.Fatalf("filtered: %v", err)
	}
	if len(resp.Replicas) != 2 || resp.Replicas[0].TenantID != "acme" {
		t.Fatalf("filtered replicas = %+v, want acme and zeta shard 1", resp.Replicas)
	}
}

func TestStateHashes_UnbindingTenantHosts(t *testing.T) {
	admin := newStateHashAdmin(digestHost("n", map[uint64][][2]string{1: nil}))
	admin.SetTenantReplicaHosts(staticTenantHosts{"acme": digestHost("a", map[uint64][][2]string{1: nil})})
	admin.SetTenantReplicaHosts(nil)

	resp, err := admin.StateHashes(context.Background(), nil)
	if err != nil {
		t.Fatalf("StateHashes: %v", err)
	}
	if len(resp.Replicas) != 1 || resp.Replicas[0].TenantID != "" {
		t.Fatalf("replicas = %+v, want the system shard only", resp.Replicas)
	}
}

func TestStateHashes_OneReplicaFailureIsItsOwn(t *testing.T) {
	host := digestHost("n", map[uint64][][2]string{1: {{"a", "1"}}, 2: nil, 3: nil})
	inner := host.staleReadFn
	host.staleReadFn = func(ctx context.Context, shardID uint64, query interface{}) (interface{}, error) {
		switch shardID {
		case 2:
			return nil, errors.New("replica not loaded")
		case 3:
			return []byte("not a digest"), nil
		}
		return inner(ctx, shardID, query)
	}
	admin := newStateHashAdmin(host)

	resp, err := admin.StateHashes(context.Background(), nil)
	if err != nil {
		t.Fatalf("StateHashes: %v, want per-replica errors only", err)
	}
	if len(resp.Replicas) != 3 {
		t.Fatalf("replicas = %+v, want 3", resp.Replicas)
	}
	if resp.Replicas[0].Error != "" || resp.Replicas[0].Hash == "" {
		t.Fatalf("healthy replica = %+v", resp.Replicas[0])
	}
	failed := resp.Replicas[1]
	if failed.Error != "replica not loaded" || failed.Hash != "" || failed.AppliedIndex != 0 || failed.Records != 0 {
		t.Fatalf("failed replica = %+v", failed)
	}
	if got := resp.Replicas[2].Error; got != (&StateDigestUnsupportedError{}).Error() {
		t.Fatalf("unsupported replica error = %q", got)
	}
}

func TestStateHashes_RefusesATenantBoundCaller(t *testing.T) {
	called := false
	host := digestHost("n", map[uint64][][2]string{1: nil})
	host.getNodeHostInfoFn = func(quicraft.NodeHostInfoOption) *quicraft.NodeHostInfo {
		called = true
		return &quicraft.NodeHostInfo{}
	}
	admin := newStateHashAdmin(host)

	_, err := admin.StateHashes(tenant.WithTenantID(context.Background(), "acme"), nil)
	var denied *tenant.CrossTenantDeniedError
	if !errors.As(err, &denied) {
		t.Fatalf("StateHashes as acme = %v; want *tenant.CrossTenantDeniedError", err)
	}
	if called {
		t.Fatal("a refused caller read the host")
	}

	if _, err := admin.StateHashes(tenant.WithTenantID(context.Background(), tenantid.System), nil); err != nil {
		t.Fatalf("StateHashes as system = %v", err)
	}
}

func TestStateHashes_CancelledContextStopsTenantHosts(t *testing.T) {
	admin := newStateHashAdmin(digestHost("n", map[uint64][][2]string{1: nil}))
	admin.SetTenantReplicaHosts(staticTenantHosts{"acme": digestHost("a", map[uint64][][2]string{1: nil})})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := admin.StateHashes(ctx, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("StateHashes after cancel = %v, want context.Canceled", err)
	}
}

func TestStateHashes_HostWithoutInfo(t *testing.T) {
	host := digestHost("n", nil)
	host.getNodeHostInfoFn = func(quicraft.NodeHostInfoOption) *quicraft.NodeHostInfo { return nil }

	resp, err := newStateHashAdmin(host).StateHashes(context.Background(), nil)
	if err != nil || len(resp.Replicas) != 0 {
		t.Fatalf("(%+v, %v), want no replicas and no error", resp, err)
	}
}

func TestShardFilter(t *testing.T) {
	if shardFilter(nil) != nil || shardFilter([]uint64{}) != nil {
		t.Fatal("an empty list must select every shard (nil filter)")
	}
	f := shardFilter([]uint64{4, 4, 9})
	if len(f) != 2 {
		t.Fatalf("filter = %v, want {4, 9}", f)
	}
	if _, ok := f[9]; !ok {
		t.Fatal("filter is missing shard 9")
	}
}

func TestReplicaStateDigest(t *testing.T) {
	host := digestHost("n", map[uint64][][2]string{1: {{"a", "b"}}})
	d, err := replicaStateDigest(context.Background(), host, 1)
	if err != nil || d.AppliedIndex != 1 || d.Records != 1 {
		t.Fatalf("(%+v, %v)", d, err)
	}

	host.staleReadFn = func(context.Context, uint64, interface{}) (interface{}, error) { return 42, nil }
	_, err = replicaStateDigest(context.Background(), host, 1)
	var unsupported *StateDigestUnsupportedError
	if !errors.As(err, &unsupported) {
		t.Fatalf("non-digest answer = %v, want *StateDigestUnsupportedError", err)
	}
}

func TestStateHashErrors(t *testing.T) {
	if got := (&ReplicaStateHashesUnsupportedError{}).Error(); !strings.Contains(got, "replica state hashes") {
		t.Fatalf("ReplicaStateHashesUnsupportedError = %q", got)
	}
	if got := (&StateDigestUnsupportedError{}).Error(); got != "state machine does not support state digests" {
		t.Fatalf("StateDigestUnsupportedError = %q", got)
	}
}
