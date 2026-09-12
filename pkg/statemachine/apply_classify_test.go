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

package statemachine

import (
	"errors"
	"fmt"
	"testing"

	"github.com/jeremyhahn/go-quicraft/pkg/sm"

	"github.com/jeremyhahn/go-qrdb/pkg/protocol"
)

// futureSubsystemRefusalError stands in for the NEXT compiled-out subsystem: a
// refusal type declared in a package this one has never heard of. The
// classifier must forgive it without being edited, which is the property the
// two-entry allowlist did not have and could not have (go-qrdb#520).
type futureSubsystemRefusalError struct{ name string }

func (e *futureSubsystemRefusalError) Error() string      { return e.name + " support not compiled" }
func (e *futureSubsystemRefusalError) Capability() string { return e.name }

// TestResolveApplyErrorForgivesAnUndeclaredFutureRefusal is the regression for
// the structural half of go-qrdb#520. Under the old allowlist this error took
// the default branch and halted the state machine; a new subsystem stub had to
// remember to edit a switch in another package to avoid bricking a shard.
func TestResolveApplyErrorForgivesAnUndeclaredFutureRefusal(t *testing.T) {
	res, stop, err := ResolveApplyError(nil, 9, sm.Result{}, &futureSubsystemRefusalError{name: "widgets"})
	if stop || err != nil {
		t.Fatalf("ResolveApplyError returned stop=%v err=%v; a declared refusal must "+
			"never halt Apply, whatever package declared it", stop, err)
	}
	msg, capability, ok := protocol.DecodeAppErrorKind(res.Data)
	if !ok || !capability {
		t.Fatalf("result payload %q is not an encoded CAPABILITY error", res.Data)
	}
	if msg != "widgets support not compiled" {
		t.Errorf("encoded refusal = %q, want the error's own text", msg)
	}
}

// TestResolveApplyErrorForgivesAWrappedRefusal proves a handler may add
// context without converting a refusal into a fault.
func TestResolveApplyErrorForgivesAWrappedRefusal(t *testing.T) {
	wrapped := fmt.Errorf("ts-write: %w", &futureSubsystemRefusalError{name: "timeseries"})
	res, stop, err := ResolveApplyError(nil, 9, sm.Result{}, wrapped)
	if stop || err != nil {
		t.Fatalf("ResolveApplyError returned stop=%v err=%v for a wrapped refusal", stop, err)
	}
	if _, capability, ok := protocol.DecodeAppErrorKind(res.Data); !ok || !capability {
		t.Fatalf("result payload %q is not an encoded capability error", res.Data)
	}
}

// TestResolveApplyErrorHaltsOnEverySilentError is the property the inversion
// deliberately did NOT take: an error that declares nothing is fatal. A
// replica that has actually diverged must stop rather than serve wrong data,
// so a forgotten declaration costs availability, never correctness.
func TestResolveApplyErrorHaltsOnEverySilentError(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
	}{
		{"bare sentinel", errors.New("disk I/O error")},
		{"typed storage fault", &StorageError{Op: "put", Inner: errors.New("corrupt sstable")}},
		{"typed decode fault", &IncompleteDataError{Reason: "truncated"}},
		{"wrapped fault", fmt.Errorf("apply: %w", errors.New("short write"))},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, stop, err := ResolveApplyError(nil, 9, sm.Result{}, tc.err)
			if !stop || !errors.Is(err, tc.err) {
				t.Fatalf("ResolveApplyError returned stop=%v err=%v; a silent error "+
					"must halt Apply and travel back verbatim", stop, err)
			}
		})
	}
}

// TestResolveApplyErrorPassesASuccessfulResultThrough covers the nil-error
// arm, which every caller takes on every ordinary entry.
func TestResolveApplyErrorPassesASuccessfulResultThrough(t *testing.T) {
	want := sm.Result{Value: 42, Data: []byte("payload")}
	got, stop, err := ResolveApplyError(nil, 9, want, nil)
	if stop || err != nil {
		t.Fatalf("ResolveApplyError returned stop=%v err=%v for a nil handler error", stop, err)
	}
	if got.Value != want.Value || string(got.Data) != string(want.Data) {
		t.Errorf("result = %+v, want %+v", got, want)
	}
}

// TestStatemachineRefusalsDeclareTheirCapability pins the two refusal types
// this package raises. The compile-time assertions in errors.go prove the
// interface is satisfied; this proves the strings identify the thing refused.
func TestStatemachineRefusalsDeclareTheirCapability(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want string
	}{
		{&ServiceNotConfiguredError{Service: "timeseries"}, "timeseries"},
		{&InvalidOpcodeError{Op: 42}, "opcode 42"},
	} {
		capErr, ok := protocol.AsCapabilityError(tc.err)
		if !ok {
			t.Fatalf("%T is not recognised as an absent capability", tc.err)
		}
		if got := capErr.Capability(); got != tc.want {
			t.Errorf("%T.Capability() = %q, want %q", tc.err, got, tc.want)
		}
	}
}
