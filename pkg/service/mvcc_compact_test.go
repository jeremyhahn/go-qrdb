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
	"github.com/jeremyhahn/go-qrdb/pkg/protocol"
)

// TestMVCCKVService_Compact_RawServiceRefusesAndProposesNothing pins the
// removal of the store-global compaction: the raw shard service is shared by
// every prefix-isolated tenant, and compacting it whole discarded every
// tenant's history. It now refuses without proposing anything.
func TestMVCCKVService_Compact_RawServiceRefusesAndProposesNothing(t *testing.T) {
	host, cmds := recordingHost(sm.Result{})
	svc := NewMVCCKVService(host, 1)

	err := svc.Compact(context.Background(), 100)
	var scope *CompactScopeRequiredError
	if !errors.As(err, &scope) {
		t.Fatalf("Compact = %v (%T), want *CompactScopeRequiredError", err, err)
	}
	if len(*cmds) != 0 {
		t.Fatalf("a store-global compaction was proposed: %v", *cmds)
	}
}

func TestMVCCKVService_CompactPrefix_RefusesRevisionBelowOneWithoutProposing(t *testing.T) {
	for _, rev := range []int64{0, -7} {
		host, cmds := recordingHost(sm.Result{})
		err := NewMVCCKVService(host, 1).(PrefixCompactor).CompactPrefix(context.Background(), "acme/app/", rev)
		var invalid *CompactRevisionInvalidError
		if !errors.As(err, &invalid) || invalid.Revision != rev {
			t.Fatalf("CompactPrefix(%d) = %v (%T)", rev, err, err)
		}
		if len(*cmds) != 0 {
			t.Fatalf("CompactPrefix(%d) proposed %v", rev, *cmds)
		}
	}
}

// TestMVCCKVService_CompactPrefix_WaitsForTheFeatureGate: a group that may
// hold a replica unable to apply OpMVCCCompactPrefix is never sent it.
func TestMVCCKVService_CompactPrefix_WaitsForTheFeatureGate(t *testing.T) {
	inner, cmds := recordingHost(sm.Result{})
	host := &gatedHost{mockHost: inner, enabled: map[uint64]bool{1: false}}

	err := NewMVCCKVService(host, 1).(PrefixCompactor).CompactPrefix(context.Background(), "acme/app/", 5)
	var gate *FeatureNotEnabledError
	if !errors.As(err, &gate) || gate.Feature != featuregate.PrefixCompaction || gate.ShardID != 1 {
		t.Fatalf("CompactPrefix before the gate opened = %v (%T)", err, err)
	}
	if len(*cmds) != 0 {
		t.Fatalf("proposed before the gate opened: %v", *cmds)
	}
	if len(host.asked) != 1 || host.asked[0] != featuregate.PrefixCompaction {
		t.Fatalf("gate asked about %v, want [%s]", host.asked, featuregate.PrefixCompaction)
	}

	host.enabled[1] = true
	if err := NewMVCCKVService(host, 1).(PrefixCompactor).CompactPrefix(context.Background(), "acme/app/", 5); err != nil {
		t.Fatalf("CompactPrefix after the gate opened: %v", err)
	}
	want := mustEncode(protocol.EncodeMVCCCompactPrefix([]byte("acme/app/"), 5))
	if len(*cmds) != 1 || !bytes.Equal((*cmds)[0], want) {
		t.Fatalf("proposed %v, want exactly %v", *cmds, want)
	}
}

// TestMVCCKVService_CompactPrefix_TypesTheStateMachinesRefusals: the state
// machine re-checks the revision at apply and answers a refusal in the
// result; each reason reaches the caller as its own typed error.
func TestMVCCKVService_CompactPrefix_TypesTheStateMachinesRefusals(t *testing.T) {
	tests := []struct {
		name   string
		result []byte
		check  func(error) bool
	}{
		{
			name:   "already compacted",
			result: protocol.EncodeCompactRefusal(protocol.CompactRefusalCompacted, 4, 9),
			check: func(err error) bool {
				var e *RevisionCompactedError
				return errors.As(err, &e) && e.Requested == 4 && e.Compacted == 9
			},
		},
		{
			name:   "future revision",
			result: protocol.EncodeCompactRefusal(protocol.CompactRefusalFuture, 40, 9),
			check: func(err error) bool {
				var e *CompactRevisionFutureError
				return errors.As(err, &e) && e.Requested == 40 && e.Current == 9
			},
		},
		{
			name:   "invalid revision",
			result: protocol.EncodeCompactRefusal(protocol.CompactRefusalInvalid, 3, 0),
			check: func(err error) bool {
				var e *CompactRevisionInvalidError
				return errors.As(err, &e) && e.Revision == 3
			},
		},
		{
			// A reason this binary does not know is still an error -- the
			// generic invalid-command refusal -- never a success.
			name:   "unknown reason",
			result: protocol.EncodeCompactRefusal(protocol.CompactRefusalReason(200), 3, 0),
			check: func(err error) bool {
				var e *CommandRejectedError
				return errors.As(err, &e)
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			host, _ := recordingHost(sm.Result{Data: tt.result})
			err := NewMVCCKVService(host, 1).(PrefixCompactor).CompactPrefix(context.Background(), "acme/app/", 4)
			if !tt.check(err) {
				t.Fatalf("CompactPrefix = %v (%T)", err, err)
			}
		})
	}
}

func TestCompactErrors_Messages(t *testing.T) {
	for _, err := range []error{
		&CompactScopeRequiredError{},
		&CompactRevisionInvalidError{Revision: -1},
		&CompactRevisionFutureError{Requested: 9, Current: 3},
		&FeatureNotEnabledError{Feature: featuregate.PrefixCompaction, ShardID: 7},
		&CompactPrefixInvalidError{Prefix: "acme/"},
	} {
		if msg := err.Error(); len(msg) < len("service: ") || msg[:len("service: ")] != "service: " {
			t.Errorf("%T.Error() = %q, want a service: prefix", err, msg)
		}
	}
}

// TestMVCCKVService_CompactPrefix_RefusesAnUnsafePrefix: a prefix compaction
// is of exactly one database's keyspace -- a "<tenant>/<database>/" namespace,
// or "" for a store that is one database's own (a dedicated raft group). Any
// other prefix could span many databases ("acme/" is every database of acme,
// "acme/ap" is acme/app/ and acme/apples/) or part of one, and is refused
// before anything is proposed.
func TestMVCCKVService_CompactPrefix_RefusesAnUnsafePrefix(t *testing.T) {
	for _, prefix := range []string{"acme/", "acme/ap", "acme/app", "acme/app/deeper/", "/app/", "acme//", "p/"} {
		host, cmds := recordingHost(sm.Result{})
		err := NewMVCCKVService(host, 1).(PrefixCompactor).CompactPrefix(context.Background(), prefix, 5)
		var unsafe *CompactPrefixInvalidError
		if !errors.As(err, &unsafe) || unsafe.Prefix != prefix {
			t.Fatalf("CompactPrefix(%q) = %v (%T), want *CompactPrefixInvalidError", prefix, err, err)
		}
		if len(*cmds) != 0 {
			t.Fatalf("CompactPrefix(%q) proposed %v", prefix, *cmds)
		}
	}
	for _, prefix := range []string{"acme/app/", "system/system/", ""} {
		host, cmds := recordingHost(sm.Result{Value: 1})
		if err := NewMVCCKVService(host, 1).(PrefixCompactor).CompactPrefix(context.Background(), prefix, 5); err != nil {
			t.Fatalf("CompactPrefix(%q) = %v", prefix, err)
		}
		if len(*cmds) != 1 {
			t.Fatalf("CompactPrefix(%q) proposed %d commands", prefix, len(*cmds))
		}
	}
}
