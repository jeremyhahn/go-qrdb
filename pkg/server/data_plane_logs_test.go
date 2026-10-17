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

package server

import (
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/lease"
	"github.com/jeremyhahn/go-qrdb/pkg/store/storetest"
	"github.com/jeremyhahn/go-qrdb/pkg/watch"
)

// TestNewDataPlaneCreateFunc_EveryStateMachineHasItsOwnLogs runs the node
// create function's two-groups proof over the data-plane create function a
// tenant's and a database's groups are built from: each state machine gets
// its own log registry, so no group's audit chain or named log shares heads
// with another's.
func TestNewDataPlaneCreateFunc_EveryStateMachineHasItsOwnLogs(t *testing.T) {
	_, tb := registerVolumeTenant(t, newVolumePooledRegistry(t), "acme")
	hub := watch.NewHub()
	t.Cleanup(hub.Close)
	storetest.RequireOwnLogsPerStateMachine(t, newDataPlaneCreateFunc(tb, lease.NewLessor(), hub))
}
