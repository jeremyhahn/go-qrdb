//go:build codec_json

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

// Factory partition-routing tests. Which partition a DAO resolves to decides
// which keyspace its entities land in, so a wrong answer here silently writes
// data where nothing will look for it.

package dao

import (
	"context"
	"strings"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
)

// TestNewFactoryWithConfigToleratesANilConfig covers the nil branch. The
// returned factory must still have a usable partition map, or the first
// SetEntityPartition would panic on a nil map.
func TestNewFactoryWithConfigToleratesANilConfig(t *testing.T) {
	f := NewFactoryWithConfig(nil)
	if f == nil {
		t.Fatal("NewFactoryWithConfig(nil) returned nil")
	}

	f.SetEntityPartition("users", "users-partition")
	if got := f.GetEntityPartition("users"); got != "users-partition" {
		t.Fatalf("GetEntityPartition = %q, want %q", got, "users-partition")
	}
}

// TestNewFactoryWithConfigSuppliesAPartitionMap covers the branch where a
// config is given but its EntityPartitions is nil -- the same panic risk as
// above, reached by a different path.
func TestNewFactoryWithConfigSuppliesAPartitionMap(t *testing.T) {
	f := NewFactoryWithConfig(&FactoryConfig{DefaultPartition: "default"})

	f.SetEntityPartition("sessions", "sessions-partition")
	if got := f.GetEntityPartition("sessions"); got != "sessions-partition" {
		t.Fatalf("GetEntityPartition = %q, want %q", got, "sessions-partition")
	}
	if got := f.DefaultPartition(); got != "default" {
		t.Fatalf("DefaultPartition = %q, want %q", got, "default")
	}
}

// TestNewFactoryWithConfigKeepsTheSuppliedMapping covers the fully-populated
// path, including that the caller's mapping survives construction.
func TestNewFactoryWithConfigKeepsTheSuppliedMapping(t *testing.T) {
	f := NewFactoryWithConfig(&FactoryConfig{
		EntityPartitions: EntityPartitionMapping{
			"users":    "users-partition",
			"sessions": "sessions-partition",
		},
	})

	if got := f.GetEntityPartition("users"); got != "users-partition" {
		t.Errorf("users -> %q, want %q", got, "users-partition")
	}
	if got := f.GetEntityPartition("sessions"); got != "sessions-partition" {
		t.Errorf("sessions -> %q, want %q", got, "sessions-partition")
	}
	if got := f.GetEntityPartition("absent"); got != "" {
		t.Errorf("an unmapped entity -> %q, want the empty string", got)
	}
}

// TestSetDefaultPartitionOverridesTheConfiguredValue covers the setter.
func TestSetDefaultPartitionOverridesTheConfiguredValue(t *testing.T) {
	f := NewFactoryWithConfig(&FactoryConfig{DefaultPartition: "original"})

	f.SetDefaultPartition("replacement")
	if got := f.DefaultPartition(); got != "replacement" {
		t.Fatalf("DefaultPartition = %q, want %q", got, "replacement")
	}
}

// TestHasPartitionedProviderReportsWhetherOneIsWired covers both branches. The
// answer decides whether partitioned routing is available at all, so a wrong
// "true" would send every lookup through a nil provider.
func TestHasPartitionedProviderReportsWhetherOneIsWired(t *testing.T) {
	without := NewFactoryWithConfig(&FactoryConfig{})
	if without.HasPartitionedProvider() {
		t.Error("a factory with no partitioned provider reported one")
	}

	with := NewFactoryWithConfig(&FactoryConfig{
		PartitionedProvider: func(string, uint64) kvstore.KVStore { return newMemoryKVStore() },
	})
	if !with.HasPartitionedProvider() {
		t.Error("a factory with a partitioned provider reported none")
	}
}

// TestKVStoreForPartitionPrefersThePartitionedProvider covers the three-way
// fallback. Silently falling back to the default store when a partition WAS
// named is the dangerous case: entities would be written to the shared
// keyspace while the caller believes they are partitioned.
func TestKVStoreForPartitionPrefersThePartitionedProvider(t *testing.T) {
	partitioned := newMemoryKVStore()
	fallback := newMemoryKVStore()

	var gotPartition string
	var gotShard uint64

	f := NewFactoryWithConfig(&FactoryConfig{
		KVStoreProvider: func(uint64) kvstore.KVStore { return fallback },
		PartitionedProvider: func(name string, shard uint64) kvstore.KVStore {
			gotPartition, gotShard = name, shard
			return partitioned
		},
	})

	got := f.KVStoreForPartition("users", 7)
	if got != partitioned {
		t.Fatal("a named partition did not route to the partitioned provider")
	}
	if gotPartition != "users" || gotShard != 7 {
		t.Fatalf("provider called with (%q, %d), want (%q, %d)", gotPartition, gotShard, "users", uint64(7))
	}
}

// TestKVStoreForPartitionFallsBackWhenUnpartitioned covers the two fallback
// branches: no partition named, and no partitioned provider configured.
func TestKVStoreForPartitionFallsBackWhenUnpartitioned(t *testing.T) {
	fallback := newMemoryKVStore()
	partitioned := newMemoryKVStore()

	t.Run("no partition named", func(t *testing.T) {
		f := NewFactoryWithConfig(&FactoryConfig{
			KVStoreProvider:     func(uint64) kvstore.KVStore { return fallback },
			PartitionedProvider: func(string, uint64) kvstore.KVStore { return partitioned },
		})
		if got := f.KVStoreForPartition("", 1); got != fallback {
			t.Fatal("an empty partition name must use the default provider")
		}
	})

	t.Run("no partitioned provider", func(t *testing.T) {
		f := NewFactoryWithConfig(&FactoryConfig{
			KVStoreProvider: func(uint64) kvstore.KVStore { return fallback },
		})
		if got := f.KVStoreForPartition("users", 1); got != fallback {
			t.Fatal("without a partitioned provider the default must be used")
		}
	})
}

// TestKVStoreForPartitionReturnsNilWithNoProviders covers the last branch. Nil
// is the honest answer for a factory wired with no stores at all; inventing
// one would hide a misconfiguration until the first write.
func TestKVStoreForPartitionReturnsNilWithNoProviders(t *testing.T) {
	f := NewFactoryWithConfig(&FactoryConfig{})
	if got := f.KVStoreForPartition("users", 1); got != nil {
		t.Fatalf("KVStoreForPartition = %v, want nil", got)
	}
}

// TestKVStoreUsesTheConfiguredProvider covers the direct accessor, including
// that the shard identifier reaches the provider unchanged.
func TestKVStoreUsesTheConfiguredProvider(t *testing.T) {
	store := newMemoryKVStore()
	var gotShard uint64

	f := NewFactoryWithConfig(&FactoryConfig{
		KVStoreProvider: func(shard uint64) kvstore.KVStore {
			gotShard = shard
			return store
		},
	})

	if got := f.KVStore(42); got != store {
		t.Fatal("KVStore did not return the provider's store")
	}
	if gotShard != 42 {
		t.Fatalf("provider called with shard %d, want 42", gotShard)
	}
}

// TestResolvePartitionFollowsItsDocumentedPriority pins the whole precedence
// chain in one place: explicit option, then entity mapping, then default,
// then empty. Each step overrides the next, and getting the order wrong routes
// entities to a partition the caller never asked for.
func TestResolvePartitionFollowsItsDocumentedPriority(t *testing.T) {
	f := NewFactoryWithConfig(&FactoryConfig{
		DefaultPartition: "default-partition",
		EntityPartitions: EntityPartitionMapping{"users": "mapped-partition"},
	})

	tests := []struct {
		name       string
		entityType string
		cfg        *Config
		want       string
	}{
		{
			name:       "explicit option wins over everything",
			entityType: "users",
			cfg:        &Config{Partition: "explicit-partition"},
			want:       "explicit-partition",
		},
		{
			name:       "entity mapping wins over the default",
			entityType: "users",
			cfg:        &Config{},
			want:       "mapped-partition",
		},
		{
			name:       "default applies to an unmapped entity",
			entityType: "unmapped",
			cfg:        &Config{},
			want:       "default-partition",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := f.resolvePartition(tc.entityType, tc.cfg); got != tc.want {
				t.Fatalf("resolvePartition(%q) = %q, want %q", tc.entityType, got, tc.want)
			}
		})
	}
}

// TestResolvePartitionReturnsEmptyWithNoDefault covers the final fallthrough,
// which is what an unpartitioned deployment relies on.
func TestResolvePartitionReturnsEmptyWithNoDefault(t *testing.T) {
	f := NewFactoryWithConfig(&FactoryConfig{})
	if got := f.resolvePartition("anything", &Config{}); got != "" {
		t.Fatalf("resolvePartition = %q, want the empty string", got)
	}
}

// TestSnowflakeNodeRangeErrorNamesTheLimit covers the typed error. The node id
// is operator-supplied, so the message has to state the valid range rather
// than only that the value was wrong.
func TestSnowflakeNodeRangeErrorNamesTheLimit(t *testing.T) {
	err := &SnowflakeNodeRangeError{NodeID: 5000}
	msg := err.Error()

	for _, want := range []string{"dao:", "snowflake", "0..1023"} {
		if !strings.Contains(msg, want) {
			t.Errorf("message %q does not mention %q", msg, want)
		}
	}
}

// TestSaveIfAbsentCreatesOnceAndRefusesTheSecond covers the conditional-write
// path. Exactly-once creation is what callers build single-use semantics on
// (invitation redemption, for one), so a second create reporting success would
// break every one of them.
func TestSaveIfAbsentCreatesOnceAndRefusesTheSecond(t *testing.T) {
	ctx := context.Background()
	store := newMemoryKVStore()

	d, err := New(store, "savers", func() *TestEntity { return &TestEntity{} })
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	first := &TestEntity{ID: 1, Name: "alice", Email: "alice@example.com"}
	stored, err := d.SaveIfAbsent(ctx, first)
	if err != nil {
		t.Fatalf("first SaveIfAbsent: %v", err)
	}
	if !stored {
		t.Fatal("the first SaveIfAbsent must store the entity")
	}

	second := &TestEntity{ID: 1, Name: "mallory", Email: "mallory@example.com"}
	stored, err = d.SaveIfAbsent(ctx, second)
	if err != nil {
		t.Fatalf("second SaveIfAbsent: %v", err)
	}
	if stored {
		t.Fatal("the second SaveIfAbsent must refuse an existing id")
	}

	// The original must be intact -- a refused write must not partially apply.
	got, err := d.Get(ctx, 1)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Name != "alice" {
		t.Fatalf("Name = %q, want %q -- the refused write overwrote the record", got.Name, "alice")
	}
}
