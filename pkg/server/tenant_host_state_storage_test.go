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
	"errors"
	"strings"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/lease"
	"github.com/jeremyhahn/go-qrdb/pkg/store"
	"github.com/jeremyhahn/go-qrdb/pkg/watch"
)

// TestNewDataPlaneCreateFunc_StateStorage pins the state storages a Raft
// group's data-plane state machine accepts: pebble (and the empty binding
// default) and memory open the Pebble-native MVCC store; file, which has no
// MVCC state machine, and any unknown value are refused with a typed error,
// never silently opened on Pebble.
func TestNewDataPlaneCreateFunc_StateStorage(t *testing.T) {
	_, tb := registerVolumeTenant(t, newVolumePooledRegistry(t), "acme")

	accepted := []database.BackendType{"", database.BackendPebble, database.BackendMemory}
	for _, ss := range accepted {
		t.Run("accepts_"+string(ss), func(t *testing.T) {
			hub := watch.NewHub()
			t.Cleanup(hub.Close)
			createFn, err := newDataPlaneCreateFunc(tb, lease.NewLessor(), hub, ss)
			if err != nil {
				t.Fatalf("newDataPlaneCreateFunc(%q): %v", ss, err)
			}
			if _, ok := createFn(9001, 1).(*store.MVCCStateMachine); !ok {
				t.Fatalf("state storage %q built a %T, want *store.MVCCStateMachine", ss, createFn(9001, 1))
			}
		})
	}

	refused := []database.BackendType{database.BackendFile, "bogus"}
	for _, ss := range refused {
		t.Run("refuses_"+string(ss), func(t *testing.T) {
			hub := watch.NewHub()
			t.Cleanup(hub.Close)
			createFn, err := newDataPlaneCreateFunc(tb, lease.NewLessor(), hub, ss)
			if createFn != nil {
				t.Fatalf("state storage %q returned a create function, want none", ss)
			}
			var unsupported *UnsupportedRaftStateStorageError
			if !errors.As(err, &unsupported) {
				t.Fatalf("state storage %q: err = %v, want *UnsupportedRaftStateStorageError", ss, err)
			}
			if unsupported.StateStorage != ss {
				t.Fatalf("error names %q, want %q", unsupported.StateStorage, ss)
			}
			if !strings.Contains(err.Error(), string(ss)) {
				t.Fatalf("error %q does not name the refused state storage %q", err, ss)
			}
		})
	}
}
