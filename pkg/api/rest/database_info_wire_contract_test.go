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

package rest

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/api/dbwire"
	"github.com/jeremyhahn/go-qrdb/pkg/compaction"
	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// databaseInfoFixture is the REST DatabaseInfo response the TypeScript SDK's
// contract test decodes. TestDatabaseInfoWire_FixtureIsTheRESTResponse holds
// it to the handler's real output, so the TypeScript side never tests against
// a shape the server does not send.
var databaseInfoFixture = filepath.Join("..", "..", "..", "sdk", "typescript", "test", "fixtures", "rest-database-info.json")

// fullDatabaseInfo sets every field the REST response carries, so every key
// of the wire shape appears in its encoding.
func fullDatabaseInfo() *database.DatabaseInfo {
	return &database.DatabaseInfo{
		TenantID: "acme",
		Name:     "orders",
		Backend:  database.BackendType("raft"),
		Status:   database.StatusType("open"),
		Protocol: database.ProtocolConfig{
			MySQL:      true,
			PostgreSQL: true,
			Etcd: &database.EtcdConfig{
				Enabled:             true,
				ListenAddr:          "0.0.0.0:2379",
				AdvertiseClientURLs: []string{"https://db.example.com:2379"},
			},
		},
		DataDir:      "/var/lib/qrdb/databases/orders",
		ShardID:      1007,
		Mode:         database.DatabaseMode("embedded"),
		StorageCodec: "json",
		Replicas:     3,
		DedicatedWAL: true,
		Raft: &database.DatabaseRaftConfig{
			ClusterMode:    database.RaftClusterMode("distributed"),
			StateStorage:   database.BackendFile,
			ReplicaID:      2,
			RaftAddress:    "db.example.com:63001",
			Peers:          []string{"db.example.com:63001"},
			LocalNodeCount: 3,
			Discovery:      database.DiscoveryMethod("static"),
			Static:         &database.StaticDiscoveryConfig{Peers: []string{"db.example.com:63001"}, SharedSecret: "example-static-secret"},
			Multicast: &database.MulticastDiscoveryConfig{
				SharedSecret:     "example-multicast-secret",
				MulticastGroup:   "239.255.0.1:63100",
				Interface:        "eth0",
				MinPeers:         2,
				AnnounceInterval: "1s",
				DiscoverTimeout:  "10s",
			},
			DNS:   &database.DNSDiscoveryConfig{Service: "qrdb", Proto: "tcp", Domain: "example.com", LookupTimeout: "5s"},
			Token: &database.TokenDiscoveryConfig{Endpoint: "https://db.example.com/discovery", Token: "example-token", ClusterSize: 3, Timeout: "30s"},
		},
		Barrier: &database.DatabaseBarrierConfig{Enabled: true, Mode: "shamir", SharesRequired: 2, SharesTotal: 3},
		Connection: &database.DatabaseConnectionConfig{
			Transport: "grpc",
			Address:   "db.example.com:63000",
			CAFile:    "/etc/qrdb/ca.pem",
			CertFile:  "/etc/qrdb/client.pem",
			KeyFile:   "/etc/qrdb/client-key.pem",
		},
		SQL: &database.SQLConfig{Concurrency: database.ConcurrencyPessimistic},
		// Every compaction key is set, so each is on the wire to compare:
		// this is a wire fixture, not a valid policy.
		Compaction: &compaction.Policy{Mode: compaction.Period, RetainRevisions: 500, RetainPeriod: "720h"},
		// Set beside a policy only so the key is on the wire to compare.
		CompactionUnavailable: true,
	}
}

// restDatabaseInfo is the generic JSON the REST handlers write for
// fullDatabaseInfo.
func restDatabaseInfo(t *testing.T) (raw []byte, generic map[string]any) {
	t.Helper()
	raw, err := json.MarshalIndent(dbwire.Info(fullDatabaseInfo()), "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &generic); err != nil {
		t.Fatal(err)
	}
	return raw, generic
}

func TestDatabaseInfoWire_FixtureIsTheRESTResponse(t *testing.T) {
	raw, want := restDatabaseInfo(t)
	data, err := os.ReadFile(databaseInfoFixture)
	if err != nil {
		t.Fatalf("read %s: %v", databaseInfoFixture, err)
	}
	var got map[string]any
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("decode %s: %v", databaseInfoFixture, err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("%s is not what the REST handler sends for fullDatabaseInfo; replace it with\n%s", databaseInfoFixture, raw)
	}
	for _, key := range []string{"shardId", "dataDir"} {
		if _, ok := got[key]; !ok {
			t.Fatalf("fixture has no %q key: it would not catch a misnamed %s", key, key)
		}
	}
}

// jsonPaths lists every key path in a decoded JSON object, an object's own
// path included; arrays are leaves.
func jsonPaths(v map[string]any, prefix string, into map[string]bool) {
	for k, child := range v {
		p := prefix + k
		into[p] = true
		if m, ok := child.(map[string]any); ok {
			jsonPaths(m, p+".", into)
		}
	}
}

// declaredPaths lists every JSON key path a Go type declares.
func declaredPaths(typ reflect.Type, prefix string, into map[string]bool) {
	for typ.Kind() == reflect.Pointer {
		typ = typ.Elem()
	}
	for i := range typ.NumField() {
		f := typ.Field(i)
		name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
		if name == "-" || !f.IsExported() {
			continue
		}
		if name == "" {
			name = f.Name
		}
		p := prefix + name
		into[p] = true
		ft := f.Type
		for ft.Kind() == reflect.Pointer {
			ft = ft.Elem()
		}
		if ft.Kind() == reflect.Struct {
			declaredPaths(ft, p+".", into)
		}
	}
}

// outermost keeps the paths whose parent is not itself in the set.
func outermost(paths map[string]bool) []string {
	var out []string
	for p := range paths {
		if i := strings.LastIndex(p, "."); i >= 0 && paths[p[:i]] {
			continue
		}
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

// valueAt returns the value at a key path of a decoded JSON object.
func valueAt(v map[string]any, path string) any {
	head, rest, nested := strings.Cut(path, ".")
	if !nested {
		return v[head]
	}
	child, _ := v[head].(map[string]any)
	return valueAt(child, rest)
}

func TestDatabaseInfoWire_GoSDKDecodesEveryField(t *testing.T) {
	raw, rest := restDatabaseInfo(t)

	var info transport.DatabaseInfo
	if err := json.Unmarshal(raw, &info); err != nil {
		t.Fatalf("Go SDK cannot decode the REST response: %v", err)
	}
	if info.ShardID != 1007 || info.DataDir != "/var/lib/qrdb/databases/orders" {
		t.Fatalf("Go SDK decoded shardId=%d dataDir=%q, want 1007 and the data dir", info.ShardID, info.DataDir)
	}

	// Every value the SDK holds is the value REST sent under the same key.
	sdkRaw, err := json.Marshal(info)
	if err != nil {
		t.Fatal(err)
	}
	var sdk map[string]any
	if err := json.Unmarshal(sdkRaw, &sdk); err != nil {
		t.Fatal(err)
	}
	restPaths := make(map[string]bool)
	jsonPaths(rest, "", restPaths)
	sdkHeld := make(map[string]bool)
	jsonPaths(sdk, "", sdkHeld)
	for p := range sdkHeld {
		if !restPaths[p] {
			t.Errorf("Go SDK holds %q, which the REST response does not carry", p)
			continue
		}
		got, want := valueAt(sdk, p), valueAt(rest, p)
		if _, object := got.(map[string]any); object {
			continue // its keys are compared one by one
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("Go SDK decoded %q as %v, REST sent %v", p, got, want)
		}
	}

	// Every key the SDK declares is a key REST sends, and every key REST
	// sends is one the SDK declares: no exceptions, so a new key, or a
	// renamed one, fails here.
	declared := make(map[string]bool)
	declaredPaths(reflect.TypeFor[transport.DatabaseInfo](), "", declared)
	for p := range declared {
		if !restPaths[p] {
			t.Errorf("Go SDK declares %q, which the REST response never sends: a misnamed key decodes to its zero value", p)
		}
	}
	undecoded := make(map[string]bool)
	for p := range restPaths {
		if !declared[p] {
			undecoded[p] = true
		}
	}
	if got := outermost(undecoded); len(got) != 0 {
		t.Fatalf("REST keys the Go SDK does not decode: %v", got)
	}
}
