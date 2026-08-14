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
	quicraft "github.com/jeremyhahn/go-quicraft/pkg"

	raftbackend "github.com/jeremyhahn/go-qrdb/pkg/backend/raft"
)

// Re-export the types from pkg/backend/raft under the package-main aliases
// that the rest of start.go uses. This keeps the daemon-internal callers
// unchanged while making HostAdapter and BootstrapAdapter available to
// external embedders through the exported pkg/backend/raft package.

// quicraftHost is the subset of *quicraft.Host methods that hostAdapter
// delegates to. Kept here as a local alias so existing unit tests in this
// package continue to compile without change.
type quicraftHost = raftbackend.QuicraftHost

// hostAdapter wraps a quicraft.Host to satisfy service.Host. It delegates
// all behaviour to pkg/backend/raft.HostAdapter.
type hostAdapter = raftbackend.HostAdapter

// newHostAdapter creates a hostAdapter backed by the given quicraft.Host.
func newHostAdapter(h *quicraft.Host) *hostAdapter {
	return raftbackend.New(h)
}

// bootstrapHostAdapter wraps hostAdapter and redirects SyncRead to
// StaleRead. Delegates to pkg/backend/raft.BootstrapAdapter.
type bootstrapHostAdapter = raftbackend.BootstrapAdapter

// newBootstrapHostAdapter creates a bootstrapHostAdapter from a hostAdapter.
// Used during auth bootstrap before the Raft ReadIndex pipeline is warmed up.
func newBootstrapHostAdapter(a *hostAdapter) *bootstrapHostAdapter {
	return raftbackend.NewBootstrapAdapter(a)
}
