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

package store

import (
	"context"
	"testing"

	"github.com/cockroachdb/pebble/v2"

	"github.com/jeremyhahn/go-quicraft/pkg/sm"

	"github.com/jeremyhahn/go-qrdb/pkg/protocol"
	"github.com/jeremyhahn/go-qrdb/pkg/vlog"
)

// TestNewMVCCCreateFuncWith_AppliesOptions holds each MVCCOption to the
// field it sets on the machine NewMVCCCreateFuncWith builds, and the built
// machine to applying a write.
func TestNewMVCCCreateFuncWith_AppliesOptions(t *testing.T) {
	for name, tc := range map[string]struct {
		opt     MVCCOption
		applied func(*MVCCStateMachine) bool
	}{
		"WithNoSync":            {WithNoSync(), func(s *MVCCStateMachine) bool { return s.noSync }},
		"WithLogs":              {WithLogs(), func(s *MVCCStateMachine) bool { return s.logs != nil }},
		"WithTimeSeriesEnabled": {WithTimeSeriesEnabled(), func(s *MVCCStateMachine) bool { return s.enableTimeSeries }},
	} {
		t.Run(name, func(t *testing.T) {
			s, ok := NewMVCCCreateFuncWith(nil, tc.opt)(1, 1).(*MVCCStateMachine)
			if !ok {
				t.Fatal("the create function did not build an MVCCStateMachine")
			}
			if !tc.applied(s) {
				t.Fatalf("%s was not applied", name)
			}
			ctx := context.Background()
			if _, err := s.Open(ctx, t.TempDir(), nil); err != nil {
				t.Fatalf("Open: %v", err)
			}
			t.Cleanup(func() { _ = s.Close(ctx) })
			results := make([]sm.Result, 1)
			cmd := mustEncode(protocol.EncodeMVCCPut([]byte("k"), []byte("v"), 0))
			if err := s.Update(ctx, []sm.Entry{{Index: 1, Cmd: cmd}}, results); err != nil {
				t.Fatalf("Update: %v", err)
			}
			if results[0].Value == 0 {
				t.Fatal("the put returned no revision")
			}
		})
	}
}

func TestWithWALDir_SetsTheWALDirectory(t *testing.T) {
	for _, dir := range []string{"/var/lib/qrdb/wal", ""} {
		o := &pebble.Options{WALDir: "unset"}
		WithWALDir(dir)(o)
		if o.WALDir != dir {
			t.Fatalf("WithWALDir(%q) set WALDir %q", dir, o.WALDir)
		}
	}
}

// TestWithLogs_GivesEachReplicaItsOwnRegistry holds WithLogs to building a
// fresh log registry per machine: replicas sharing one would see each
// other's logs.
func TestWithLogs_GivesEachReplicaItsOwnRegistry(t *testing.T) {
	cf := NewMVCCCreateFuncWith(nil, WithLogs())
	seen := make(map[*vlog.Chain]uint64)
	for _, replica := range []uint64{1, 2} {
		machine, ok := cf(1, replica).(*MVCCStateMachine)
		if !ok {
			t.Fatalf("replica %d: the create function did not build an MVCCStateMachine", replica)
		}
		if machine.logs == nil {
			t.Fatalf("replica %d: the log registry was not attached", replica)
		}
		if other, dup := seen[machine.logs]; dup {
			t.Fatalf("replicas %d and %d share one log registry", other, replica)
		}
		seen[machine.logs] = replica
	}
}
