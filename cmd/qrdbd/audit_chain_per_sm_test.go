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
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/atrest"
	"github.com/jeremyhahn/go-qrdb/pkg/lease"
	"github.com/jeremyhahn/go-qrdb/pkg/store/storetest"
	"github.com/jeremyhahn/go-qrdb/pkg/watch"
)

// TestBuildSMCreateFunc_EveryStateMachineHasItsOwnAuditChain starts two Raft
// groups on one node from ONE create function, for each state machine
// family, and proves their log registries share nothing: neither the audit
// chain nor a named log.
func TestBuildSMCreateFunc_EveryStateMachineHasItsOwnAuditChain(t *testing.T) {
	for _, smType := range []string{"mvcc", "pebble"} {
		t.Run(smType, func(t *testing.T) {
			hub := watch.NewHub()
			t.Cleanup(hub.Close)
			f := &startFlags{smType: smType, dataDir: t.TempDir()}
			createFn, err := buildSMCreateFunc(f, atrest.New(nil), lease.NewLessor(), hub)
			if err != nil {
				t.Fatalf("buildSMCreateFunc(%s): %v", smType, err)
			}
			storetest.RequireOwnLogsPerStateMachine(t, createFn)
		})
	}
}
