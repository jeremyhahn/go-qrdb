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

	"github.com/jeremyhahn/go-qrdb/pkg/placement"
	"github.com/jeremyhahn/go-qrdb/pkg/shard"
)

// placementLister is the slice of the placement store the shard seed reads.
type placementLister interface {
	List(ctx context.Context) ([]*placement.Placement, error)
}

// placementShardSeed returns the seed the cluster shard-ID claimer consults
// when it creates the replicated high-water mark: the highest shard ID any
// placement records. Placement is the one cluster-wide record of which shard
// IDs are in use, so a cluster whose nodes issued IDs locally before claims
// existed starts claiming past every one of them.
//
// A nil store yields a nil seed: a composition with no placement table has no
// dedicated-raft database another node could be serving.
func placementShardSeed(store *placement.Store) shard.SeedFunc {
	if store == nil {
		return nil
	}
	return seedFromPlacements(store)
}

// seedFromPlacements is placementShardSeed over the lister interface.
func seedFromPlacements(lister placementLister) shard.SeedFunc {
	return func(ctx context.Context) (uint64, error) {
		placements, err := lister.List(ctx)
		if err != nil {
			return 0, err
		}
		var highest uint64
		for _, p := range placements {
			if p != nil && p.ShardID > highest {
				highest = p.ShardID
			}
		}
		return highest, nil
	}
}
