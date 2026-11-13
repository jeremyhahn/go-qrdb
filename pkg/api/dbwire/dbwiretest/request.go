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

package dbwiretest

import (
	"reflect"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// The secrets FullRaftConfig and FullRaftRequest set, distinct from the
// seeded ones (Secrets) so a round trip cannot pass by keeping those.
const (
	FullStaticSecret    = "static-secret-7e1a5c3b9d"
	FullMulticastSecret = "multicast-secret-2f6d8b4a0c"
	FullDiscoveryToken  = "discovery-token-5c9e1b7d3a"
)

// FullRaftConfig sets every field a caller can set in a Raft configuration --
// all but the issued shard ID -- each to a value no default produces. It is
// what FullRaftRequest must arrive as on every transport.
func FullRaftConfig() *database.DatabaseRaftConfig {
	return &database.DatabaseRaftConfig{
		ClusterMode:    database.RaftDistributed,
		ReplicaID:      2,
		RaftAddress:    "db2.example.com:63001",
		Peers:          []string{"db2.example.com:63001", "db3.example.com:63001"},
		LocalNodeCount: 5,
		Discovery:      database.DiscoveryStatic,
		Static: &database.StaticDiscoveryConfig{
			Peers:        []string{"db4.example.com:63001"},
			SharedSecret: FullStaticSecret,
		},
		Multicast: &database.MulticastDiscoveryConfig{
			SharedSecret:     FullMulticastSecret,
			MulticastGroup:   "239.255.0.9:63100",
			Interface:        "eth1",
			MinPeers:         4,
			AnnounceInterval: "3s",
			DiscoverTimeout:  "45s",
		},
		DNS: &database.DNSDiscoveryConfig{
			Service:       "qrdb-raft",
			Proto:         "tcp",
			Domain:        "cluster.example.com",
			LookupTimeout: "7s",
		},
		Token: &database.TokenDiscoveryConfig{
			Endpoint:    "https://discovery.example.com/join",
			Token:       FullDiscoveryToken,
			ClusterSize: 5,
			Timeout:     "90s",
		},
		StateStorage: database.BackendFile,
	}
}

// FullRaftRequest is FullRaftConfig as an SDK request.
func FullRaftRequest() *transport.DatabaseRaftConfig {
	return &transport.DatabaseRaftConfig{
		ClusterMode:    "distributed",
		ReplicaID:      2,
		RaftAddress:    "db2.example.com:63001",
		Peers:          []string{"db2.example.com:63001", "db3.example.com:63001"},
		LocalNodeCount: 5,
		Discovery:      "static",
		Static: &transport.StaticDiscoveryConfig{
			Peers:        []string{"db4.example.com:63001"},
			SharedSecret: FullStaticSecret,
		},
		Multicast: &transport.MulticastDiscoveryConfig{
			SharedSecret:     FullMulticastSecret,
			MulticastGroup:   "239.255.0.9:63100",
			Interface:        "eth1",
			MinPeers:         4,
			AnnounceInterval: "3s",
			DiscoverTimeout:  "45s",
		},
		DNS: &transport.DNSDiscoveryConfig{
			Service:       "qrdb-raft",
			Proto:         "tcp",
			Domain:        "cluster.example.com",
			LookupTimeout: "7s",
		},
		Token: &transport.TokenDiscoveryConfig{
			Endpoint:    "https://discovery.example.com/join",
			Token:       FullDiscoveryToken,
			ClusterSize: 5,
			Timeout:     "90s",
		},
		StateStorage: "file",
	}
}

// AssertEveryFieldSet fails for any zero field reachable from v, skipping
// the named top-level fields. Run over FullRaftConfig and FullRaftRequest, it
// makes a field added to either type without a value here -- and so without
// a transport carrying it -- fail the round trips that use them.
func AssertEveryFieldSet(t testing.TB, v any, skip ...string) {
	t.Helper()
	skipped := make(map[string]bool, len(skip))
	for _, s := range skip {
		skipped[s] = true
	}
	assertSet(t, reflect.ValueOf(v), "", skipped)
}

func assertSet(t testing.TB, v reflect.Value, path string, skip map[string]bool) {
	t.Helper()
	switch v.Kind() {
	case reflect.Pointer:
		if v.IsNil() {
			t.Errorf("%s is nil", path)
			return
		}
		assertSet(t, v.Elem(), path, skip)
	case reflect.Struct:
		for i := range v.NumField() {
			f := v.Type().Field(i)
			if !f.IsExported() || (path == "" && skip[f.Name]) {
				continue
			}
			assertSet(t, v.Field(i), path+"."+f.Name, skip)
		}
	default:
		if v.IsZero() {
			t.Errorf("%s is zero", path)
		}
	}
}

// AssertRaftConfig fails unless got is FullRaftConfig, ignoring the issued
// shard ID.
func AssertRaftConfig(t testing.TB, what string, got *database.DatabaseRaftConfig) {
	t.Helper()
	if got == nil {
		t.Fatalf("%s: no Raft configuration", what)
	}
	c := *got
	c.ShardID = 0
	if want := FullRaftConfig(); !reflect.DeepEqual(&c, want) {
		t.Fatalf("%s: Raft configuration =\n%+v\nwant\n%+v", what, &c, want)
	}
}
