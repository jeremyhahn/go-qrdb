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
	"github.com/jeremyhahn/go-qrdb/pkg/audit"
	"github.com/jeremyhahn/go-qrdb/pkg/lease"
	"github.com/jeremyhahn/go-qrdb/pkg/seal/sealtest"
	"github.com/jeremyhahn/go-qrdb/pkg/watch"
)

// TestBuildSMCreateFunc_EverySMStorageBuildsUnderTheBarrier pins that the
// seal barrier refuses no --sm-storage at start: pebble and file storage are
// opened on the barrier's encfs and memory storage persists nothing, so every
// storage-generic --sm builds over each, as do the types that carry their own
// storage.
func TestBuildSMCreateFunc_EverySMStorageBuildsUnderTheBarrier(t *testing.T) {
	sealed := atrest.New(sealtest.UnsealedBarrier(t))
	lr, hub := lease.NewLessor(), watch.NewHub()
	t.Cleanup(hub.Close)

	for _, tc := range []struct{ smType, storage string }{
		{"disk", "file"},
		{"fast", "file"},
		{"disk", "pebble"},
		{"fast", ""},
		{"disk", "memory"},
		{"memory", ""},
		{"pebble", ""},
		{"mvcc", ""},
	} {
		f := &startFlags{smType: tc.smType, smStorage: tc.storage, dataDir: t.TempDir()}
		fn, err := buildSMCreateFunc(f, sealed, lr, hub)
		if err != nil {
			t.Fatalf("--sm %s --sm-storage %q under the barrier: %v", tc.smType, tc.storage, err)
		}
		if fn == nil {
			t.Errorf("--sm %s --sm-storage %q built no factory", tc.smType, tc.storage)
		}
	}

	// The barrier off: storage-backed disk builds, as it always has.
	f := &startFlags{smType: "disk", smStorage: "file", dataDir: t.TempDir()}
	if _, err := buildSMCreateFunc(f, atrest.New(nil), lr, hub); err != nil {
		t.Fatalf("--sm disk --sm-storage file with the barrier off: %v", err)
	}
}

// auditChainHolder is satisfied by both store.MVCCStateMachine and
// store.PebbleStateMachine.
type auditChainHolder interface {
	AuditChain() *audit.Chain
}

// TestBuildSMCreateFunc_AttachesAuditChainUnconditionally is audit-replicated
// review finding N4: every mvcc/pebble state machine this factory builds
// must carry a non-nil audit.Chain regardless of any per-node configuration,
// because a replica built without one answers protocol.OpAuditAppend with a
// refusal while a sibling replica of the same Raft group that has one
// applies the write -- state divergence within a single group, not a
// degraded feature. There is no flag here to turn this attachment off: that
// is the fix, so this test only has one shape to assert, not a table of
// configurations that toggle it.
func TestBuildSMCreateFunc_AttachesAuditChainUnconditionally(t *testing.T) {
	enc := atrest.New(nil)
	lr, hub := lease.NewLessor(), watch.NewHub()
	t.Cleanup(hub.Close)

	for _, smType := range []string{"mvcc", "pebble"} {
		t.Run(smType, func(t *testing.T) {
			f := &startFlags{smType: smType, dataDir: t.TempDir()}
			fn, err := buildSMCreateFunc(f, enc, lr, hub)
			if err != nil {
				t.Fatalf("buildSMCreateFunc(%s): %v", smType, err)
			}
			sm := fn(1, 1)
			holder, ok := sm.(auditChainHolder)
			if !ok {
				t.Fatalf("--sm %s state machine %T does not implement auditChainHolder", smType, sm)
			}
			if holder.AuditChain() == nil {
				t.Errorf("--sm %s state machine built with a nil audit.Chain attached", smType)
			}
		})
	}
}
