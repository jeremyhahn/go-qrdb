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

package lifecycle

import (
	"context"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/cluster/featuregate"
	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/shard"
)

// gatedHost is a host whose groups can hold other binaries: it answers
// FeatureEnabled, as the raft adapter does from its feature gate. No other
// Host method is reached by newShardClaimer.
type gatedHost struct {
	service.Host
	enabled map[featuregate.Feature]bool
}

func (h gatedHost) FeatureEnabled(_ uint64, f featuregate.Feature) bool { return h.enabled[f] }

var _ service.FeatureGatedHost = gatedHost{}

// On a host that gates features, no shard ID is claimed -- and so no raft
// database created -- until cluster-shard-ids is active cluster-wide: a node
// still running a binary that allocates per node could issue the same ID.
func TestNewShardClaimer_WaitsForClusterShardIDs(t *testing.T) {
	store := kvstore.NewMemoryKVStore()
	host := gatedHost{enabled: map[featuregate.Feature]bool{}}
	claimer, err := newShardClaimer(store, host)
	if err != nil {
		t.Fatalf("newShardClaimer: %v", err)
	}
	_, err = claimer.ClaimShardID(context.Background())
	var inactive *featuregate.NotActiveError
	if !errors.As(err, &inactive) || inactive.Feature != featuregate.ClusterShardIDs {
		t.Fatalf("claim before activation = %v, want *featuregate.NotActiveError{cluster-shard-ids}", err)
	}
	if raw, _ := store.Get(context.Background(), shard.HighWaterKey); raw != nil {
		t.Fatalf("a refused claim wrote the high-water mark: %q", raw)
	}

	host.enabled[featuregate.ClusterShardIDs] = true
	if id, err := claimer.ClaimShardID(context.Background()); err != nil || id != shard.UserMin {
		t.Fatalf("claim after activation = %d, %v; want %d", id, err, shard.UserMin)
	}
}

// Another feature being active does not stand in for cluster-shard-ids.
func TestNewShardClaimer_OtherFeaturesDoNotActivateClaims(t *testing.T) {
	claimer, err := newShardClaimer(kvstore.NewMemoryKVStore(),
		gatedHost{enabled: map[featuregate.Feature]bool{featuregate.GuardedWrites: true}})
	if err != nil {
		t.Fatalf("newShardClaimer: %v", err)
	}
	var inactive *featuregate.NotActiveError
	if _, err := claimer.ClaimShardID(context.Background()); !errors.As(err, &inactive) {
		t.Fatalf("claim with only guarded-writes active = %v, want *featuregate.NotActiveError", err)
	}
}
