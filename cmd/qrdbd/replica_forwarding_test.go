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

//go:build !integration

package main

import (
	"context"
	"errors"
	"testing"

	qrdbconfig "github.com/jeremyhahn/go-qrdb/pkg/config"
	"github.com/jeremyhahn/go-qrdb/pkg/placement"
	"github.com/jeremyhahn/go-qrdb/pkg/replicaforward"
	"github.com/jeremyhahn/go-qrdb/pkg/server/lifecycle"
)

func newForwardingPlacements(t *testing.T) *placement.Store {
	t.Helper()
	store, err := placement.NewStore(lifecycle.NewKVStoreAdapter(newMockKVStore(map[string][]byte{})))
	if err != nil {
		t.Fatal(err)
	}
	return store
}

// TestPlacementChanges: the node's feed, or no watcher at all -- never a
// typed nil a caller would take for one.
func TestPlacementChanges(t *testing.T) {
	if placementChanges(nil) != nil || placementChanges(&lifecycle.Node{}) != nil {
		t.Fatal("a node with no feed yielded a watcher")
	}
	feed := newForwardingPlacements(t)
	if placementChanges(&lifecycle.Node{PlacementChanges: feed}) == nil {
		t.Fatal("the node's feed was lost")
	}
}

// TestApplyConfigToFlags_ReplicaForwarding: the config section sets the
// flags a command line did not, and a flag given wins.
func TestApplyConfigToFlags_ReplicaForwarding(t *testing.T) {
	off := false
	cfg := &qrdbconfig.Config{ReplicaForwarding: qrdbconfig.ReplicaForwardingConfig{Enabled: &off, Timeout: "4s"}}

	cmd, f := newStartCmdWithFlags()
	if !f.replicaForwarding {
		t.Fatal("replica forwarding is off by default")
	}
	applyConfigToFlags(cmd, f, cfg)
	if f.replicaForwarding || f.replicaForwardingTimeout != "4s" {
		t.Fatalf("flags = %v, %q; want the config's off and 4s", f.replicaForwarding, f.replicaForwardingTimeout)
	}

	cmd, f = newStartCmdWithFlags()
	if err := cmd.Flags().Set("replica-forwarding", "true"); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Flags().Set("replica-forwarding-timeout", "2s"); err != nil {
		t.Fatal(err)
	}
	applyConfigToFlags(cmd, f, cfg)
	if !f.replicaForwarding || f.replicaForwardingTimeout != "2s" {
		t.Fatalf("flags = %v, %q; want the command line's on and 2s", f.replicaForwarding, f.replicaForwardingTimeout)
	}
}

// TestReplicaForwardingOption_ComposesOnARaftNode: a raft node with a
// placement table forwards by default.
func TestReplicaForwardingOption_ComposesOnARaftNode(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	f := &startFlags{replicaForwarding: true, replicaForwardingTimeout: "3s", raftAddress: "127.0.0.1:63099"}
	opt, err := replicaForwardingOption(ctx, f, false, newForwardingPlacements(t),
		newMockKVStore(map[string][]byte{}), &minimalAdminServicer{}, &mockMVCCKVServicer{mockKVServicer: &mockKVServicer{}},
		&mockLeaseServicer{}, &mockWatchServicer{})
	if err != nil || opt == nil {
		t.Fatalf("replicaForwardingOption = %v, %v; want the forwarding option", opt, err)
	}
}

// TestReplicaForwardingOption_RefusesUnwiredServices: forwarding asked for on
// a raft node whose services are not wired fails at the embedded client
// rather than start a locator it cannot read through.
func TestReplicaForwardingOption_RefusesUnwiredServices(t *testing.T) {
	f := &startFlags{replicaForwarding: true, replicaForwardingTimeout: "3s"}
	opt, err := replicaForwardingOption(context.Background(), f, false, newForwardingPlacements(t), nil, nil, nil, nil, nil)
	if err == nil || opt != nil {
		t.Fatalf("replicaForwardingOption = %v, %v; want the embedded client's error", opt, err)
	}
}

// TestReplicaForwardingOption_NotComposedWhereNothingForwards: off, a
// standalone node, or no placement table compose nothing; a bad timeout is
// the config's validation error.
func TestReplicaForwardingOption_NotComposedWhereNothingForwards(t *testing.T) {
	ctx := context.Background()
	on := &startFlags{replicaForwarding: true}
	for name, tc := range map[string]struct {
		f          *startFlags
		standalone bool
		placements replicaforward.PlacementWatcher
	}{
		"turned off":   {f: &startFlags{}, placements: newForwardingPlacements(t)},
		"standalone":   {f: on, standalone: true, placements: newForwardingPlacements(t)},
		"no placement": {f: on},
	} {
		opt, err := replicaForwardingOption(ctx, tc.f, tc.standalone, tc.placements, nil, nil, nil, nil, nil)
		if err != nil || opt != nil {
			t.Errorf("%s: replicaForwardingOption = %v, %v; want nothing", name, opt, err)
		}
	}
	bad := &startFlags{replicaForwarding: true, replicaForwardingTimeout: "never"}
	var ve *qrdbconfig.ValidationError
	if _, err := replicaForwardingOption(ctx, bad, false, newForwardingPlacements(t), nil, nil, nil, nil, nil); !errors.As(err, &ve) {
		t.Fatalf("a bad timeout = %v; want the config validation error", err)
	}
}
