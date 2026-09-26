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

	"github.com/jeremyhahn/go-quicraft/pkg/sm"

	"github.com/jeremyhahn/go-qrdb/pkg/protocol"
	"github.com/jeremyhahn/go-qrdb/pkg/statemachine"
)

// updater is the one method this test drives on both state machines.
type updater interface {
	Update(ctx context.Context, entries []sm.Entry, results []sm.Result) error
}

// TestCapabilityRefusal_NamesTheMissingSubsystem pins the text a caller reads
// when a state machine was built without the subsystem a command needs. It
// said "engine: not open" for all of them, which names neither the subsystem
// nor the actual condition — the engine WAS open. Each refusal must now be the
// statemachine.ServiceNotConfiguredError for the subsystem it lacks.
func TestCapabilityRefusal_NamesTheMissingSubsystem(t *testing.T) {
	leaseGrant := mustEncode(protocol.EncodeLeaseGrant(60))
	tsWrite := mustEncode(protocol.EncodeTSWrite("m", 1, 1.0, nil))

	openMVCC := func(t *testing.T) updater {
		t.Helper()
		s := NewMVCCStateMachine(nil)
		ctx := context.Background()
		if _, err := s.Open(ctx, t.TempDir(), make(chan struct{})); err != nil {
			t.Fatalf("open mvcc sm: %v", err)
		}
		t.Cleanup(func() { _ = s.Close(ctx) })
		return s
	}
	openPebble := func(t *testing.T) updater {
		t.Helper()
		return newTestPebbleSM(t)
	}

	tests := []struct {
		name      string
		open      func(*testing.T) updater
		cmd       []byte
		subsystem string
	}{
		{"mvcc lease grant without a lessor", openMVCC, leaseGrant, "lease"},
		{"mvcc time-series write without a store", openMVCC, tsWrite, "timeseries"},
		{"pebble lease grant without a lessor", openPebble, leaseGrant, "lease"},
		{"pebble time-series write without a store", openPebble, tsWrite, "timeseries"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			results := make([]sm.Result, 1)
			if err := tt.open(t).Update(context.Background(), []sm.Entry{{Index: 1, Cmd: tt.cmd}}, results); err != nil {
				t.Fatalf("Update must not fail the apply: %v", err)
			}
			msg, kind, ok := protocol.DecodeAppErrorKind(results[0].Data)
			if !ok || kind != protocol.AppErrorCapability {
				t.Fatalf("result: ok=%v kind=%d; want an AppErrorCapability refusal", ok, kind)
			}
			want := (&statemachine.ServiceNotConfiguredError{Service: tt.subsystem}).Error()
			if msg != want {
				t.Fatalf("refusal text = %q, want %q", msg, want)
			}
		})
	}
}
