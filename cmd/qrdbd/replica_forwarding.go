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

package main

import (
	"context"
	"log/slog"

	qrdbconfig "github.com/jeremyhahn/go-qrdb/pkg/config"
	"github.com/jeremyhahn/go-qrdb/pkg/replicaforward"
	"github.com/jeremyhahn/go-qrdb/pkg/server"
	"github.com/jeremyhahn/go-qrdb/pkg/server/lifecycle"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// placementChanges is the node's placement change feed, or a nil watcher
// when it has none.
func placementChanges(n *lifecycle.Node) replicaforward.PlacementWatcher {
	if n == nil || n.PlacementChanges == nil {
		return nil
	}
	return n.PlacementChanges
}

// replicaForwardingOption composes replica forwarding for a raft-mode node:
// a Locator over the member records every raft node publishes, its leader
// cache kept honest by the placement and member watches until ctx ends. It
// returns nil -- the listeners then answer the typed refusal -- when
// forwarding is turned off, on a standalone node (no replicas elsewhere), or
// without the placement table's change feed (no database is placed on other
// nodes).
func replicaForwardingOption(
	ctx context.Context,
	f *startFlags,
	standalone bool,
	placements replicaforward.PlacementWatcher,
	kvSvc service.KVServicer,
	adminSvc service.AdminServicer,
	mvccSvc service.MVCCKVServicer,
	leaseSvc service.LeaseServicer,
	watchSvc service.WatchServicer,
) (server.ServerOption, error) {
	if standalone || !f.replicaForwarding || placements == nil {
		return nil, nil
	}
	timeout, err := qrdbconfig.ReplicaForwardingConfig{Timeout: f.replicaForwardingTimeout}.TimeoutDuration()
	if err != nil {
		return nil, err
	}
	client, err := newEmbeddedClient(kvSvc, adminSvc, mvccSvc, leaseSvc, watchSvc)
	if err != nil {
		return nil, err
	}
	// The watches run until the daemon's ctx ends at process shutdown; nothing
	// the daemon closes after that is read through them.
	locator, _ := replicaforward.Start(ctx, client.NewMembershipStore(), placements, f.raftAddress, slog.Default())
	slog.Info("replica forwarding enabled", "timeout", timeout)
	return server.WithReplicaForwarding(locator, timeout), nil
}
