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
	"github.com/jeremyhahn/go-qrdb/pkg/cluster/featuregate"
	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/shard"
)

// newShardClaimer builds the cluster shard-ID claimer over the replicated
// system store. A store without the atomic verbs cannot make a claim every
// node agrees on, and a node without a claimer would issue IDs another node
// may hold, so the build refuses rather than wire one.
//
// On a host whose groups can hold other binaries (a service.FeatureGatedHost)
// every claim waits for featuregate.ClusterShardIDs to be active, so no raft
// database is created while any node may still allocate shard IDs on its
// own. A host that gates nothing holds only this binary, and claims at once.
func newShardClaimer(store kvstore.KVStore, host service.Host) (*shard.Claimer, error) {
	claimStore, ok := store.(shard.ClaimStore)
	if !ok {
		return nil, &ShardClaimerError{Err: kvstore.ErrAtomicWriteUnsupported}
	}
	var opts []shard.ClaimerOption
	if gated, ok := host.(service.FeatureGatedHost); ok {
		opts = append(opts, shard.WithActivation(featuregate.ClusterShardIDs, func() bool {
			return gated.FeatureEnabled(shard.ShardKV, featuregate.ClusterShardIDs)
		}))
	}
	claimer, err := shard.NewClaimer(claimStore, opts...)
	if err != nil {
		return nil, &ShardClaimerError{Err: err}
	}
	return claimer, nil
}
