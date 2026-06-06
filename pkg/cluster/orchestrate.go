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

	quicraftconfig "github.com/jeremyhahn/go-quicraft/pkg/config"
	"github.com/jeremyhahn/go-quicraft/pkg/sm"
)

// BootstrapShard forms a Raft shard from discovered peers. It builds the
// discovery method named by opts.Method, then runs the bootstrap sequence
// (discover -> validate -> quorum -> build member map -> StartShard) against
// node. This is the single reusable entry point for cluster formation, used
// by the qrdbd daemon and embeddable by downstream composers.
//
// The provided context is propagated to the discovery method, allowing the
// caller to cancel discovery during shutdown.
func BootstrapShard(
	ctx context.Context,
	node RaftNode,
	opts Options,
	shardID uint64,
	createFn sm.CreateFunc,
	shardCfg quicraftconfig.Config,
) error {
	disc, err := BuildDiscovery(opts)
	if err != nil {
		return err
	}
	// Bootstrap is one-shot: once the shard is started, ongoing discovery is
	// no longer needed. Stop the method so methods that run background
	// goroutines (e.g. multicast's announce/listen loop) and hold sockets are
	// released on both the success and error paths.
	defer func() { _ = disc.Stop() }()

	b := NewBootstrapper(Config{
		NodeID:      opts.NodeID,
		Address:     opts.RaftAddress,
		ShardID:     shardID,
		ReplicaID:   opts.NodeID,
		Discovery:   disc,
		CreateFn:    createFn,
		ShardConfig: shardCfg,
		MinPeers:    opts.MinPeers,
	}, node)

	return b.Bootstrap(ctx)
}
