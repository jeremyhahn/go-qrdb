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

package node

import (
	"log/slog"

	qrdbconfig "github.com/jeremyhahn/go-qrdb/pkg/config"
	"github.com/jeremyhahn/go-qrdb/pkg/replicaforward"
	"github.com/jeremyhahn/go-qrdb/pkg/server"
	"github.com/jeremyhahn/go-qrdb/pkg/server/lifecycle"
	sdk "github.com/jeremyhahn/go-qrdb/sdk/go"
)

// placementChangesOf is the node's placement change feed, or a nil watcher
// when it has none.
func placementChangesOf(n *lifecycle.Node) replicaforward.PlacementWatcher {
	if n == nil || n.PlacementChanges == nil {
		return nil
	}
	return n.PlacementChanges
}

// replicaForwardingOption composes replica forwarding for a clustered node's
// listeners, as qrdbd does for its own: a Locator over the member records
// every raft node publishes, its leader cache kept honest by the placement
// and member watches for the node's life. It returns nil -- the listeners
// then answer the typed refusal -- when forwarding is turned off, on a
// standalone node, or without the placement table's change feed. The
// listeners verify a replica against the server.WithCACertPool pool the
// embedder supplies.
func (n *Node) replicaForwardingOption(cfg *qrdbconfig.Config, standalone bool, placements replicaforward.PlacementWatcher, client *sdk.Client) (server.ServerOption, error) {
	if standalone || cfg == nil || !cfg.ReplicaForwarding.On() || placements == nil || client == nil {
		return nil, nil
	}
	timeout, err := cfg.ReplicaForwarding.TimeoutDuration()
	if err != nil {
		return nil, err
	}
	logger := n.logger
	if logger == nil {
		logger = slog.Default()
	}
	locator := replicaforward.Start(n.background(), client.NewMembershipStore(), placements, cfg.Raft.RaftAddress, logger)
	return server.WithReplicaForwarding(locator, timeout), nil
}
