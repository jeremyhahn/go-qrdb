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
	"testing"

	"github.com/jeremyhahn/go-quicraft/pkg/sm"
	"github.com/jeremyhahn/go-quicraft/pkg/sm/kv"

	"github.com/jeremyhahn/go-qrdb/pkg/cluster/featuregate"
	"github.com/jeremyhahn/go-qrdb/pkg/protocol"
	"github.com/jeremyhahn/go-qrdb/pkg/writemode"
)

// gatedHost is a Host whose groups can hold other binaries: it answers the
// feature gate with enabled, per shard, and records what it is asked.
type gatedHost struct {
	*mockHost
	enabled map[uint64]bool
	asked   []featuregate.Feature
}

func (h *gatedHost) FeatureEnabled(shardID uint64, feature featuregate.Feature) bool {
	h.asked = append(h.asked, feature)
	return h.enabled[shardID]
}

var _ FeatureGatedHost = (*gatedHost)(nil)

// In a group whose replicas do not all run a binary that applies OpGuarded,
// no write is ever proposed guarded -- on either proposal path -- and each
// is the exact command an older binary applies.
func TestPropose_MixedGroupNeverReceivesOpGuarded(t *testing.T) {
	inner, cmds := recordingHost(sm.Result{})
	host := &gatedHost{mockHost: inner, enabled: map[uint64]bool{1: false}}
	svc := NewKVService(host, 1)
	ctx := WithWriteGuard(context.Background(), mustDropFenceGuard(t, "acme", "orders"))

	if err := svc.Put(ctx, "k", []byte("v")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if err := svc.PutWithMode(ctx, "k", []byte("v"), writemode.Sync); err != nil {
		t.Fatalf("PutWithMode: %v", err)
	}
	want, _ := kv.EncodePut([]byte("k"), []byte("v"))
	if len(*cmds) != 2 {
		t.Fatalf("proposed %d commands, want 2", len(*cmds))
	}
	for i, cmd := range *cmds {
		if cmd[0] == protocol.OpGuarded || !bytes.Equal(cmd, want) {
			t.Fatalf("command %d = %x: a mixed group was sent OpGuarded", i, cmd)
		}
	}
	if len(host.asked) != 2 || host.asked[0] != featuregate.GuardedWrites {
		t.Fatalf("gate asked %v, want guarded-writes per proposal", host.asked)
	}
}

// Once every replica of the group advertises the feature, the same writes
// are proposed guarded.
func TestPropose_EnabledGroupReceivesOpGuarded(t *testing.T) {
	inner, cmds := recordingHost(sm.Result{})
	host := &gatedHost{mockHost: inner, enabled: map[uint64]bool{1: true}}
	svc := NewKVService(host, 1)
	ctx := WithWriteGuard(context.Background(), mustDropFenceGuard(t, "acme", "orders"))

	if err := svc.Put(ctx, "k", []byte("v")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if len(*cmds) != 1 || (*cmds)[0][0] != protocol.OpGuarded {
		t.Fatalf("proposed %x, want one OpGuarded command", *cmds)
	}
}

// A write with no guard never consults the gate.
func TestPropose_UnguardedWriteDoesNotConsultTheGate(t *testing.T) {
	inner, _ := recordingHost(sm.Result{})
	host := &gatedHost{mockHost: inner, enabled: map[uint64]bool{}}
	if err := NewKVService(host, 1).Put(context.Background(), "k", []byte("v")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if len(host.asked) != 0 {
		t.Fatalf("gate asked %v for an unguarded write", host.asked)
	}
}
