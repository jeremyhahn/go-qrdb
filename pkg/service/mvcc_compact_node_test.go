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

package service

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-quicraft/pkg/sm"

	"github.com/jeremyhahn/go-qrdb/pkg/cluster/featuregate"
	"github.com/jeremyhahn/go-qrdb/pkg/feature"
	"github.com/jeremyhahn/go-qrdb/pkg/protocol"
)

func nodeCompactor(h Host) NodeKeyspaceCompactor {
	return NewMVCCKVService(h, 1).(NodeKeyspaceCompactor)
}

func TestMVCCKVService_CompactNodeKeyspace_Proposes(t *testing.T) {
	host, cmds := recordingHost(sm.Result{})
	if err := nodeCompactor(host).CompactNodeKeyspace(context.Background(), 12); err != nil {
		t.Fatalf("CompactNodeKeyspace: %v", err)
	}
	want := mustEncode(protocol.EncodeMVCCCompactNode(12))
	if len(*cmds) != 1 || !bytes.Equal((*cmds)[0], want) {
		t.Fatalf("proposed %v, want %v", *cmds, want)
	}
}

func TestMVCCKVService_CompactNodeKeyspace_Refusals(t *testing.T) {
	host, cmds := recordingHost(sm.Result{})
	var invalid *CompactRevisionInvalidError
	if err := nodeCompactor(host).CompactNodeKeyspace(context.Background(), 0); !errors.As(err, &invalid) {
		t.Fatalf("revision 0 = %v", err)
	}
	if len(*cmds) != 0 {
		t.Fatalf("an invalid revision was proposed: %v", *cmds)
	}

	inner, gatedCmds := recordingHost(sm.Result{})
	gated := &gatedHost{mockHost: inner, enabled: map[uint64]bool{1: false}}
	var gate *feature.NotActiveError
	if err := nodeCompactor(gated).CompactNodeKeyspace(context.Background(), 5); !errors.As(err, &gate) || gate.Feature != featuregate.NodeCompaction {
		t.Fatalf("before the gate = %v", err)
	}
	if len(*gatedCmds) != 0 {
		t.Fatalf("proposed before the gate opened: %v", *gatedCmds)
	}

	compacted, _ := recordingHost(sm.Result{Data: protocol.EncodeCompactRefusal(protocol.CompactRefusalCompacted, 5, 9)})
	var already *RevisionCompactedError
	if err := nodeCompactor(compacted).CompactNodeKeyspace(context.Background(), 5); !errors.As(err, &already) || already.Compacted != 9 {
		t.Fatalf("already compacted = %v", err)
	}
}
